package dca

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"sync"
	"time"
)

const dataReadPollInterval = 100 * time.Millisecond

// maxTrackedOutputRanges bounds both coverage memory and per-packet range
// scans. Exceeding it means the capture is too sparse to preserve faithfully.
const maxTrackedOutputRanges = 4096

// RawModeTailFlushGuard is longer than the DCA1000 FPGA's fixed two-second
// raw-mode partial-packet flush interval. A receiver that declares the stream
// idle sooner can lose the final payload whenever the capture size is not an
// exact multiple of the 1456-byte Ethernet payload.
const RawModeTailFlushGuard = 2500 * time.Millisecond

var (
	ErrFirstPacketTimeout = errors.New("DCA1000 first data packet timeout")
	ErrReceiverClosed     = errors.New("DCA1000 data receiver closed")
	ErrReceiverAlreadyRun = errors.New("DCA1000 data receiver can only run once")
	errOutputRangeOverlap = errors.New("DCA1000 packet overlaps previously written output")
	errSparseRangeLimit   = errors.New("DCA1000 sparse output range limit exceeded")
)

// ReceiverConfig configures the raw ADC UDP receiver. The data socket is bound
// by NewReceiver, before Start or Run can be called, so higher layers can arm it
// before sending StartRecord and sensorStart.
type ReceiverConfig struct {
	DataBindAddress    net.IP
	DataBindPort       int
	DeviceIP           net.IP
	FirstPacketTimeout time.Duration
	IdleTimeout        time.Duration
	ReceiveBufferBytes int
	MaxOutputBytes     int64
	// ExpectedOutputBytes makes a finite capture eligible to complete only after
	// the exact contiguous range [0, ExpectedOutputBytes) has arrived and then
	// remained quiet for IdleTimeout. Zero leaves completion controlled by the
	// normal idle window.
	ExpectedOutputBytes int64
	// CadenceFrameBytes and CadenceFramePeriod optionally describe the
	// finite frame layout. When both are set, the receiver retains the packet
	// that implies the earliest possible frame-zero time. A coordinator can
	// compare that monotonic timestamp with the instant it issued sensorStart
	// without confusing DCA packet aggregation or the delayed short tail with
	// an early radar stop.
	CadenceFrameBytes  int64
	CadenceFramePeriod time.Duration
}

func DefaultReceiverConfig() ReceiverConfig {
	return ReceiverConfig{
		DataBindAddress:    net.ParseIP("192.168.33.30"),
		DataBindPort:       4098,
		DeviceIP:           net.ParseIP("192.168.33.180"),
		FirstPacketTimeout: 30 * time.Second,
		IdleTimeout:        RawModeTailFlushGuard,
		ReceiveBufferBytes: 16 * 1024 * 1024,
		MaxOutputBytes:     64 << 30,
	}
}

// CaptureStats describes raw datagrams and output coverage. OutputBytes spans
// from the first accepted DCA byte offset to the highest accepted packet end;
// MissingBytes counts holes still uncovered within that interval.
type CaptureStats struct {
	PacketsReceived            uint64
	PayloadBytesReceived       uint64
	OutputBytes                int64
	SequenceGaps               uint64
	OutOfOrderPackets          uint64
	MissingBytes               uint64
	DiscardedBeforeBasePackets uint64
	IgnoredSourcePackets       uint64
	MalformedPackets           uint64
	OverlappingPackets         uint64
	BaseByteOffset             uint64
	FirstPacketAt              time.Time
	LastPacketAt               time.Time
	// EarliestImpliedStartAt is the minimum, over accepted packets, of the
	// packet arrival time minus its last byte's zero-based frame offset. FPGA
	// buffering and UDP latency can only move this bound later. The remaining
	// anchor fields identify the packet that established it.
	EarliestImpliedStartAt time.Time
	CadenceAnchorAt        time.Time
	CadenceAnchorEndOffset int64
	CadenceAnchorFrame     uint64
}

type byteRange struct {
	start int64
	end   int64
}

// Receiver is a one-shot raw-data receiver. NewReceiver binds the data UDP
// endpoint synchronously. Start runs in the background; Run is its synchronous
// counterpart. After the radar stops, Wait lets the configured idle window
// drain tail packets. A caller-imposed deadline followed by Close provides an
// absolute bound even if data never becomes idle.
type Receiver struct {
	conn   *net.UDPConn
	config ReceiverConfig

	stateMu sync.Mutex
	started bool
	closed  bool
	stats   CaptureStats
	runErr  error

	firstOnce     sync.Once
	first         chan struct{}
	firstWaitOnce sync.Once
	firstWait     chan time.Time
	doneOnce      sync.Once
	done          chan struct{}
	closeOnce     sync.Once
	closeErr      error
}

// NewReceiver validates the configuration and immediately binds the data UDP
// socket. A zero DataBindPort requests an ephemeral port for loopback tests.
func NewReceiver(config ReceiverConfig) (*Receiver, error) {
	bindIP, err := requireIPv4("data bind address", config.DataBindAddress)
	if err != nil {
		return nil, err
	}
	deviceIP, err := requireIPv4("data device address", config.DeviceIP)
	if err != nil {
		return nil, err
	}
	if config.DataBindPort < 0 || config.DataBindPort > 65535 {
		return nil, fmt.Errorf("DCA1000 data bind port must be in 0..65535, got %d", config.DataBindPort)
	}
	if config.FirstPacketTimeout <= 0 {
		return nil, fmt.Errorf("DCA1000 first-packet timeout must be positive, got %s", config.FirstPacketTimeout)
	}
	if config.IdleTimeout <= 0 {
		return nil, fmt.Errorf("DCA1000 idle timeout must be positive, got %s", config.IdleTimeout)
	}
	if config.ReceiveBufferBytes < 0 {
		return nil, fmt.Errorf("DCA1000 receive buffer size cannot be negative, got %d", config.ReceiveBufferBytes)
	}
	if config.MaxOutputBytes <= 0 {
		return nil, fmt.Errorf("DCA1000 maximum output size must be positive, got %d", config.MaxOutputBytes)
	}
	if config.ExpectedOutputBytes < 0 {
		return nil, fmt.Errorf("DCA1000 expected output size cannot be negative, got %d", config.ExpectedOutputBytes)
	}
	if config.ExpectedOutputBytes > config.MaxOutputBytes {
		return nil, fmt.Errorf(
			"DCA1000 expected output size %d exceeds maximum output size %d",
			config.ExpectedOutputBytes,
			config.MaxOutputBytes,
		)
	}
	if (config.CadenceFrameBytes == 0) != (config.CadenceFramePeriod == 0) {
		return nil, errors.New("DCA1000 cadence frame bytes and period must either both be zero or both be set")
	}
	if config.CadenceFrameBytes < 0 {
		return nil, fmt.Errorf("DCA1000 cadence frame bytes cannot be negative, got %d", config.CadenceFrameBytes)
	}
	if config.CadenceFramePeriod < 0 {
		return nil, fmt.Errorf("DCA1000 cadence frame period cannot be negative, got %s", config.CadenceFramePeriod)
	}
	if config.CadenceFrameBytes > 0 {
		if config.ExpectedOutputBytes <= 0 {
			return nil, errors.New("DCA1000 cadence validation requires a finite expected output size")
		}
		lastFrame := uint64((config.ExpectedOutputBytes - 1) / config.CadenceFrameBytes)
		if lastFrame > uint64(math.MaxInt64/int64(config.CadenceFramePeriod)) {
			return nil, errors.New("DCA1000 cadence frame offset exceeds supported duration")
		}
	}
	if config.ExpectedOutputBytes > 0 {
		// The exact target is also the strongest write bound. Extra bytes are a
		// capture mismatch, not data that may be silently retained past target.
		config.MaxOutputBytes = config.ExpectedOutputBytes
	}
	config.DataBindAddress = bindIP
	config.DeviceIP = deviceIP

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: bindIP, Port: config.DataBindPort})
	if err != nil {
		return nil, fmt.Errorf("bind DCA1000 data socket %s:%d: %w", bindIP, config.DataBindPort, err)
	}
	if config.ReceiveBufferBytes > 0 {
		// OS limits differ across Windows and Linux. A smaller effective buffer is
		// not a reason to make an otherwise usable socket fail to open.
		_ = conn.SetReadBuffer(config.ReceiveBufferBytes)
	}
	return &Receiver{
		conn:      conn,
		config:    config,
		first:     make(chan struct{}),
		firstWait: make(chan time.Time, 1),
		done:      make(chan struct{}),
	}, nil
}

func (receiver *Receiver) LocalEndpoint() *net.UDPAddr {
	if receiver == nil || receiver.conn == nil {
		return nil
	}
	endpoint, _ := receiver.conn.LocalAddr().(*net.UDPAddr)
	return cloneUDPAddr(endpoint)
}

// Start begins receiving in a goroutine. The socket was already bound by
// NewReceiver, so a successful return means StartRecord may safely follow.
func (receiver *Receiver) Start(ctx context.Context, output io.WriterAt) error {
	if output == nil {
		return errors.New("DCA1000 output writer is required")
	}
	if err := receiver.claimRun(); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		stats, err := receiver.receive(ctx, output)
		receiver.finish(stats, err)
	}()
	return nil
}

// Run receives synchronously until the first-packet timeout, context
// cancellation, an error, or a quiet window after valid data. An exact output
// range must first become complete, then remain quiet for the same bounded
// window so an overlong stream cannot hide immediately beyond the write cap.
func (receiver *Receiver) Run(ctx context.Context, output io.WriterAt) (CaptureStats, error) {
	if output == nil {
		return CaptureStats{}, errors.New("DCA1000 output writer is required")
	}
	if err := receiver.claimRun(); err != nil {
		return CaptureStats{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	receiver.beginFirstPacketWindow()
	stats, err := receiver.receive(ctx, output)
	receiver.finish(stats, err)
	return stats, err
}

// WaitForFirst blocks until the first accepted payload was written, the run
// ended, or ctx expired.
func (receiver *Receiver) WaitForFirst(ctx context.Context) error {
	if receiver == nil {
		return errors.New("nil DCA1000 receiver")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	receiver.beginFirstPacketWindow()
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-receiver.first:
		if err := ctx.Err(); err != nil {
			return err
		}
		return nil
	case <-receiver.done:
		if err := ctx.Err(); err != nil {
			return err
		}
		stats, err := receiver.result()
		if !stats.FirstPacketAt.IsZero() {
			return nil
		}
		if err != nil {
			return err
		}
		return ErrFirstPacketTimeout
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Wait returns the final result of a receiver started with Start. It does not
// stop the receiver when ctx expires; call Close to enforce a hard shutdown.
func (receiver *Receiver) Wait(ctx context.Context) (CaptureStats, error) {
	if receiver == nil {
		return CaptureStats{}, errors.New("nil DCA1000 receiver")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	receiver.stateMu.Lock()
	started := receiver.started
	receiver.stateMu.Unlock()
	if !started {
		return CaptureStats{}, errors.New("DCA1000 receiver has not been started")
	}
	receiver.beginFirstPacketWindow()
	if err := ctx.Err(); err != nil {
		return receiver.Stats(), err
	}
	select {
	case <-receiver.done:
		stats, err := receiver.result()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return stats, ctxErr
		}
		return stats, err
	case <-ctx.Done():
		return receiver.Stats(), ctx.Err()
	}
}

func (receiver *Receiver) Stats() CaptureStats {
	if receiver == nil {
		return CaptureStats{}
	}
	receiver.stateMu.Lock()
	defer receiver.stateMu.Unlock()
	return receiver.stats
}

func (receiver *Receiver) Close() error {
	if receiver == nil {
		return nil
	}
	receiver.closeOnce.Do(func() {
		receiver.stateMu.Lock()
		receiver.closed = true
		receiver.stateMu.Unlock()
		receiver.closeErr = receiver.conn.Close()
	})
	receiver.stateMu.Lock()
	started := receiver.started
	receiver.stateMu.Unlock()
	if started {
		// Closing a UDPConn interrupts ReadFromUDP. Joining here freezes stats and
		// guarantees no WriteAt remains concurrent with capture-file publication.
		<-receiver.done
	}
	return receiver.closeErr
}

// ReceiveRaw is a synchronous convenience wrapper. Use NewReceiver directly
// when the caller must prove the data socket is bound before StartRecord.
func ReceiveRaw(ctx context.Context, config ReceiverConfig, output io.WriterAt) (CaptureStats, error) {
	receiver, err := NewReceiver(config)
	if err != nil {
		return CaptureStats{}, err
	}
	defer receiver.Close()
	return receiver.Run(ctx, output)
}

// ReceiveRawFile creates a new file without overwriting existing evidence and
// receives raw ADC bytes into it. Failed captures are intentionally retained.
func ReceiveRawFile(ctx context.Context, config ReceiverConfig, path string) (CaptureStats, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return CaptureStats{}, fmt.Errorf("create raw DCA1000 output %s: %w", path, err)
	}
	stats, receiveErr := ReceiveRaw(ctx, config, file)
	syncErr := file.Sync()
	closeErr := file.Close()
	if receiveErr != nil || syncErr != nil || closeErr != nil {
		return stats, errors.Join(receiveErr, syncErr, closeErr)
	}
	return stats, nil
}

func (receiver *Receiver) claimRun() error {
	if receiver == nil {
		return errors.New("nil DCA1000 receiver")
	}
	receiver.stateMu.Lock()
	defer receiver.stateMu.Unlock()
	if receiver.started {
		return ErrReceiverAlreadyRun
	}
	if receiver.closed {
		return ErrReceiverClosed
	}
	receiver.started = true
	return nil
}

func (receiver *Receiver) receive(ctx context.Context, output io.WriterAt) (CaptureStats, error) {
	if output == nil {
		return CaptureStats{}, errors.New("DCA1000 output writer is required")
	}
	var stats CaptureStats
	var ranges []byteRange
	var baseOffset uint64
	baseSet := false
	var highestOutputOffset int64
	var lastSequence uint32
	lastSequenceSet := false
	expectedComplete := false
	var firstPacketDeadline time.Time
	buffer := make([]byte, 65535)

	for {
		if err := ctx.Err(); err != nil {
			stats = finalizeStats(stats, ranges, highestOutputOffset)
			receiver.publishStats(stats)
			return stats, err
		}
		now := time.Now()
		if firstPacketDeadline.IsZero() && stats.FirstPacketAt.IsZero() {
			select {
			case waitStarted := <-receiver.firstWait:
				firstPacketDeadline = waitStarted.Add(receiver.config.FirstPacketTimeout)
			default:
			}
		}
		var phaseDeadline time.Time
		if stats.FirstPacketAt.IsZero() {
			phaseDeadline = firstPacketDeadline
		} else if (receiver.config.ExpectedOutputBytes == 0 || expectedComplete) && !stats.LastPacketAt.IsZero() {
			phaseDeadline = stats.LastPacketAt.Add(receiver.config.IdleTimeout)
		}
		if !phaseDeadline.IsZero() && !now.Before(phaseDeadline) {
			stats = finalizeStats(stats, ranges, highestOutputOffset)
			receiver.publishStats(stats)
			if stats.FirstPacketAt.IsZero() {
				return stats, ErrFirstPacketTimeout
			}
			return stats, nil
		}
		readDeadline := now.Add(dataReadPollInterval)
		if !phaseDeadline.IsZero() && phaseDeadline.Before(readDeadline) {
			readDeadline = phaseDeadline
		}
		if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(readDeadline) {
			readDeadline = contextDeadline
		}
		if err := receiver.conn.SetReadDeadline(readDeadline); err != nil {
			if errors.Is(err, net.ErrClosed) {
				stats = finalizeStats(stats, ranges, highestOutputOffset)
				receiver.publishStats(stats)
				return stats, ErrReceiverClosed
			}
			return stats, fmt.Errorf("set DCA1000 data read deadline: %w", err)
		}
		length, source, err := receiver.conn.ReadFromUDP(buffer)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				stats = finalizeStats(stats, ranges, highestOutputOffset)
				receiver.publishStats(stats)
				return stats, ctxErr
			}
			if errors.Is(err, net.ErrClosed) {
				stats = finalizeStats(stats, ranges, highestOutputOffset)
				receiver.publishStats(stats)
				return stats, ErrReceiverClosed
			}
			if networkError, ok := err.(net.Error); ok && networkError.Timeout() {
				continue
			}
			return stats, fmt.Errorf("receive DCA1000 data: %w", err)
		}
		if source == nil || !source.IP.Equal(receiver.config.DeviceIP) {
			stats.IgnoredSourcePackets++
			receiver.publishStats(stats)
			continue
		}
		packet, err := ParseDataPacket(buffer[:length])
		if err != nil {
			stats.MalformedPackets++
			receiver.publishStats(stats)
			continue
		}
		receivedAt := time.Now()
		if !baseSet {
			baseOffset = packet.ByteOffset
			baseSet = true
			stats.BaseByteOffset = baseOffset
		}
		if packet.ByteOffset < baseOffset {
			stats.OutOfOrderPackets++
			stats.DiscardedBeforeBasePackets++
			receiver.publishStats(stats)
			continue
		}
		relativeUnsigned := packet.ByteOffset - baseOffset
		if relativeUnsigned > math.MaxInt64 {
			return stats, errors.New("DCA1000 byte offset exceeds local file limits")
		}
		relativeOffset := int64(relativeUnsigned)
		if uint64(len(packet.Payload)) > uint64(math.MaxInt64-relativeOffset) {
			return stats, errors.New("DCA1000 packet end exceeds local file limits")
		}
		packetEnd := relativeOffset + int64(len(packet.Payload))
		if packetEnd > receiver.config.MaxOutputBytes {
			return stats, fmt.Errorf(
				"DCA1000 packet end %d exceeds maximum output size %d",
				packetEnd,
				receiver.config.MaxOutputBytes,
			)
		}
		addition := byteRange{start: relativeOffset, end: packetEnd}
		if err := preflightRangeAddition(ranges, addition); err != nil {
			if errors.Is(err, errOutputRangeOverlap) {
				stats.OverlappingPackets++
			}
			stats = finalizeStats(stats, ranges, highestOutputOffset)
			receiver.publishStats(stats)
			return stats, err
		}
		written, err := output.WriteAt(packet.Payload, relativeOffset)
		if err != nil {
			return stats, fmt.Errorf("write DCA1000 payload at offset %d: %w", relativeOffset, err)
		}
		if written != len(packet.Payload) {
			return stats, fmt.Errorf("write DCA1000 payload at offset %d: %w", relativeOffset, io.ErrShortWrite)
		}
		ranges = addRange(ranges, addition)
		if packetEnd > highestOutputOffset {
			highestOutputOffset = packetEnd
		}

		advanceSequence := true
		if lastSequenceSet {
			expected := lastSequence + 1
			if packet.Sequence != expected {
				delta := packet.Sequence - expected
				if delta < 0x80000000 {
					stats.SequenceGaps += uint64(delta)
				} else {
					stats.OutOfOrderPackets++
					advanceSequence = false
				}
			}
		}
		if advanceSequence {
			lastSequence = packet.Sequence
			lastSequenceSet = true
		}

		stats.PacketsReceived++
		stats.PayloadBytesReceived += uint64(len(packet.Payload))
		firstAcceptedPacket := stats.FirstPacketAt.IsZero()
		if firstAcceptedPacket {
			stats.FirstPacketAt = receivedAt
		}
		if receiver.config.CadenceFrameBytes > 0 {
			frameIndex := uint64((packetEnd - 1) / receiver.config.CadenceFrameBytes)
			frameOffset := time.Duration(frameIndex * uint64(receiver.config.CadenceFramePeriod))
			impliedStart := receivedAt.Add(-frameOffset)
			if stats.EarliestImpliedStartAt.IsZero() || impliedStart.Before(stats.EarliestImpliedStartAt) {
				stats.EarliestImpliedStartAt = impliedStart
				stats.CadenceAnchorAt = receivedAt
				stats.CadenceAnchorEndOffset = packetEnd
				stats.CadenceAnchorFrame = frameIndex
			}
		}
		stats.LastPacketAt = receivedAt
		stats.OutputBytes = highestOutputOffset
		receiver.publishStats(stats)
		if firstAcceptedPacket {
			// Publish the timestamp and all first-packet metadata before waking
			// WaitForFirst. Session deadlines are anchored from Stats immediately
			// after that wait returns.
			receiver.firstOnce.Do(func() { close(receiver.first) })
		}
		if receiver.config.ExpectedOutputBytes > 0 &&
			rangesComplete(ranges, receiver.config.ExpectedOutputBytes) {
			expectedComplete = true
		}
	}
}

func (receiver *Receiver) beginFirstPacketWindow() {
	if receiver == nil {
		return
	}
	receiver.firstWaitOnce.Do(func() {
		receiver.firstWait <- time.Now()
	})
}

func (receiver *Receiver) finish(stats CaptureStats, err error) {
	receiver.stateMu.Lock()
	receiver.stats = stats
	receiver.runErr = err
	receiver.stateMu.Unlock()
	receiver.doneOnce.Do(func() { close(receiver.done) })
}

func (receiver *Receiver) result() (CaptureStats, error) {
	receiver.stateMu.Lock()
	defer receiver.stateMu.Unlock()
	return receiver.stats, receiver.runErr
}

func (receiver *Receiver) publishStats(stats CaptureStats) {
	receiver.stateMu.Lock()
	receiver.stats = stats
	receiver.stateMu.Unlock()
}

func addRange(ranges []byteRange, addition byteRange) []byteRange {
	index := 0
	for index < len(ranges) && ranges[index].end < addition.start {
		index++
	}
	for index < len(ranges) && ranges[index].start <= addition.end {
		if ranges[index].start < addition.start {
			addition.start = ranges[index].start
		}
		if ranges[index].end > addition.end {
			addition.end = ranges[index].end
		}
		ranges = append(ranges[:index], ranges[index+1:]...)
	}
	ranges = append(ranges, byteRange{})
	copy(ranges[index+1:], ranges[index:])
	ranges[index] = addition
	return ranges
}

func preflightRangeAddition(ranges []byteRange, addition byteRange) error {
	resultingCount := len(ranges) + 1
	for _, current := range ranges {
		if current.end < addition.start {
			continue
		}
		if current.start > addition.end {
			break
		}
		if current.end > addition.start && current.start < addition.end {
			return fmt.Errorf(
				"%w: packet [%d,%d)",
				errOutputRangeOverlap,
				addition.start,
				addition.end,
			)
		}
		// Exact adjacency is merged without concealing a gap.
		resultingCount--
	}
	if resultingCount > maxTrackedOutputRanges {
		return fmt.Errorf(
			"%w: limit=%d, refusing packet [%d,%d)",
			errSparseRangeLimit,
			maxTrackedOutputRanges,
			addition.start,
			addition.end,
		)
	}
	return nil
}

func rangesComplete(ranges []byteRange, expected int64) bool {
	return expected > 0 && len(ranges) == 1 && ranges[0].start == 0 && ranges[0].end == expected
}

func finalizeStats(stats CaptureStats, ranges []byteRange, highestOutputOffset int64) CaptureStats {
	stats.OutputBytes = highestOutputOffset
	coveredUntil := int64(0)
	missing := uint64(0)
	for _, current := range ranges {
		if current.start > coveredUntil {
			missing += uint64(current.start - coveredUntil)
		}
		if current.end > coveredUntil {
			coveredUntil = current.end
		}
	}
	if highestOutputOffset > coveredUntil {
		missing += uint64(highestOutputOffset - coveredUntil)
	}
	stats.MissingBytes = missing
	return stats
}
