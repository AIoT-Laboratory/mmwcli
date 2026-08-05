package debugcapture

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"mmwcli/internal/d2xx"
)

func TestDeriveBoardControlSelectors(t *testing.T) {
	tests := []struct {
		name      string
		selectors D2XXSelectors
		want      boardControlSelectors
	}{
		{
			name: "serial",
			selectors: D2XXSelectors{
				SPI: d2xx.Selector{By: d2xx.SelectBySerialNumber, Value: "FT7LVC9AA"},
				IRQ: d2xx.Selector{By: d2xx.SelectBySerialNumber, Value: "FT7LVC9AB"},
			},
			want: boardControlSelectors{
				reset: d2xx.Selector{By: d2xx.SelectBySerialNumber, Value: "FT7LVC9AC"},
				gpio:  d2xx.Selector{By: d2xx.SelectBySerialNumber, Value: "FT7LVC9AD"},
			},
		},
		{
			name: "description",
			selectors: D2XXSelectors{
				SPI: d2xx.Selector{By: d2xx.SelectByDescription, Value: "AR-DevPack-EVM-012 A"},
				IRQ: d2xx.Selector{By: d2xx.SelectByDescription, Value: "AR-DevPack-EVM-012 B"},
			},
			want: boardControlSelectors{
				reset: d2xx.Selector{By: d2xx.SelectByDescription, Value: "AR-DevPack-EVM-012 C"},
				gpio:  d2xx.Selector{By: d2xx.SelectByDescription, Value: "AR-DevPack-EVM-012 D"},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := deriveBoardControlSelectors(test.selectors)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("selectors = %+v, want %+v", got, test.want)
			}
		})
	}
	if _, err := deriveBoardControlSelectors(D2XXSelectors{}); err == nil {
		t.Fatal("invalid A/B selectors were accepted")
	}
}

func TestPrepareSOP2TargetUsesTIBitBangSequence(t *testing.T) {
	fixture := newSOP2Fixture()
	if err := prepareSOP2TargetWithBackend(context.Background(), fixture.selectors, fixture.backend()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"open:D:AR-DevPack-EVM-012 D",
		"D:set-bit-mode:00:00",
		"D:set-timeouts:500:100",
		"D:set-usb:4096:4096",
		"D:set-latency:1",
		"D:set-bit-mode:ff:01",
		"D:set-baud:115200",
		"D:get-bit-mode:e7",
		"D:write:fb",
		"wait:300ms",
		"open:C:AR-DevPack-EVM-012 C",
		"C:set-bit-mode:00:00",
		"C:set-timeouts:500:100",
		"C:set-usb:4096:4096",
		"C:set-latency:1",
		"C:set-bit-mode:f9:01",
		"C:set-baud:115200",
		"wait:500ms",
		"C:get-bit-mode:ff",
		"C:write:bf",
		"wait:2ms",
		"wait:500ms",
		"C:get-bit-mode:3f",
		"C:write:7f",
		"wait:2ms",
		"wait:500ms",
		"C:set-bit-mode:00:00",
		"C:close",
		"D:set-bit-mode:00:00",
		"D:close",
		"library-close",
	}
	if !reflect.DeepEqual(fixture.events, want) {
		t.Fatalf("events:\n%s\nwant:\n%s", strings.Join(fixture.events, "\n"), strings.Join(want, "\n"))
	}
}

func TestPrepareSOP2TargetNeverRetriesStateWrites(t *testing.T) {
	tests := []struct {
		name       string
		failEvent  string
		shortWrite bool
		wantD      int
		wantC      int
		wantCOpens int
		wantWaits  []string
		forbid     []string
	}{
		{name: "SOP write", failEvent: "D:write:fb", wantD: 1},
		{
			name: "NRST low short write", failEvent: "C:write:bf", shortWrite: true,
			wantD: 1, wantC: 1, wantCOpens: 1, wantWaits: []string{"wait:300ms", "wait:500ms"},
			forbid: []string{"C:get-bit-mode:3f", "C:write:7f"},
		},
		{
			name: "NRST high write", failEvent: "C:write:7f",
			wantD: 1, wantC: 2, wantCOpens: 1,
			wantWaits: []string{"wait:300ms", "wait:500ms", "wait:2ms", "wait:500ms"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSOP2Fixture()
			fixture.failEvent = test.failEvent
			fixture.shortWrite = test.shortWrite
			err := prepareSOP2TargetWithBackend(context.Background(), fixture.selectors, fixture.backend())
			var stateErr *sop2ResetStateError
			if !errors.As(err, &stateErr) {
				t.Fatalf("error = %v, want unknown target state", err)
			}
			if test.shortWrite && !errors.Is(err, io.ErrShortWrite) {
				t.Fatalf("short-write error = %v", err)
			}
			if !test.shortWrite && !errors.Is(err, fixture.failure) {
				t.Fatalf("write error = %v", err)
			}
			if got := countEventPrefix(fixture.events, "D:write:"); got != test.wantD {
				t.Fatalf("D writes = %d, want %d; events=%v", got, test.wantD, fixture.events)
			}
			if got := countEventPrefix(fixture.events, "C:write:"); got != test.wantC {
				t.Fatalf("C writes = %d, want %d; events=%v", got, test.wantC, fixture.events)
			}
			if got := countEventPrefix(fixture.events, "open:C:"); got != test.wantCOpens {
				t.Fatalf("C opens = %d, want %d; events=%v", got, test.wantCOpens, fixture.events)
			}
			if got := eventsWithPrefix(fixture.events, "wait:"); !reflect.DeepEqual(got, test.wantWaits) {
				t.Fatalf("waits = %v, want %v; events=%v", got, test.wantWaits, fixture.events)
			}
			for _, forbidden := range test.forbid {
				if slicesContains(fixture.events, forbidden) {
					t.Fatalf("forbidden event %q occurred: %v", forbidden, fixture.events)
				}
			}
			assertSOP2CleanupOrder(t, fixture.events)
		})
	}
}

func TestPrepareSOP2TargetFailsFastOnInitializationAndReads(t *testing.T) {
	tests := []struct {
		name       string
		failEvent  string
		wantCOpens int
		wantD      int
		wantC      int
	}{
		{name: "D initialization", failEvent: "D:set-bit-mode:ff:01"},
		{name: "D pin read", failEvent: "D:get-bit-mode:e7"},
		{name: "C initialization", failEvent: "C:set-bit-mode:f9:01", wantCOpens: 1, wantD: 1},
		{name: "C low pin read", failEvent: "C:get-bit-mode:ff", wantCOpens: 1, wantD: 1},
		{name: "C high pin read", failEvent: "C:get-bit-mode:3f", wantCOpens: 1, wantD: 1, wantC: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSOP2Fixture()
			fixture.failEvent = test.failEvent
			err := prepareSOP2TargetWithBackend(context.Background(), fixture.selectors, fixture.backend())
			var stateErr *sop2ResetStateError
			if !errors.As(err, &stateErr) || !errors.Is(err, fixture.failure) {
				t.Fatalf("error = %v", err)
			}
			if got := countEventPrefix(fixture.events, "open:C:"); got != test.wantCOpens {
				t.Fatalf("C opens = %d, want %d; events=%v", got, test.wantCOpens, fixture.events)
			}
			if got := countEventPrefix(fixture.events, "D:write:"); got != test.wantD {
				t.Fatalf("D writes = %d, want %d; events=%v", got, test.wantD, fixture.events)
			}
			if got := countEventPrefix(fixture.events, "C:write:"); got != test.wantC {
				t.Fatalf("C writes = %d, want %d; events=%v", got, test.wantC, fixture.events)
			}
			assertSOP2CleanupOrder(t, fixture.events)
		})
	}
}

func TestPrepareSOP2TargetStopsBeforeResetOnCanceledLatchWait(t *testing.T) {
	fixture := newSOP2Fixture()
	ctx, cancel := context.WithCancel(context.Background())
	backend := fixture.backend()
	backend.wait = func(waitCtx context.Context, duration time.Duration) error {
		fixture.events = append(fixture.events, "wait:"+duration.String())
		cancel()
		<-waitCtx.Done()
		return waitCtx.Err()
	}
	err := prepareSOP2TargetWithBackend(ctx, fixture.selectors, backend)
	var stateErr *sop2ResetStateError
	if !errors.As(err, &stateErr) || !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if countEventPrefix(fixture.events, "open:C:") != 0 {
		t.Fatalf("reset interface opened after canceled latch wait: %v", fixture.events)
	}
	assertSOP2CleanupOrder(t, fixture.events)
}

func TestPrepareSOP2TargetCancellationNeverContinuesResetSequence(t *testing.T) {
	fullWaits := []string{
		"wait:300ms", "wait:500ms", "wait:2ms",
		"wait:500ms", "wait:2ms", "wait:500ms",
	}
	tests := []struct {
		name       string
		cancelCall int
		wantC      int
	}{
		{name: "after NRST low device wait", cancelCall: 3, wantC: 1},
		{name: "during NRST low hold", cancelCall: 4, wantC: 1},
		{name: "after NRST high device wait", cancelCall: 5, wantC: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSOP2Fixture()
			ctx, cancel := context.WithCancel(context.Background())
			waitCalls := 0
			backend := fixture.backend()
			backend.wait = func(waitCtx context.Context, duration time.Duration) error {
				waitCalls++
				fixture.events = append(fixture.events, "wait:"+duration.String())
				if waitCalls != test.cancelCall {
					return nil
				}
				cancel()
				<-waitCtx.Done()
				return waitCtx.Err()
			}
			err := prepareSOP2TargetWithBackend(ctx, fixture.selectors, backend)
			var stateErr *sop2ResetStateError
			if !errors.As(err, &stateErr) || !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v", err)
			}
			if got := eventsWithPrefix(fixture.events, "wait:"); !reflect.DeepEqual(got, fullWaits[:test.cancelCall]) {
				t.Fatalf("waits = %v, want %v; events=%v", got, fullWaits[:test.cancelCall], fixture.events)
			}
			if got := countEventPrefix(fixture.events, "C:write:"); got != test.wantC {
				t.Fatalf("C writes = %d, want %d; events=%v", got, test.wantC, fixture.events)
			}
			assertSOP2CleanupOrder(t, fixture.events)
		})
	}
}

func TestUpdateBitBangPinsPreservesInvalidCountAndNativeError(t *testing.T) {
	fixture := newSOP2Fixture()
	device := &invalidCountBitBangDevice{fakeBitBangDevice: fixture.gpio, failure: fixture.failure}
	err := updateBitBangPins(context.Background(), device, sop2GPIOPinMask, sop2GPIODevelopment, "write SOP2")
	if !errors.Is(err, fixture.failure) || !strings.Contains(err.Error(), "invalid D2XX write count 2") {
		t.Fatalf("error = %v", err)
	}
}

func TestPrepareSOP2TargetOpenFailureHasNoUnknownTargetState(t *testing.T) {
	fixture := newSOP2Fixture()
	fixture.failEvent = "open:D:AR-DevPack-EVM-012 D"
	err := prepareSOP2TargetWithBackend(context.Background(), fixture.selectors, fixture.backend())
	var stateErr *sop2ResetStateError
	if !errors.Is(err, fixture.failure) || errors.As(err, &stateErr) {
		t.Fatalf("error = %v", err)
	}
	want := []string{"open:D:AR-DevPack-EVM-012 D", "library-close"}
	if !reflect.DeepEqual(fixture.events, want) {
		t.Fatalf("events = %v, want %v", fixture.events, want)
	}
}

func TestPrepareSOP2TargetCleansUnexpectedDeviceReturnedWithOpenError(t *testing.T) {
	fixture := newSOP2Fixture()
	backend := fixture.backend()
	backend.open = func(d2xx.Selector) (bitBangDevice, error) {
		fixture.events = append(fixture.events, "open:D-with-handle-and-error")
		return fixture.gpio, fixture.failure
	}
	err := prepareSOP2TargetWithBackend(context.Background(), fixture.selectors, backend)
	var stateErr *sop2ResetStateError
	if !errors.As(err, &stateErr) || !errors.Is(err, fixture.failure) {
		t.Fatalf("error = %v", err)
	}
	want := []string{
		"open:D-with-handle-and-error",
		"D:set-bit-mode:00:00", "D:close", "library-close",
	}
	if !reflect.DeepEqual(fixture.events, want) {
		t.Fatalf("events = %v, want %v", fixture.events, want)
	}
}

func TestPrepareSOP2TargetCleanupContinuesAfterCloseFailure(t *testing.T) {
	fixture := newSOP2Fixture()
	fixture.failEvent = "C:close"
	err := prepareSOP2TargetWithBackend(context.Background(), fixture.selectors, fixture.backend())
	var stateErr *sop2ResetStateError
	if !errors.As(err, &stateErr) || !errors.Is(err, fixture.failure) {
		t.Fatalf("error = %v", err)
	}
	assertSOP2CleanupOrder(t, fixture.events)
}

type sop2Fixture struct {
	selectors  boardControlSelectors
	events     []string
	gpio       *fakeBitBangDevice
	reset      *fakeBitBangDevice
	failure    error
	failEvent  string
	shortWrite bool
}

func newSOP2Fixture() *sop2Fixture {
	fixture := &sop2Fixture{
		selectors: boardControlSelectors{
			gpio:  d2xx.Selector{By: d2xx.SelectByDescription, Value: "AR-DevPack-EVM-012 D"},
			reset: d2xx.Selector{By: d2xx.SelectByDescription, Value: "AR-DevPack-EVM-012 C"},
		},
		failure: errors.New("injected board-control failure"),
	}
	fixture.gpio = &fakeBitBangDevice{name: "D", fixture: fixture, readModes: []byte{0xe7}}
	fixture.reset = &fakeBitBangDevice{name: "C", fixture: fixture, readModes: []byte{0xff, 0x3f}}
	return fixture
}

func (fixture *sop2Fixture) backend() boardControlBackend {
	return boardControlBackend{
		open: func(selector d2xx.Selector) (bitBangDevice, error) {
			name := "?"
			device := fixture.gpio
			if selector == fixture.selectors.reset {
				name, device = "C", fixture.reset
			} else if selector == fixture.selectors.gpio {
				name = "D"
			}
			event := fmt.Sprintf("open:%s:%s", name, selector.Value)
			if err := fixture.record(event); err != nil {
				return nil, err
			}
			if name == "?" {
				return nil, errors.New("unexpected selector")
			}
			return device, nil
		},
		close: func() error { return fixture.record("library-close") },
		wait: func(_ context.Context, duration time.Duration) error {
			return fixture.record("wait:" + duration.String())
		},
	}
}

func (fixture *sop2Fixture) record(event string) error {
	fixture.events = append(fixture.events, event)
	if event == fixture.failEvent && !fixture.shortWrite {
		return fixture.failure
	}
	return nil
}

type fakeBitBangDevice struct {
	name      string
	fixture   *sop2Fixture
	mode      byte
	readModes []byte
}

type invalidCountBitBangDevice struct {
	*fakeBitBangDevice
	failure error
}

func (device *invalidCountBitBangDevice) Write([]byte) (int, error) {
	return 2, device.failure
}

func (device *fakeBitBangDevice) Write(buffer []byte) (int, error) {
	event := fmt.Sprintf("%s:write:%x", device.name, buffer)
	device.fixture.events = append(device.fixture.events, event)
	if event == device.fixture.failEvent {
		if device.fixture.shortWrite {
			return 0, nil
		}
		return len(buffer), device.fixture.failure
	}
	if len(buffer) == 1 {
		device.mode = buffer[0]
	}
	return len(buffer), nil
}

func (device *fakeBitBangDevice) Close() error {
	return device.fixture.record(device.name + ":close")
}

func (device *fakeBitBangDevice) SetTimeouts(read, write uint32) error {
	return device.fixture.record(fmt.Sprintf("%s:set-timeouts:%d:%d", device.name, read, write))
}

func (device *fakeBitBangDevice) SetLatencyTimer(milliseconds byte) error {
	return device.fixture.record(fmt.Sprintf("%s:set-latency:%d", device.name, milliseconds))
}

func (device *fakeBitBangDevice) SetBitMode(mask, mode byte) error {
	return device.fixture.record(fmt.Sprintf("%s:set-bit-mode:%02x:%02x", device.name, mask, mode))
}

func (device *fakeBitBangDevice) GetBitMode() (byte, error) {
	if len(device.readModes) != 0 {
		device.mode = device.readModes[0]
		device.readModes = device.readModes[1:]
	}
	event := fmt.Sprintf("%s:get-bit-mode:%02x", device.name, device.mode)
	return device.mode, device.fixture.record(event)
}

func (device *fakeBitBangDevice) SetBaudRate(baud uint32) error {
	return device.fixture.record(fmt.Sprintf("%s:set-baud:%d", device.name, baud))
}

func (device *fakeBitBangDevice) SetUSBParameters(input, output uint32) error {
	return device.fixture.record(fmt.Sprintf("%s:set-usb:%d:%d", device.name, input, output))
}

func countEventPrefix(events []string, prefix string) int {
	count := 0
	for _, event := range events {
		if strings.HasPrefix(event, prefix) {
			count++
		}
	}
	return count
}

func eventsWithPrefix(events []string, prefix string) []string {
	var matched []string
	for _, event := range events {
		if strings.HasPrefix(event, prefix) {
			matched = append(matched, event)
		}
	}
	return matched
}

func slicesContains(events []string, want string) bool {
	for _, event := range events {
		if event == want {
			return true
		}
	}
	return false
}

func assertSOP2CleanupOrder(t *testing.T, events []string) {
	t.Helper()
	wantSuffix := []string{"D:set-bit-mode:00:00", "D:close", "library-close"}
	if countEventPrefix(events, "open:C:") != 0 {
		wantSuffix = []string{
			"C:set-bit-mode:00:00", "C:close",
			"D:set-bit-mode:00:00", "D:close",
			"library-close",
		}
	}
	if len(events) < len(wantSuffix) || !reflect.DeepEqual(events[len(events)-len(wantSuffix):], wantSuffix) {
		t.Fatalf("cleanup suffix = %v, want %v; events=%v", events[max(0, len(events)-len(wantSuffix)):], wantSuffix, events)
	}
}
