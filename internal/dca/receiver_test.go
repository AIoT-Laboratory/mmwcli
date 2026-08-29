package dca

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func TestReceiverBindsBeforeStartAndReassemblesRawOffsets(t *testing.T) {
	config := DefaultReceiverConfig()
	config.DataBindAddress = net.IPv4(127, 0, 0, 1)
	config.DataBindPort = 0
	config.DeviceIP = net.IPv4(127, 0, 0, 2)
	config.FirstPacketTimeout = time.Second
	config.IdleTimeout = 60 * time.Millisecond
	config.ReceiveBufferBytes = 64 * 1024
	receiver, err := NewReceiver(config)
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	if endpoint := receiver.LocalEndpoint(); endpoint == nil || endpoint.Port == 0 {
		t.Fatalf("receiver was not bound before Start: %v", endpoint)
	}

	output, err := os.CreateTemp(t.TempDir(), "raw-*.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	if err := receiver.Start(context.Background(), output); err != nil {
		t.Fatal(err)
	}

	device := listenUDP(t, config.DeviceIP)
	wrongSource := listenUDP(t, net.IPv4(127, 0, 0, 3))
	destination := receiver.LocalEndpoint()
	sendDataPacket(t, wrongSource, destination, 1, 100, []byte{99})
	if _, err := device.WriteToUDP(make([]byte, DataHeaderSize), destination); err != nil {
		t.Fatal(err)
	}
	sendDataPacket(t, device, destination, 10, 100, []byte{1, 2, 3, 4})
	sendDataPacket(t, device, destination, 12, 108, []byte{9, 10})
	sendDataPacket(t, device, destination, 11, 104, []byte{5, 6, 7, 8})

	firstContext, cancelFirst := context.WithTimeout(context.Background(), time.Second)
	defer cancelFirst()
	if err := receiver.WaitFirst(firstContext); err != nil {
		t.Fatal(err)
	}
	waitContext, cancelWait := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelWait()
	stats, err := receiver.Wait(waitContext)
	if err != nil {
		t.Fatal(err)
	}
	if stats.PacketsReceived != 3 || stats.PayloadBytesReceived != 10 || stats.OutputBytes != 10 {
		t.Fatalf("unexpected basic stats: %+v", stats)
	}
	if stats.SequenceGaps != 1 || stats.OutOfOrderPackets != 1 || stats.MissingBytes != 0 {
		t.Fatalf("unexpected ordering/coverage stats: %+v", stats)
	}
	if stats.IgnoredSourcePackets != 1 || stats.MalformedPackets != 1 || stats.BaseByteOffset != 100 {
		t.Fatalf("unexpected filtering stats: %+v", stats)
	}
	if stats.FirstPacketAt.IsZero() || stats.LastPacketAt.IsZero() {
		t.Fatalf("packet timestamps were not recorded: %+v", stats)
	}
	if err := output.Sync(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}; !bytes.Equal(data, want) {
		t.Fatalf("raw output = %v, want %v", data, want)
	}
	if _, err := receiver.Run(context.Background(), output); !errors.Is(err, ErrReceiverAlreadyRun) {
		t.Fatalf("second Run error = %v, want ErrReceiverAlreadyRun", err)
	}
}

func TestReceiverFirstPacketTimeoutAndClose(t *testing.T) {
	config := DefaultReceiverConfig()
	config.DataBindAddress = net.IPv4(127, 0, 0, 1)
	config.DataBindPort = 0
	config.DeviceIP = net.IPv4(127, 0, 0, 2)
	config.FirstPacketTimeout = 40 * time.Millisecond
	config.IdleTimeout = 20 * time.Millisecond
	config.ReceiveBufferBytes = 0
	receiver, err := NewReceiver(config)
	if err != nil {
		t.Fatal(err)
	}
	output, err := os.CreateTemp(t.TempDir(), "empty-*.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	stats, err := receiver.Run(context.Background(), output)
	if !errors.Is(err, ErrFirstPacketTimeout) || stats.PacketsReceived != 0 {
		t.Fatalf("Run = (%+v, %v), want first-packet timeout", stats, err)
	}
	if err := receiver.Close(); err != nil {
		t.Fatal(err)
	}

	config.FirstPacketTimeout = time.Second
	receiver, err = NewReceiver(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := receiver.Start(context.Background(), output); err != nil {
		t.Fatal(err)
	}
	if err := receiver.Close(); err != nil {
		t.Fatal(err)
	}
	waitContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = receiver.Wait(waitContext)
	if !errors.Is(err, ErrReceiverClosed) {
		t.Fatalf("Wait after Close error = %v, want ErrReceiverClosed", err)
	}
}

func TestReceiverCanRejectMalformedPacketImmediately(t *testing.T) {
	config := DefaultReceiverConfig()
	config.DataBindAddress = net.IPv4(127, 0, 0, 1)
	config.DataBindPort = 0
	config.DeviceIP = net.IPv4(127, 0, 0, 2)
	config.FirstPacketTimeout = time.Second
	config.IdleTimeout = time.Second
	config.RejectMalformed = true
	receiver, err := NewReceiver(config)
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	output, err := os.CreateTemp(t.TempDir(), "strict-*.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	if err := receiver.Start(context.Background(), output); err != nil {
		t.Fatal(err)
	}
	device := listenUDP(t, config.DeviceIP)
	defer device.Close()
	if _, err := device.WriteToUDP(make([]byte, DataHeaderSize), receiver.LocalEndpoint()); err != nil {
		t.Fatal(err)
	}
	waitContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stats, err := receiver.Wait(waitContext)
	if err == nil || !strings.Contains(err.Error(), "malformed") || stats.MalformedPackets != 1 {
		t.Fatalf("Wait = (%+v, %v), want immediate malformed-packet failure", stats, err)
	}
}

func TestFreshStartReceiverRejectsLostFirstPacketBeforeWriting(t *testing.T) {
	config := DefaultReceiverConfig()
	config.DataBindAddress = net.IPv4(127, 0, 0, 1)
	config.DataBindPort = 0
	config.DeviceIP = net.IPv4(127, 0, 0, 2)
	config.FirstPacketTimeout = time.Second
	config.IdleTimeout = time.Second
	config.RequireFreshStart = true
	receiver, err := NewReceiver(config)
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()

	var output bytes.Buffer
	frames, err := NewFrameWriter(4, &output)
	if err != nil {
		t.Fatal(err)
	}
	if err := receiver.Start(context.Background(), frames); err != nil {
		t.Fatal(err)
	}
	device := listenUDP(t, config.DeviceIP)
	defer device.Close()
	// Packet 1 (bytes 0..4) was lost. Without a fresh-start anchor, packet 2
	// would be rebased to zero and silently emitted as a complete frame.
	sendDataPacket(t, device, receiver.LocalEndpoint(), 2, 4, []byte("efgh"))

	waitContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stats, err := receiver.Wait(waitContext)
	if !errors.Is(err, errFirstPacketNotFresh) {
		t.Fatalf("Wait = (%+v, %v), want fresh-start failure", stats, err)
	}
	if output.Len() != 0 || stats.PacketsReceived != 0 {
		t.Fatalf(
			"lost first packet produced output: bytes=%q stats=%+v",
			output.Bytes(),
			stats,
		)
	}
}

func TestFreshStartReceiverAcceptsPacketOneAtZero(t *testing.T) {
	config := DefaultReceiverConfig()
	config.DataBindAddress = net.IPv4(127, 0, 0, 1)
	config.DataBindPort = 0
	config.DeviceIP = net.IPv4(127, 0, 0, 2)
	config.FirstPacketTimeout = time.Second
	config.IdleTimeout = 20 * time.Millisecond
	config.RequireFreshStart = true
	receiver, err := NewReceiver(config)
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()

	var output bytes.Buffer
	frames, err := NewFrameWriter(4, &output)
	if err != nil {
		t.Fatal(err)
	}
	if err := receiver.Start(context.Background(), frames); err != nil {
		t.Fatal(err)
	}
	device := listenUDP(t, config.DeviceIP)
	defer device.Close()
	sendDataPacket(t, device, receiver.LocalEndpoint(), 1, 0, []byte("abcd"))

	waitContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stats, err := receiver.Wait(waitContext)
	if err != nil {
		t.Fatal(err)
	}
	if output.String() != "abcd" || stats.PacketsReceived != 1 || stats.BaseByteOffset != 0 {
		t.Fatalf("fresh stream output=%q stats=%+v", output.String(), stats)
	}
}

func TestFirstPacketTimeoutStartsWhenCallerWaitsAfterArm(t *testing.T) {
	config := DefaultReceiverConfig()
	config.DataBindAddress = net.IPv4(127, 0, 0, 1)
	config.DataBindPort = 0
	config.DeviceIP = net.IPv4(127, 0, 0, 2)
	config.FirstPacketTimeout = 40 * time.Millisecond
	config.IdleTimeout = 20 * time.Millisecond
	receiver, err := NewReceiver(config)
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	output, err := os.CreateTemp(t.TempDir(), "armed-*.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	if err := receiver.Start(context.Background(), output); err != nil {
		t.Fatal(err)
	}

	// Arming the data socket must not consume the post-sensorStart timeout.
	time.Sleep(2 * config.FirstPacketTimeout)
	device := listenUDP(t, config.DeviceIP)
	defer device.Close()
	sendDataPacket(t, device, receiver.LocalEndpoint(), 1, 0, []byte{1, 2, 3, 4})
	waitContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := receiver.WaitFirst(waitContext); err != nil {
		t.Fatalf("packet after a long arm interval was rejected: %v", err)
	}
	if receiver.Stats().FirstPacketAt.IsZero() {
		t.Fatal("WaitFirst returned before first-packet stats were published")
	}
}

func TestReceiverExpectedOutputWaitsPastIdleAndAcceptsLateCoverage(t *testing.T) {
	config := DefaultReceiverConfig()
	config.DataBindAddress = net.IPv4(127, 0, 0, 1)
	config.DataBindPort = 0
	config.DeviceIP = net.IPv4(127, 0, 0, 2)
	config.FirstPacketTimeout = 100 * time.Millisecond
	config.IdleTimeout = 50 * time.Millisecond
	config.ReceiveBufferBytes = 64 * 1024
	config.MaxOutputBytes = 6
	config.ExpectedOutputBytes = 6
	receiver, err := NewReceiver(config)
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()

	output, err := os.CreateTemp(t.TempDir(), "finite-tail-*.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	if err := receiver.Start(context.Background(), output); err != nil {
		t.Fatal(err)
	}

	device := listenUDP(t, config.DeviceIP)
	defer device.Close()
	destination := receiver.LocalEndpoint()
	sendDataPacket(t, device, destination, 1, 100, []byte("ab"))
	firstContext, cancelFirst := context.WithTimeout(context.Background(), time.Second)
	defer cancelFirst()
	if err := receiver.WaitFirst(firstContext); err != nil {
		t.Fatal(err)
	}

	// A finite target must remain armed beyond the ordinary idle window while
	// the DCA1000 holds its short final payload in the FPGA.
	prematureContext, cancelPremature := context.WithTimeout(context.Background(), 3*config.IdleTimeout)
	_, err = receiver.Wait(prematureContext)
	cancelPremature()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait before tail = %v, want caller deadline rather than idle completion", err)
	}

	// A missing range may still arrive out of order during the quiet window.
	sendDataPacket(t, device, destination, 2, 104, []byte("ef"))
	holeContext, cancelHole := context.WithTimeout(context.Background(), config.IdleTimeout/3)
	_, err = receiver.Wait(holeContext)
	cancelHole()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait before hole quiet window = %v, want caller deadline", err)
	}

	sendDataPacket(t, device, destination, 3, 102, []byte("cd"))
	completeContext, cancelComplete := context.WithTimeout(context.Background(), time.Second)
	defer cancelComplete()
	stats, err := receiver.Wait(completeContext)
	if err != nil {
		t.Fatal(err)
	}
	if stats.OutputBytes != config.ExpectedOutputBytes || stats.MissingBytes != 0 || stats.PacketsReceived != 3 {
		t.Fatalf("completed stats = %+v", stats)
	}
	if err := output.Sync(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, []byte("abcdef")) {
		t.Fatalf("raw output = %q, want abcdef", data)
	}
}

func TestReceiverExpectedOutputSettlesAfterQuietWindowWithUnresolvedHole(t *testing.T) {
	config := DefaultReceiverConfig()
	config.DataBindAddress = net.IPv4(127, 0, 0, 1)
	config.DataBindPort = 0
	config.DeviceIP = net.IPv4(127, 0, 0, 2)
	config.FirstPacketTimeout = 100 * time.Millisecond
	config.IdleTimeout = 20 * time.Millisecond
	config.ReceiveBufferBytes = 64 * 1024
	config.MaxOutputBytes = 6
	config.ExpectedOutputBytes = 6
	receiver, err := NewReceiver(config)
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()

	output, err := os.CreateTemp(t.TempDir(), "finite-hole-*.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	if err := receiver.Start(context.Background(), output); err != nil {
		t.Fatal(err)
	}

	device := listenUDP(t, config.DeviceIP)
	defer device.Close()
	destination := receiver.LocalEndpoint()
	sendDataPacket(t, device, destination, 1, 100, []byte("ab"))
	sendDataPacket(t, device, destination, 3, 104, []byte("ef"))

	waitContext, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	stats, err := receiver.Wait(waitContext)
	if err != nil {
		t.Fatal(err)
	}
	if stats.OutputBytes != 6 || stats.MissingBytes != 2 || stats.SequenceGaps != 1 {
		t.Fatalf("hole stats = %+v", stats)
	}
}

func TestReceiverRetainsEarliestPacketCadenceAnchor(t *testing.T) {
	config := DefaultReceiverConfig()
	config.DataBindAddress = net.IPv4(127, 0, 0, 1)
	config.DataBindPort = 0
	config.DeviceIP = net.IPv4(127, 0, 0, 2)
	config.FirstPacketTimeout = time.Second
	config.IdleTimeout = 20 * time.Millisecond
	config.ReceiveBufferBytes = 64 * 1024
	config.MaxOutputBytes = 12
	config.ExpectedOutputBytes = 12
	config.CadenceFrameBytes = 4
	config.CadenceFramePeriod = time.Second
	receiver, err := NewReceiver(config)
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()

	output, err := os.CreateTemp(t.TempDir(), "cadence-*.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	if err := receiver.Start(context.Background(), output); err != nil {
		t.Fatal(err)
	}

	device := listenUDP(t, config.DeviceIP)
	defer device.Close()
	destination := receiver.LocalEndpoint()
	sendDataPacket(t, device, destination, 1, 100, []byte("abcd"))
	sendDataPacket(t, device, destination, 2, 104, []byte("efgh"))
	sendDataPacket(t, device, destination, 3, 108, []byte("ijkl"))

	waitContext, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	stats, err := receiver.Wait(waitContext)
	if err != nil {
		t.Fatal(err)
	}
	if stats.CadenceAnchorFrame != 2 || stats.CadenceAnchorEndOffset != 12 {
		t.Fatalf("cadence anchor = frame %d end %d, want frame 2 end 12", stats.CadenceAnchorFrame, stats.CadenceAnchorEndOffset)
	}
	wantImplied := stats.CadenceAnchorAt.Add(-2 * time.Second)
	if !stats.EarliestImpliedStartAt.Equal(wantImplied) {
		t.Fatalf("implied start = %s, want %s", stats.EarliestImpliedStartAt, wantImplied)
	}
}

func TestFiniteReceiverKeepsWatchingForDataBeyondExactTarget(t *testing.T) {
	config := DefaultReceiverConfig()
	config.DataBindAddress = net.IPv4(127, 0, 0, 1)
	config.DataBindPort = 0
	config.DeviceIP = net.IPv4(127, 0, 0, 2)
	config.FirstPacketTimeout = time.Second
	config.IdleTimeout = 80 * time.Millisecond
	config.ReceiveBufferBytes = 64 * 1024
	config.MaxOutputBytes = 4
	config.ExpectedOutputBytes = 4
	receiver, err := NewReceiver(config)
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()

	output, err := os.CreateTemp(t.TempDir(), "overlong-*.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	if err := receiver.Start(context.Background(), output); err != nil {
		t.Fatal(err)
	}
	device := listenUDP(t, config.DeviceIP)
	defer device.Close()
	destination := receiver.LocalEndpoint()
	sendDataPacket(t, device, destination, 1, 100, []byte("abcd"))
	if err := receiver.WaitFirst(context.Background()); err != nil {
		t.Fatal(err)
	}

	prematureContext, cancelPremature := context.WithTimeout(context.Background(), config.IdleTimeout/3)
	_, err = receiver.Wait(prematureContext)
	cancelPremature()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait at exact target = %v, want post-target quiet verification", err)
	}

	sendDataPacket(t, device, destination, 2, 104, []byte("efgh"))
	waitContext, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	_, err = receiver.Wait(waitContext)
	if err == nil || !strings.Contains(err.Error(), "exceeds maximum output size") {
		t.Fatalf("overlong finite stream error = %v", err)
	}
}

func TestReceiverRejectsInvalidExpectedOutputSize(t *testing.T) {
	for _, expected := range []int64{-1, 9} {
		config := DefaultReceiverConfig()
		config.DataBindAddress = net.IPv4(127, 0, 0, 1)
		config.DataBindPort = 0
		config.DeviceIP = net.IPv4(127, 0, 0, 2)
		config.MaxOutputBytes = 8
		config.ExpectedOutputBytes = expected
		if _, err := NewReceiver(config); err == nil {
			t.Fatalf("NewReceiver accepted expected output size %d with maximum 8", expected)
		}
	}

	config := DefaultReceiverConfig()
	config.DataBindAddress = net.IPv4(127, 0, 0, 1)
	config.DataBindPort = 0
	config.DeviceIP = net.IPv4(127, 0, 0, 2)
	config.MaxOutputBytes = 8
	config.ExpectedOutputBytes = 6
	receiver, err := NewReceiver(config)
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	if receiver.config.MaxOutputBytes != config.ExpectedOutputBytes {
		t.Fatalf(
			"effective maximum output size = %d, want exact target %d",
			receiver.config.MaxOutputBytes,
			config.ExpectedOutputBytes,
		)
	}

	invalidCadence := DefaultReceiverConfig()
	invalidCadence.DataBindAddress = net.IPv4(127, 0, 0, 1)
	invalidCadence.DataBindPort = 0
	invalidCadence.DeviceIP = net.IPv4(127, 0, 0, 2)
	invalidCadence.MaxOutputBytes = 8
	invalidCadence.ExpectedOutputBytes = 8
	invalidCadence.CadenceFrameBytes = 4
	if _, err := NewReceiver(invalidCadence); err == nil {
		t.Fatal("NewReceiver accepted cadence bytes without a cadence period")
	}
	invalidCadence.CadenceFramePeriod = time.Millisecond
	invalidCadence.ExpectedOutputBytes = 0
	if _, err := NewReceiver(invalidCadence); err == nil {
		t.Fatal("NewReceiver accepted cadence validation without a finite target")
	}
}

func TestReceiverRejectsSparseOffsetBeforeLargeWrite(t *testing.T) {
	config := DefaultReceiverConfig()
	config.DataBindAddress = net.IPv4(127, 0, 0, 1)
	config.DataBindPort = 0
	config.DeviceIP = net.IPv4(127, 0, 0, 2)
	config.FirstPacketTimeout = time.Second
	config.IdleTimeout = 20 * time.Millisecond
	config.MaxOutputBytes = 8
	receiver, err := NewReceiver(config)
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	output, err := os.CreateTemp(t.TempDir(), "bounded-*.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	if err := receiver.Start(context.Background(), output); err != nil {
		t.Fatal(err)
	}
	device := listenUDP(t, config.DeviceIP)
	defer device.Close()
	sendDataPacket(t, device, receiver.LocalEndpoint(), 1, 100, []byte{1, 2, 3, 4})
	sendDataPacket(t, device, receiver.LocalEndpoint(), 2, 1<<40, []byte{5, 6, 7, 8})
	waitContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stats, err := receiver.Wait(waitContext)
	if err == nil || !strings.Contains(err.Error(), "exceeds maximum output size") {
		t.Fatalf("Wait = (%+v, %v), want maximum-size failure", stats, err)
	}
	info, statErr := output.Stat()
	if statErr != nil {
		t.Fatal(statErr)
	}
	if info.Size() > 4 {
		t.Fatalf("sparse file grew to %d bytes before rejecting the offset", info.Size())
	}
}

func TestReceiverRejectsOverlappingPayload(t *testing.T) {
	config := DefaultReceiverConfig()
	config.DataBindAddress = net.IPv4(127, 0, 0, 1)
	config.DataBindPort = 0
	config.DeviceIP = net.IPv4(127, 0, 0, 2)
	config.FirstPacketTimeout = time.Second
	config.IdleTimeout = 20 * time.Millisecond
	receiver, err := NewReceiver(config)
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	output, err := os.CreateTemp(t.TempDir(), "overlap-*.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	if err := receiver.Start(context.Background(), output); err != nil {
		t.Fatal(err)
	}
	device := listenUDP(t, config.DeviceIP)
	defer device.Close()
	sendDataPacket(t, device, receiver.LocalEndpoint(), 1, 100, []byte{1, 2, 3, 4})
	sendDataPacket(t, device, receiver.LocalEndpoint(), 2, 102, []byte{9, 9, 9, 9})
	waitContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stats, err := receiver.Wait(waitContext)
	if err == nil || stats.OverlappingPackets != 1 {
		t.Fatalf("Wait = (%+v, %v), want overlap failure", stats, err)
	}
}

func TestSparseRangePreflightHasFixedLimit(t *testing.T) {
	ranges := make([]byteRange, maxTrackedOutputRanges)
	for index := range ranges {
		start := int64(index * 4)
		ranges[index] = byteRange{start: start, end: start + 1}
	}
	isolated := byteRange{
		start: int64(maxTrackedOutputRanges * 4),
		end:   int64(maxTrackedOutputRanges*4 + 1),
	}
	if err := preflightRangeAddition(ranges[:len(ranges)-1], isolated); err != nil {
		t.Fatalf("range at fixed limit rejected: %v", err)
	}
	if err := preflightRangeAddition(ranges, isolated); !errors.Is(err, errSparseRangeLimit) ||
		!strings.Contains(err.Error(), "refusing packet") {
		t.Fatalf("range beyond fixed limit error = %v", err)
	}

	bridge := byteRange{start: ranges[0].end, end: ranges[1].start}
	if err := preflightRangeAddition(ranges, bridge); err != nil {
		t.Fatalf("gap-closing range at fixed limit rejected: %v", err)
	}
	if got := len(addRange(append([]byteRange(nil), ranges...), bridge)); got != maxTrackedOutputRanges-1 {
		t.Fatalf("gap-closing range count = %d, want %d", got, maxTrackedOutputRanges-1)
	}

	if err := preflightRangeAddition(ranges, ranges[0]); !errors.Is(err, errOutputRangeOverlap) {
		t.Fatalf("overlap error = %v", err)
	}
}

func TestAdjacentCoverageStaysOneRange(t *testing.T) {
	var ranges []byteRange
	for offset := int64(0); offset < 10_000; offset++ {
		ranges = addRange(ranges, byteRange{start: offset, end: offset + 1})
	}
	if len(ranges) != 1 || ranges[0] != (byteRange{start: 0, end: 10_000}) {
		t.Fatalf("adjacent coverage = %#v", ranges)
	}
}

func sendDataPacket(t *testing.T, socket *net.UDPConn, destination *net.UDPAddr, sequence uint32, offset uint64, payload []byte) {
	t.Helper()
	if offset > 0xFFFFFFFFFFFF {
		t.Fatalf("test offset exceeds u48: %d", offset)
	}
	datagram := make([]byte, DataHeaderSize+len(payload))
	binary.LittleEndian.PutUint32(datagram[0:4], sequence)
	for index := range 6 {
		datagram[4+index] = byte(offset >> (8 * index))
	}
	copy(datagram[DataHeaderSize:], payload)
	if _, err := socket.WriteToUDP(datagram, destination); err != nil {
		t.Fatal(err)
	}
}
