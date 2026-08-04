package debugcapture

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

func TestOpenD2XXTransportInitializesAAndBThenClosesInReverse(t *testing.T) {
	backend, spi, irq, calls := newFakeD2XXBackend()
	transport, err := openD2XXTransport(context.Background(), testD2XXSelectors(), backend)
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
		"open:FTAK3Z11A", "open:FTAK3Z11B",
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

func TestOpenD2XXTransportRejectsBadSyncAndCleansUp(t *testing.T) {
	backend, _, irq, calls := newFakeD2XXBackend()
	irq.syncResponse = []byte{0xFA, 0xAA}
	_, err := openD2XXTransport(context.Background(), testD2XXSelectors(), backend)
	if err == nil || !strings.Contains(err.Error(), "unexpected MPSSE sync response") {
		t.Fatalf("open error = %v", err)
	}
	wantSuffix := []string{"B:bitmode:00/00", "B:close", "A:bitmode:00/00", "A:close", "library:close"}
	got := (*calls)[len(*calls)-len(wantSuffix):]
	if strings.Join(got, "\n") != strings.Join(wantSuffix, "\n") {
		t.Fatalf("cleanup calls = %v", got)
	}
}

func TestOpenD2XXTransportBoundsSyncWaitAndCleansUp(t *testing.T) {
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
	_, err := openD2XXTransport(ctx, testD2XXSelectors(), backend)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("open error = %v", err)
	}
	assertFakeCleanupSuffix(t, calls)
}

func TestOpenD2XXTransportRejectsShortIOAndCleansUp(t *testing.T) {
	t.Run("write", func(t *testing.T) {
		backend, spi, _, calls := newFakeD2XXBackend()
		command := spiMPSSEInitCommand()
		spi.shortWriteOn = command[:]
		_, err := openD2XXTransport(context.Background(), testD2XXSelectors(), backend)
		if !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("open error = %v", err)
		}
		assertFakeCleanupSuffix(t, calls)
	})

	t.Run("read", func(t *testing.T) {
		backend, _, irq, calls := newFakeD2XXBackend()
		irq.readLimit = 1
		_, err := openD2XXTransport(context.Background(), testD2XXSelectors(), backend)
		if err == nil || !strings.Contains(err.Error(), "short MPSSE read") {
			t.Fatalf("open error = %v", err)
		}
		assertFakeCleanupSuffix(t, calls)
	})
}

func TestOpenD2XXTransportClosesAWhenBOpenFails(t *testing.T) {
	backend, _, _, calls := newFakeD2XXBackend()
	backend.open = func(selector d2xx.Selector) (mpsseDevice, error) {
		*calls = append(*calls, "open:"+selector.Value)
		if strings.HasSuffix(selector.Value, "B") {
			return nil, errors.New("B unavailable")
		}
		return &fakeMPSSEDevice{name: "A", calls: calls}, nil
	}
	_, err := openD2XXTransport(context.Background(), testD2XXSelectors(), backend)
	if err == nil || !strings.Contains(err.Error(), "B unavailable") {
		t.Fatalf("open error = %v", err)
	}
	want := []string{"open:FTAK3Z11A", "open:FTAK3Z11B", "A:close", "library:close"}
	if strings.Join(*calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls = %v", *calls)
	}
}

func TestD2XXSelectorsFailBeforeOpen(t *testing.T) {
	tests := []D2XXSelectors{
		{
			SPI: d2xx.Selector{By: d2xx.SelectBySerialNumber, Value: "same"},
			IRQ: d2xx.Selector{By: d2xx.SelectBySerialNumber, Value: "same"},
		},
		{
			SPI: d2xx.Selector{By: d2xx.SelectBySerialNumber, Value: "BASEB"},
			IRQ: d2xx.Selector{By: d2xx.SelectBySerialNumber, Value: "BASEA"},
		},
		{
			SPI: d2xx.Selector{By: d2xx.SelectBySerialNumber, Value: "BASEC"},
			IRQ: d2xx.Selector{By: d2xx.SelectBySerialNumber, Value: "BASED"},
		},
		{
			SPI: d2xx.Selector{By: d2xx.SelectBySerialNumber, Value: "ONEA"},
			IRQ: d2xx.Selector{By: d2xx.SelectBySerialNumber, Value: "TWOB"},
		},
		{
			SPI: d2xx.Selector{By: d2xx.SelectBySerialNumber, Value: "BASEA"},
			IRQ: d2xx.Selector{By: d2xx.SelectByDescription, Value: "BASE B"},
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
		if _, err := openD2XXTransport(context.Background(), selectors, backend); err == nil {
			t.Fatalf("selectors %+v were accepted", selectors)
		}
		if strings.Join(calls, ",") != "close" {
			t.Fatalf("selectors %+v calls = %v", selectors, calls)
		}
	}
}

func TestD2XXSelectorsAcceptDescriptionPair(t *testing.T) {
	selectors := D2XXSelectors{
		SPI: d2xx.Selector{By: d2xx.SelectByDescription, Value: "AR-DevPack-EVM-012 A"},
		IRQ: d2xx.Selector{By: d2xx.SelectByDescription, Value: "AR-DevPack-EVM-012 B"},
	}
	if err := selectors.validate(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenD2XXTransportChecksCanceledContextBeforeOpen(t *testing.T) {
	backend, _, _, calls := newFakeD2XXBackend()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := openD2XXTransport(ctx, testD2XXSelectors(), backend)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("open error = %v", err)
	}
	if strings.Join(*calls, ",") != "library:close" {
		t.Fatalf("calls = %v", *calls)
	}
}

func TestOpenD2XXTransportChecksCancellationBeforeOpeningB(t *testing.T) {
	backend, spi, _, calls := newFakeD2XXBackend()
	ctx, cancel := context.WithCancel(context.Background())
	backend.open = func(selector d2xx.Selector) (mpsseDevice, error) {
		*calls = append(*calls, "open:"+selector.Value)
		cancel()
		return spi, nil
	}
	_, err := openD2XXTransport(ctx, testD2XXSelectors(), backend)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("open error = %v", err)
	}
	want := []string{"open:FTAK3Z11A", "A:close", "library:close"}
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

func testD2XXSelectors() D2XXSelectors {
	return D2XXSelectors{
		SPI: d2xx.Selector{By: d2xx.SelectBySerialNumber, Value: "FTAK3Z11A"},
		IRQ: d2xx.Selector{By: d2xx.SelectBySerialNumber, Value: "FTAK3Z11B"},
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
			*calls = append(*calls, "open:"+selector.Value)
			if strings.HasSuffix(selector.Value, "A") {
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
	zeroRead     bool
}

func (device *fakeMPSSEDevice) Read(buffer []byte) (int, error) {
	if device.zeroRead {
		*device.calls = append(*device.calls, device.name+":read:0")
		return 0, nil
	}
	if device.readLimit > 0 && len(buffer) > device.readLimit {
		buffer = buffer[:device.readLimit]
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
