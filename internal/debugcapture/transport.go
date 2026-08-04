package debugcapture

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"mmwcli/internal/d2xx"
)

const (
	nativeOperationTimeout = 10 * time.Second
	nativePollInterval     = time.Millisecond
	nativeDrainLimit       = uint32(64 * 1024)
	nativeDrainChunk       = uint32(4096)
)

type D2XXSelectors struct {
	SPI d2xx.Selector
	IRQ d2xx.Selector
}

type mpsseDevice interface {
	io.ReadWriteCloser
	QueueStatus() (uint32, error)
	SetTimeouts(uint32, uint32) error
	SetChars(byte, bool, byte, bool) error
	SetLatencyTimer(byte) error
	SetBitMode(byte, byte) error
	SetUSBParameters(uint32, uint32) error
}

type d2xxBackend struct {
	open  func(d2xx.Selector) (mpsseDevice, error)
	close func() error
	wait  func(context.Context, time.Duration) error
}

type D2XXTransport struct {
	mu      sync.Mutex
	spi     mpsseDevice
	irq     mpsseDevice
	backend d2xxBackend
	closed  bool
}

func OpenD2XXTransport(ctx context.Context, selectors D2XXSelectors) (*D2XXTransport, error) {
	if err := selectors.validate(); err != nil {
		return nil, err
	}
	library, err := d2xx.Load()
	if err != nil {
		return nil, err
	}
	return openD2XXTransport(ctx, selectors, d2xxBackend{
		open: func(selector d2xx.Selector) (mpsseDevice, error) {
			return library.Open(selector)
		},
		close: library.Close,
		wait:  waitContext,
	})
}

func (selectors D2XXSelectors) validate() error {
	if err := selectors.SPI.Validate(); err != nil {
		return fmt.Errorf("SPI selector: %w", err)
	}
	if err := selectors.IRQ.Validate(); err != nil {
		return fmt.Errorf("IRQ selector: %w", err)
	}
	if selectors.SPI.By != selectors.IRQ.By {
		return errors.New("D2XX SPI and IRQ selectors must use the same selection method")
	}
	var spiBase, irqBase string
	var spiOK, irqOK bool
	switch selectors.SPI.By {
	case d2xx.SelectBySerialNumber:
		spiBase, spiOK = strings.CutSuffix(selectors.SPI.Value, "A")
		irqBase, irqOK = strings.CutSuffix(selectors.IRQ.Value, "B")
	case d2xx.SelectByDescription:
		spiBase, spiOK = strings.CutSuffix(selectors.SPI.Value, " A")
		irqBase, irqOK = strings.CutSuffix(selectors.IRQ.Value, " B")
	}
	if !spiOK || !irqOK || spiBase == "" || spiBase != irqBase {
		return errors.New("D2XX selectors must identify the A (SPI) and B (IRQ) interfaces of one FTDI device")
	}
	return nil
}

func openD2XXTransport(ctx context.Context, selectors D2XXSelectors, backend d2xxBackend) (*D2XXTransport, error) {
	if err := selectors.validate(); err != nil {
		return nil, errors.Join(err, backend.close())
	}
	operationContext, cancel := withNativeDeadline(ctx)
	defer cancel()
	if err := checkContext(operationContext); err != nil {
		return nil, errors.Join(err, backend.close())
	}

	spi, err := backend.open(selectors.SPI)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("open D2XX SPI interface: %w", err), backend.close())
	}
	if err := checkContext(operationContext); err != nil {
		return nil, errors.Join(err, spi.Close(), backend.close())
	}
	irq, err := backend.open(selectors.IRQ)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("open D2XX IRQ interface: %w", err),
			spi.Close(),
			backend.close(),
		)
	}

	transport := &D2XXTransport{spi: spi, irq: irq, backend: backend}
	if err := transport.initialize(operationContext); err != nil {
		return nil, errors.Join(err, transport.Close())
	}
	return transport, nil
}

func (transport *D2XXTransport) initialize(ctx context.Context) error {
	if err := initializeSPI(ctx, transport.spi); err != nil {
		return fmt.Errorf("initialize D2XX SPI interface: %w", err)
	}
	if err := initializeIRQ(ctx, transport.irq, transport.backend.wait); err != nil {
		return fmt.Errorf("initialize D2XX IRQ interface: %w", err)
	}
	return nil
}

func initializeSPI(ctx context.Context, device mpsseDevice) error {
	if err := runNativeSteps(ctx,
		func() error { return device.SetBitMode(0, d2xx.BitModeReset) },
		func() error { return device.SetTimeouts(500, 500) },
		func() error { return device.SetUSBParameters(4096, 4096) },
		func() error { return device.SetLatencyTimer(16) },
		func() error { return device.SetBitMode(spiDirection, d2xx.BitModeMPSSE) },
	); err != nil {
		return err
	}
	command := spiMPSSEInitCommand()
	if err := writeMPSSE(ctx, device, command[:]); err != nil {
		return err
	}
	return drainMPSSE(ctx, device)
}

func initializeIRQ(ctx context.Context, device mpsseDevice, wait func(context.Context, time.Duration) error) error {
	if err := runNativeSteps(ctx,
		func() error { return device.SetTimeouts(500, 100) },
		func() error { return device.SetChars(0, false, 0, false) },
		func() error { return device.SetUSBParameters(4096, 4096) },
		func() error { return device.SetLatencyTimer(1) },
		func() error { return device.SetBitMode(irqDirection, d2xx.BitModeMPSSE) },
	); err != nil {
		return err
	}
	if err := wait(ctx, 50*time.Millisecond); err != nil {
		return err
	}
	if err := drainMPSSE(ctx, device); err != nil {
		return err
	}

	syncCommand := irqMPSSESyncCommand()
	if err := writeMPSSE(ctx, device, syncCommand[:]); err != nil {
		return err
	}
	response, err := readMPSSEExact(ctx, device, 2, wait)
	if err != nil {
		return err
	}
	if !validIRQMPSSESync(response) {
		return fmt.Errorf("unexpected MPSSE sync response: % X", response)
	}
	if err := drainMPSSE(ctx, device); err != nil {
		return err
	}

	clockCommand := irqMPSSEClockCommand()
	if err := writeMPSSE(ctx, device, clockCommand[:]); err != nil {
		return err
	}
	gpioCommand := irqMPSSEGPIOCommand()
	if err := writeMPSSE(ctx, device, gpioCommand[:]); err != nil {
		return err
	}
	if err := wait(ctx, 20*time.Millisecond); err != nil {
		return err
	}
	loopbackCommand := irqMPSSELoopbackOffCommand()
	if err := writeMPSSE(ctx, device, loopbackCommand[:]); err != nil {
		return err
	}
	return drainMPSSE(ctx, device)
}

func runNativeSteps(ctx context.Context, steps ...func() error) error {
	for _, step := range steps {
		if err := checkContext(ctx); err != nil {
			return err
		}
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

func writeMPSSE(ctx context.Context, device mpsseDevice, payload []byte) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	count, err := device.Write(payload)
	if err != nil {
		return err
	}
	if count != len(payload) {
		return io.ErrShortWrite
	}
	return nil
}

func readMPSSEExact(
	ctx context.Context,
	device mpsseDevice,
	want uint32,
	wait func(context.Context, time.Duration) error,
) ([]byte, error) {
	for {
		if err := checkContext(ctx); err != nil {
			return nil, err
		}
		queued, err := device.QueueStatus()
		if err != nil {
			return nil, err
		}
		if queued > want {
			return nil, fmt.Errorf("MPSSE receive queue has %d bytes, expected exactly %d", queued, want)
		}
		if queued == want {
			break
		}
		if err := wait(ctx, nativePollInterval); err != nil {
			return nil, err
		}
	}

	response := make([]byte, int(want))
	count, err := device.Read(response)
	if err != nil {
		return nil, err
	}
	if count != len(response) {
		return nil, fmt.Errorf("short MPSSE read: got %d bytes, want %d: %w", count, len(response), io.ErrUnexpectedEOF)
	}
	remaining, err := device.QueueStatus()
	if err != nil {
		return nil, err
	}
	if remaining != 0 {
		return nil, fmt.Errorf("MPSSE receive queue has %d unexpected trailing bytes", remaining)
	}
	return response, nil
}

func drainMPSSE(ctx context.Context, device mpsseDevice) error {
	var drained uint32
	buffer := make([]byte, nativeDrainChunk)
	for {
		if err := checkContext(ctx); err != nil {
			return err
		}
		queued, err := device.QueueStatus()
		if err != nil {
			return err
		}
		if queued == 0 {
			return nil
		}
		chunk := min(queued, nativeDrainChunk)
		if drained > nativeDrainLimit-chunk {
			return fmt.Errorf("MPSSE drain exceeded %d bytes", nativeDrainLimit)
		}
		count, err := device.Read(buffer[:chunk])
		if err != nil {
			return err
		}
		if count == 0 {
			return errors.New("MPSSE drain made no progress")
		}
		drained += uint32(count)
	}
}

func (transport *D2XXTransport) Close() error {
	if transport == nil {
		return nil
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.closed {
		return nil
	}
	transport.closed = true

	spi, irq := transport.spi, transport.irq
	transport.spi, transport.irq = nil, nil
	var result error
	if irq != nil {
		result = errors.Join(result, irq.SetBitMode(0, d2xx.BitModeReset), irq.Close())
	}
	if spi != nil {
		result = errors.Join(result, spi.SetBitMode(0, d2xx.BitModeReset), spi.Close())
	}
	if transport.backend.close != nil {
		result = errors.Join(result, transport.backend.close())
	}
	return result
}

func withNativeDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, nativeOperationTimeout)
}

func checkContext(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func waitContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
