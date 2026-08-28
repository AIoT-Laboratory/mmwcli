package iwr6843

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"mmwcli/internal/d2xx"
)

func TestOpenTransportInitializesAAndBThenClosesInReverse(t *testing.T) {
	backend, spi, irq, calls := newFakeD2XXBackend()
	transport, err := openTransport(context.Background(), testSelectors(), backend)
	if err != nil {
		t.Fatal(err)
	}
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"open:AR-DevPack-EVM-012 A", "open:AR-DevPack-EVM-012 B",
		"A:bitmode:00/00", "A:timeouts:500/500", "A:usb:4096/4096",
		"A:latency:16", "A:bitmode:4B/02", "A:write:8A978D86020080C84B",
		"B:timeouts:500/100", "B:chars:00/false/00/false", "B:usb:4096/4096",
		"B:latency:1", "B:bitmode:5B/02", "wait:50ms", "B:write:AB", "B:read:2",
		"B:write:8A978C", "B:write:80135B864A00", "wait:20ms", "B:write:85",
		"B:bitmode:00/00", "B:close", "A:bitmode:00/00", "A:close", "library:close",
	}
	if strings.Join(*calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(*calls, "\n"), strings.Join(want, "\n"))
	}
	if spi.closeCalls != 1 || irq.closeCalls != 1 {
		t.Fatalf("close calls: SPI=%d IRQ=%d", spi.closeCalls, irq.closeCalls)
	}
}

func TestOpenTransportRejectsBadSyncAndCleansUp(t *testing.T) {
	backend, _, irq, calls := newFakeD2XXBackend()
	irq.syncResponse = []byte{0xFA, 0xAA}
	_, err := openTransport(context.Background(), testSelectors(), backend)
	if err == nil || !strings.Contains(err.Error(), "unexpected MPSSE sync response") {
		t.Fatalf("open error = %v", err)
	}
	wantSuffix := []string{"B:bitmode:00/00", "B:close", "A:bitmode:00/00", "A:close", "library:close"}
	got := (*calls)[len(*calls)-len(wantSuffix):]
	if strings.Join(got, "\n") != strings.Join(wantSuffix, "\n") {
		t.Fatalf("cleanup calls = %v", got)
	}
}

func TestOpenTransportBoundsSyncWaitAndCleansUp(t *testing.T) {
	backend, _, irq, calls := newFakeD2XXBackend()
	irq.syncResponse = nil
	ctx, cancel := context.WithCancel(context.Background())
	backend.wait = func(waitContext context.Context, duration time.Duration) error {
		*calls = append(*calls, "wait:"+duration.String())
		if duration == nativePollInterval {
			cancel()
		}
		return checkContext(waitContext)
	}
	_, err := openTransport(ctx, testSelectors(), backend)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("open error = %v", err)
	}
	assertFakeCleanupSuffix(t, calls)
}

func TestOpenTransportRejectsShortIOAndCleansUp(t *testing.T) {
	t.Run("write", func(t *testing.T) {
		backend, spi, _, calls := newFakeD2XXBackend()
		command := spiMPSSEInitCommand()
		spi.shortWriteOn = command[:]
		_, err := openTransport(context.Background(), testSelectors(), backend)
		if !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("open error = %v", err)
		}
		assertFakeCleanupSuffix(t, calls)
	})

	t.Run("read", func(t *testing.T) {
		backend, _, irq, calls := newFakeD2XXBackend()
		irq.readLimit = 1
		_, err := openTransport(context.Background(), testSelectors(), backend)
		if err == nil || !strings.Contains(err.Error(), "short MPSSE read") {
			t.Fatalf("open error = %v", err)
		}
		assertFakeCleanupSuffix(t, calls)
	})
}

func TestOpenTransportClosesAWhenBOpenFails(t *testing.T) {
	backend, _, _, calls := newFakeD2XXBackend()
	backend.open = func(selector d2xx.Selector) (mpsseDevice, error) {
		*calls = append(*calls, "open:"+selector.Description)
		if strings.HasSuffix(selector.Description, "B") {
			return nil, errors.New("B unavailable")
		}
		return &fakeMPSSEDevice{name: "A", calls: calls}, nil
	}
	_, err := openTransport(context.Background(), testSelectors(), backend)
	if err == nil || !strings.Contains(err.Error(), "B unavailable") {
		t.Fatalf("open error = %v", err)
	}
	want := []string{"open:AR-DevPack-EVM-012 A", "open:AR-DevPack-EVM-012 B", "A:close", "library:close"}
	if strings.Join(*calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls = %v", *calls)
	}
}

func TestSelectorsFailBeforeOpen(t *testing.T) {
	tests := []Selectors{
		{
			SPI: d2xx.Selector{Description: "same"},
			IRQ: d2xx.Selector{Description: "same"},
		},
		{
			SPI: d2xx.Selector{Description: "BASE B"},
			IRQ: d2xx.Selector{Description: "BASE A"},
		},
		{
			SPI: d2xx.Selector{Description: "BASE C"},
			IRQ: d2xx.Selector{Description: "BASE D"},
		},
		{
			SPI: d2xx.Selector{Description: "ONE A"},
			IRQ: d2xx.Selector{Description: "TWO B"},
		},
	}
	for _, selectors := range tests {
		calls := []string{}
		backend := d2xxBackend{
			open: func(d2xx.Selector) (mpsseDevice, error) {
				calls = append(calls, "open")
				return nil, nil
			},
			close: func() error {
				calls = append(calls, "close")
				return nil
			},
			wait: fakeMPSSEWait(&calls),
		}
		if _, err := openTransport(context.Background(), selectors, backend); err == nil {
			t.Fatalf("selectors %+v were accepted", selectors)
		}
		if strings.Join(calls, ",") != "close" {
			t.Fatalf("selectors %+v calls = %v", selectors, calls)
		}
	}
}

func TestSelectorsAcceptDescriptionPair(t *testing.T) {
	selectors := Selectors{
		SPI: d2xx.Selector{Description: "AR-DevPack-EVM-012 A"},
		IRQ: d2xx.Selector{Description: "AR-DevPack-EVM-012 B"},
	}
	if err := selectors.validate(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenTransportChecksCanceledContextBeforeOpen(t *testing.T) {
	backend, _, _, calls := newFakeD2XXBackend()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := openTransport(ctx, testSelectors(), backend)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("open error = %v", err)
	}
	if strings.Join(*calls, ",") != "library:close" {
		t.Fatalf("calls = %v", *calls)
	}
}

func TestOpenTransportChecksCancellationBeforeOpeningB(t *testing.T) {
	backend, spi, _, calls := newFakeD2XXBackend()
	ctx, cancel := context.WithCancel(context.Background())
	backend.open = func(selector d2xx.Selector) (mpsseDevice, error) {
		*calls = append(*calls, "open:"+selector.Description)
		cancel()
		return spi, nil
	}
	_, err := openTransport(ctx, testSelectors(), backend)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("open error = %v", err)
	}
	want := []string{"open:AR-DevPack-EVM-012 A", "A:close", "library:close"}
	if strings.Join(*calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls = %v", *calls)
	}
}

func TestDrainMPSSEIsBoundedAndNeverReadsZeroBytes(t *testing.T) {
	t.Run("4096 byte chunk", func(t *testing.T) {
		calls := &[]string{}
		device := &fakeMPSSEDevice{name: "A", calls: calls, rx: make([]byte, 4096)}
		if err := drainMPSSE(context.Background(), device); err != nil {
			t.Fatal(err)
		}
		if strings.Join(*calls, ",") != "A:read:4096" {
			t.Fatalf("calls = %v", *calls)
		}
	})

	t.Run("zero progress", func(t *testing.T) {
		calls := &[]string{}
		device := &fakeMPSSEDevice{name: "A", calls: calls, rx: []byte{1}, zeroRead: true}
		err := drainMPSSE(context.Background(), device)
		if err == nil || !strings.Contains(err.Error(), "no progress") {
			t.Fatalf("drain error = %v", err)
		}
	})

	t.Run("total limit", func(t *testing.T) {
		calls := &[]string{}
		device := &fakeMPSSEDevice{name: "A", calls: calls, rx: make([]byte, nativeDrainLimit+1)}
		err := drainMPSSE(context.Background(), device)
		if err == nil || !strings.Contains(err.Error(), "exceeded") {
			t.Fatalf("drain error = %v", err)
		}
	})
}

func TestNativeDeadlineCapsLongParent(t *testing.T) {
	parent, parentCancel := context.WithTimeout(context.Background(), time.Hour)
	defer parentCancel()
	ctx, cancel := withNativeDeadline(parent)
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("native context has no deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > nativeOperationTimeout {
		t.Fatalf("native deadline remaining = %s", remaining)
	}
}

func TestTransportSPIAndIRQTransactions(t *testing.T) {
	backend, spi, irq, calls := newFakeD2XXBackend()
	transport, err := openTransport(context.Background(), testSelectors(), backend)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()

	*calls = nil
	if err := transport.SPIWrite(context.Background(), []byte{0x34, 0x12, 0xCD, 0xAB}); err != nil {
		t.Fatal(err)
	}
	spi.spiResponses = [][]byte{{0x12, 0x34}, {0xAB, 0xCD}}
	buffer := make([]byte, 4)
	if err := transport.SPIRead(context.Background(), buffer); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buffer, []byte{0x34, 0x12, 0xCD, 0xAB}) {
		t.Fatalf("SPI read = % X", buffer)
	}
	irq.irqSamples = []byte{0x00, 0x20}
	if err := transport.WaitIRQ(context.Background(), true); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"A:write:80C04B110100123480C84B87",
		"A:write:80C04B110100ABCD80C84B87",
		"A:write:80C24B20010080C84B87", "A:read:2",
		"A:write:80C24B20010080C84B87", "A:read:2",
		"B:write:81", "B:read:1", "wait:1ms",
		"B:write:81", "B:read:1",
	}
	if strings.Join(*calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(*calls, "\n"), strings.Join(want, "\n"))
	}
}

func TestTransportRejectsInvalidSPITransferBeforeIO(t *testing.T) {
	backend, _, _, calls := newFakeD2XXBackend()
	transport, err := openTransport(context.Background(), testSelectors(), backend)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	*calls = nil
	for _, buffer := range [][]byte{nil, {1}} {
		if err := transport.SPIWrite(context.Background(), buffer); err == nil {
			t.Fatalf("SPIWrite(%v) succeeded", buffer)
		}
		if err := transport.SPIRead(context.Background(), buffer); err == nil {
			t.Fatalf("SPIRead(%v) succeeded", buffer)
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("invalid transfer reached device: %v", *calls)
	}
}

func TestTransportFailsClosedWithoutRetry(t *testing.T) {
	t.Run("stale queue", func(t *testing.T) {
		backend, spi, _, calls := newFakeD2XXBackend()
		transport, err := openTransport(context.Background(), testSelectors(), backend)
		if err != nil {
			t.Fatal(err)
		}
		defer transport.Close()
		*calls = nil
		spi.rx = []byte{0xFA}
		err = transport.SPIWrite(context.Background(), []byte{0x34, 0x12})
		if err == nil || !strings.Contains(err.Error(), "not empty") {
			t.Fatalf("SPIWrite error = %v", err)
		}
		if len(*calls) != 0 {
			t.Fatalf("stale queue was drained or written through: %v", *calls)
		}
	})

	t.Run("short write", func(t *testing.T) {
		backend, spi, _, calls := newFakeD2XXBackend()
		transport, err := openTransport(context.Background(), testSelectors(), backend)
		if err != nil {
			t.Fatal(err)
		}
		defer transport.Close()
		*calls = nil
		command := spiMPSSEWriteWord(0x1234)
		spi.shortWriteOn = command[:]
		if err := transport.SPIWrite(context.Background(), []byte{0x34, 0x12}); !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("SPIWrite error = %v", err)
		}
		if len(*calls) != 1 {
			t.Fatalf("short write was retried: %v", *calls)
		}
	})
}

func TestTransportStopsOnCancellationAndClosedState(t *testing.T) {
	var nilTransport *Transport
	if err := nilTransport.WaitIRQ(context.Background(), true); !errors.Is(err, ErrTransportClosed) {
		t.Fatalf("nil WaitIRQ = %v", err)
	}
	backend, _, _, calls := newFakeD2XXBackend()
	transport, err := openTransport(context.Background(), testSelectors(), backend)
	if err != nil {
		t.Fatal(err)
	}
	*calls = nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := transport.WaitIRQ(ctx, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitIRQ error = %v", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("canceled wait reached device: %v", *calls)
	}
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
	if err := transport.SPIWrite(context.Background(), []byte{0, 0}); !errors.Is(err, ErrTransportClosed) {
		t.Fatalf("SPIWrite after close = %v", err)
	}
}

func TestTransportAllowsSPIWhileWaitingForIRQAndCloseCancelsWait(t *testing.T) {
	backend, spi, irq, _ := newFakeD2XXBackend()
	transport, err := openTransport(context.Background(), testSelectors(), backend)
	if err != nil {
		t.Fatal(err)
	}
	spiCalls, irqCalls := []string{}, []string{}
	spi.calls, irq.calls = &spiCalls, &irqCalls
	irq.irqSamples = nil
	polling := make(chan struct{}, 1)
	backend.wait = func(ctx context.Context, duration time.Duration) error {
		if duration == nativePollInterval {
			select {
			case polling <- struct{}{}:
			default:
			}
			return waitContext(ctx, time.Hour)
		}
		return nil
	}
	transport.backend.wait = backend.wait

	waitResult := make(chan error, 1)
	go func() {
		waitResult <- transport.WaitIRQ(context.Background(), true)
	}()
	select {
	case <-polling:
	case <-time.After(time.Second):
		t.Fatal("WaitIRQ did not begin polling")
	}
	if err := transport.SPIWrite(context.Background(), []byte{0x34, 0x12}); err != nil {
		t.Fatalf("SPIWrite while waiting for IRQ: %v", err)
	}
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-waitResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("WaitIRQ error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel WaitIRQ")
	}
}

func TestTransportSPIReadPublishesAtomically(t *testing.T) {
	backend, spi, _, _ := newFakeD2XXBackend()
	transport, err := openTransport(context.Background(), testSelectors(), backend)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	spi.spiResponses = [][]byte{{0x12, 0x34}, {0xAB, 0xCD}}
	spi.readLimits = []int{2, 1}
	buffer := []byte{0xAA, 0xAA, 0xAA, 0xAA}
	if err := transport.SPIRead(context.Background(), buffer); err == nil {
		t.Fatal("short second word succeeded")
	}
	if !bytes.Equal(buffer, []byte{0xAA, 0xAA, 0xAA, 0xAA}) {
		t.Fatalf("partial SPI read was published: % X", buffer)
	}
}

func testSelectors() Selectors {
	return Selectors{
		SPI: d2xx.Selector{Description: "AR-DevPack-EVM-012 A"},
		IRQ: d2xx.Selector{Description: "AR-DevPack-EVM-012 B"},
	}
}

func assertFakeCleanupSuffix(t *testing.T, calls *[]string) {
	t.Helper()
	want := []string{"B:bitmode:00/00", "B:close", "A:bitmode:00/00", "A:close", "library:close"}
	if len(*calls) < len(want) {
		t.Fatalf("cleanup calls = %v", *calls)
	}
	got := (*calls)[len(*calls)-len(want):]
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("cleanup calls = %v", got)
	}
}

func newFakeD2XXBackend() (d2xxBackend, *fakeMPSSEDevice, *fakeMPSSEDevice, *[]string) {
	calls := &[]string{}
	spi := &fakeMPSSEDevice{name: "A", calls: calls}
	irq := &fakeMPSSEDevice{name: "B", calls: calls, syncResponse: []byte{0xFA, 0xAB}}
	backend := d2xxBackend{
		open: func(selector d2xx.Selector) (mpsseDevice, error) {
			*calls = append(*calls, "open:"+selector.Description)
			if strings.HasSuffix(selector.Description, "A") {
				return spi, nil
			}
			return irq, nil
		},
		close: func() error {
			*calls = append(*calls, "library:close")
			return nil
		},
		wait: fakeMPSSEWait(calls),
	}
	return backend, spi, irq, calls
}

func fakeMPSSEWait(calls *[]string) func(context.Context, time.Duration) error {
	return func(ctx context.Context, duration time.Duration) error {
		*calls = append(*calls, "wait:"+duration.String())
		return checkContext(ctx)
	}
}

type fakeMPSSEDevice struct {
	name         string
	calls        *[]string
	rx           []byte
	syncResponse []byte
	closeCalls   int
	shortWriteOn []byte
	readLimit    int
	readLimits   []int
	zeroRead     bool
	spiResponses [][]byte
	irqSamples   []byte
}

func (device *fakeMPSSEDevice) Read(buffer []byte) (int, error) {
	if device.zeroRead {
		*device.calls = append(*device.calls, device.name+":read:0")
		return 0, nil
	}
	if device.readLimit > 0 && len(buffer) > device.readLimit {
		buffer = buffer[:device.readLimit]
	}
	if len(device.readLimits) != 0 {
		limit := device.readLimits[0]
		device.readLimits = device.readLimits[1:]
		if limit > 0 && len(buffer) > limit {
			buffer = buffer[:limit]
		}
	}
	count := copy(buffer, device.rx)
	device.rx = device.rx[count:]
	*device.calls = append(*device.calls, fmt.Sprintf("%s:read:%d", device.name, count))
	return count, nil
}

func (device *fakeMPSSEDevice) Write(buffer []byte) (int, error) {
	*device.calls = append(*device.calls, fmt.Sprintf("%s:write:%X", device.name, buffer))
	if bytes.Equal(buffer, device.shortWriteOn) {
		return len(buffer) - 1, nil
	}
	if bytes.Equal(buffer, []byte{0xAB}) {
		device.rx = append(device.rx, device.syncResponse...)
	}
	spiReadCommand := spiMPSSEReadWordCommand()
	if bytes.Equal(buffer, spiReadCommand[:]) && len(device.spiResponses) != 0 {
		device.rx = append(device.rx, device.spiResponses[0]...)
		device.spiResponses = device.spiResponses[1:]
	}
	irqReadCommand := irqMPSSEReadCommand()
	if bytes.Equal(buffer, irqReadCommand[:]) && len(device.irqSamples) != 0 {
		device.rx = append(device.rx, device.irqSamples[0])
		device.irqSamples = device.irqSamples[1:]
	}
	return len(buffer), nil
}

func (device *fakeMPSSEDevice) Close() error {
	device.closeCalls++
	*device.calls = append(*device.calls, device.name+":close")
	return nil
}

func (device *fakeMPSSEDevice) QueueStatus() (uint32, error) {
	return uint32(len(device.rx)), nil
}

func (device *fakeMPSSEDevice) SetTimeouts(read, write uint32) error {
	*device.calls = append(*device.calls, fmt.Sprintf("%s:timeouts:%d/%d", device.name, read, write))
	return nil
}

func (device *fakeMPSSEDevice) SetChars(event byte, eventEnabled bool, errorChar byte, errorEnabled bool) error {
	*device.calls = append(*device.calls, fmt.Sprintf(
		"%s:chars:%02X/%t/%02X/%t",
		device.name, event, eventEnabled, errorChar, errorEnabled,
	))
	return nil
}

func (device *fakeMPSSEDevice) SetLatencyTimer(milliseconds byte) error {
	*device.calls = append(*device.calls, fmt.Sprintf("%s:latency:%d", device.name, milliseconds))
	return nil
}

func (device *fakeMPSSEDevice) SetBitMode(mask, mode byte) error {
	*device.calls = append(*device.calls, fmt.Sprintf("%s:bitmode:%02X/%02X", device.name, mask, mode))
	return nil
}

func (device *fakeMPSSEDevice) SetUSBParameters(inputSize, outputSize uint32) error {
	*device.calls = append(*device.calls, fmt.Sprintf("%s:usb:%d/%d", device.name, inputSize, outputSize))
	return nil
}
