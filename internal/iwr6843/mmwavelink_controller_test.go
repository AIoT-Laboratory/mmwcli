package iwr6843

import (
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"mmwcli/internal/d2xx"
	"mmwcli/internal/session"
)

var _ session.Radar = (*Controller)(nil)

func TestOpenUsesFixedTransportOrder(t *testing.T) {
	var calls []string
	enhanced := &fakeControllerEnhanced{calls: &calls, receipt: validFirmwareReceipt()}
	transport := &fakeControllerTransport{calls: &calls}
	link := &fakeControllerLink{}
	diagnostics := validControllerDiagnostics()

	options := validOptions(t)
	controller, err := openWithBackend(context.Background(), options, controllerBackend{
		prepareSOP2: func(context.Context, Selectors) error {
			calls = append(calls, "prepare-sop2")
			return nil
		},
		openEnhanced: func(context.Context, string) (controllerEnhancedConnection, error) {
			calls = append(calls, "open-enhanced")
			return enhanced, nil
		},
		openD2XX: func(context.Context, Selectors) (controllerTransport, error) {
			calls = append(calls, "open-d2xx")
			return transport, nil
		},
		bootstrap: func(context.Context, controllerTransport) (controllerLink, mmWaveLinkDeviceDiagnostics, error) {
			calls = append(calls, "bootstrap")
			return link, diagnostics, nil
		},
	})
	if err != nil {
		t.Fatalf("openWithBackend: %v", err)
	}
	want := []string{"prepare-sop2", "open-enhanced", "submit-firmware", "open-d2xx", "bootstrap", "close-enhanced"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("open calls = %v, want %v", calls, want)
	}

	platform, err := controller.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !strings.Contains(platform, "MSS 2.0.0.3 RF 6.2.1.5") {
		t.Fatalf("platform report = %q", platform)
	}
	if err := controller.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := controller.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if transport.closeCalls != 1 {
		t.Fatalf("D2XX close calls = %d, want 1", transport.closeCalls)
	}
}

func TestOpenStopsBeforeEnhancedAfterSOP2Failure(t *testing.T) {
	want := errors.New("injected SOP2 failure")
	tests := []struct {
		name            string
		prepareError    error
		wantTargetState bool
	}{
		{name: "before target change", prepareError: want},
		{name: "unknown target state", prepareError: &sop2ResetStateError{err: want}, wantTargetState: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := validOptions(t)
			enhancedOpens := 0
			_, err := openWithBackend(context.Background(), options, controllerBackend{
				prepareSOP2: func(context.Context, Selectors) error { return test.prepareError },
				openEnhanced: func(context.Context, string) (controllerEnhancedConnection, error) {
					enhancedOpens++
					return nil, errors.New("unexpected Enhanced open")
				},
				openD2XX: func(context.Context, Selectors) (controllerTransport, error) {
					return nil, errors.New("unexpected D2XX open")
				},
				bootstrap: func(context.Context, controllerTransport) (controllerLink, mmWaveLinkDeviceDiagnostics, error) {
					return nil, mmWaveLinkDeviceDiagnostics{}, errors.New("unexpected bootstrap")
				},
			})
			if !errors.Is(err, want) || enhancedOpens != 0 {
				t.Fatalf("error/Enhanced opens = %v/%d", err, enhancedOpens)
			}
			var stateFailure *TargetStateError
			if errors.As(err, &stateFailure) != test.wantTargetState {
				t.Fatalf("TargetStateError = %v, want %v; error=%v", errors.As(err, &stateFailure), test.wantTargetState, err)
			}
		})
	}
}

func TestOpenPreflightsBeforeHardware(t *testing.T) {
	valid := validOptions(t)
	tests := []struct {
		name   string
		mutate func(*Options)
	}{
		{name: "port", mutate: func(options *Options) { options.EnhancedPort = " " }},
		{name: "assets", mutate: func(options *Options) { options.Assets = Assets{} }},
		{name: "selectors", mutate: func(options *Options) {
			options.Selectors.IRQ.Description = "different B"
		}},
		{name: "plan", mutate: func(options *Options) { options.Plan = Plan{} }},
		{name: "mutated plan", mutate: func(options *Options) {
			options.Plan.operations[0].command.messageID++
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := valid
			options.Plan = Plan{
				source:     clonePlan(valid.Plan.source),
				operations: valid.Plan.operationsCopy(),
			}
			test.mutate(&options)
			opens := 0
			_, err := openWithBackend(context.Background(), options, controllerBackend{
				prepareSOP2: func(context.Context, Selectors) error {
					opens++
					return errors.New("must not prepare")
				},
				openEnhanced: func(context.Context, string) (controllerEnhancedConnection, error) {
					opens++
					return nil, errors.New("must not open")
				},
				openD2XX: func(context.Context, Selectors) (controllerTransport, error) {
					opens++
					return nil, errors.New("must not open")
				},
				bootstrap: func(context.Context, controllerTransport) (controllerLink, mmWaveLinkDeviceDiagnostics, error) {
					return nil, mmWaveLinkDeviceDiagnostics{}, errors.New("must not bootstrap")
				},
			})
			if err == nil {
				t.Fatal("openWithBackend unexpectedly succeeded")
			}
			if opens != 0 {
				t.Fatalf("hardware open calls = %d, want 0", opens)
			}
		})
	}
}

func TestOpenMarksFailuresAfterFirmwareSubmission(t *testing.T) {
	want := errors.New("injected failure")
	tests := []struct {
		name      string
		configure func(*fakeControllerEnhanced, *fakeControllerTransport, *controllerBackend)
	}{
		{
			name: "submission entered",
			configure: func(enhanced *fakeControllerEnhanced, _ *fakeControllerTransport, _ *controllerBackend) {
				enhanced.submitErr = &firmwareSubmissionError{Step: "BSS block 0", RequiresReset: true, Cause: want}
			},
		},
		{
			name: "D2XX open",
			configure: func(_ *fakeControllerEnhanced, _ *fakeControllerTransport, backend *controllerBackend) {
				backend.openD2XX = func(context.Context, Selectors) (controllerTransport, error) {
					return nil, want
				}
			},
		},
		{
			name: "bootstrap",
			configure: func(_ *fakeControllerEnhanced, _ *fakeControllerTransport, backend *controllerBackend) {
				backend.bootstrap = func(context.Context, controllerTransport) (controllerLink, mmWaveLinkDeviceDiagnostics, error) {
					return nil, mmWaveLinkDeviceDiagnostics{}, want
				}
			},
		},
		{
			name: "Enhanced close",
			configure: func(enhanced *fakeControllerEnhanced, _ *fakeControllerTransport, _ *controllerBackend) {
				enhanced.closeErr = want
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			enhanced := &fakeControllerEnhanced{receipt: validFirmwareReceipt()}
			transport := &fakeControllerTransport{}
			backend := controllerBackend{
				prepareSOP2:  func(context.Context, Selectors) error { return nil },
				openEnhanced: func(context.Context, string) (controllerEnhancedConnection, error) { return enhanced, nil },
				openD2XX:     func(context.Context, Selectors) (controllerTransport, error) { return transport, nil },
				bootstrap: func(context.Context, controllerTransport) (controllerLink, mmWaveLinkDeviceDiagnostics, error) {
					return &fakeControllerLink{}, validControllerDiagnostics(), nil
				},
			}
			test.configure(enhanced, transport, &backend)
			controller, err := openWithBackend(context.Background(), validOptions(t), backend)
			if controller != nil || err == nil {
				t.Fatalf("openWithBackend = (%v, %v), want (nil, error)", controller, err)
			}
			var stateFailure *TargetStateError
			if !errors.As(err, &stateFailure) || !stateFailure.CleanupFailed() {
				t.Fatalf("error = %v, want TargetStateError", err)
			}
			if enhanced.closeCalls != 1 {
				t.Fatalf("Enhanced close calls = %d, want 1", enhanced.closeCalls)
			}
			if test.name == "bootstrap" || test.name == "Enhanced close" {
				if transport.closeCalls != 1 {
					t.Fatalf("D2XX close calls = %d, want 1", transport.closeCalls)
				}
			}
		})
	}
}

func TestControllerAppliesImmutablePlanAndValidatesRFInit(t *testing.T) {
	source := goldenIWR6843Plan(t)
	plan, err := buildPlan(source)
	if err != nil {
		t.Fatal(err)
	}
	link := &fakeControllerLink{}
	controller := newFakeController(plan, link)

	if err := controller.Apply(context.Background(), source); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	wantOperations := plan.operationsCopy()
	if len(link.commands) != len(wantOperations) {
		t.Fatalf("commands = %d, want %d", len(link.commands), len(wantOperations))
	}
	for index := range wantOperations {
		if !reflect.DeepEqual(link.commands[index], wantOperations[index].command) {
			t.Fatalf("command %d differs from immutable plan", index)
		}
	}
	if len(link.events) != 1 || link.events[0] != [2]uint16{mmWaveLinkRFAsyncMessageID, mmWaveLinkRFInitEventSubblockID} {
		t.Fatalf("events = %v, want one RF-init event", link.events)
	}
	if controller.state != controllerStateConfigured {
		t.Fatalf("controller state = %d, want configured", controller.state)
	}
}

func TestControllerRejectsPlanMismatchWithoutIO(t *testing.T) {
	source := goldenIWR6843Plan(t)
	plan, err := buildPlan(source)
	if err != nil {
		t.Fatal(err)
	}
	link := &fakeControllerLink{}
	controller := newFakeController(plan, link)
	source.ExpectedBytes++
	if err := controller.Apply(context.Background(), source); err == nil {
		t.Fatal("Apply unexpectedly accepted a mismatched plan")
	}
	if len(link.commands) != 0 || len(link.events) != 0 {
		t.Fatalf("mismatched plan caused I/O: commands=%d events=%d", len(link.commands), len(link.events))
	}
}

func TestControllerRejectsFailedRFInitializationAndStopsPlan(t *testing.T) {
	source := goldenIWR6843Plan(t)
	plan, err := buildPlan(source)
	if err != nil {
		t.Fatal(err)
	}
	link := &fakeControllerLink{waitHook: func(_ uint16, subblock uint16) (mmWaveLinkMessage, error) {
		if subblock == mmWaveLinkRFInitEventSubblockID {
			return controllerAsyncEvent(subblock, make([]byte, mmWaveLinkRFInitEventDataLength)), nil
		}
		return controllerAsyncEvent(subblock, nil), nil
	}}
	controller := newFakeController(plan, link)
	err = controller.Apply(context.Background(), source)
	var stateFailure *TargetStateError
	if !errors.As(err, &stateFailure) || !strings.Contains(err.Error(), "calibration status") {
		t.Fatalf("Apply error = %v, want calibration TargetStateError", err)
	}
	if len(link.commands) != 5 {
		t.Fatalf("commands after failed RF init = %d, want 5", len(link.commands))
	}
	if controller.state != controllerStateUnknown {
		t.Fatalf("controller state = %d, want unknown", controller.state)
	}
}

func TestControllerStartAndStopUseSingleFrameTriggers(t *testing.T) {
	plan := mustDebugControllerPlan(t)
	link := &fakeControllerLink{}
	controller := newFakeController(plan, link)
	controller.state = controllerStateConfigured

	if _, err := controller.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	lower, upper, err := controller.StartInterval()
	if err != nil || lower.IsZero() || upper.Before(lower) || time.Since(upper) < 0 {
		t.Fatalf("frame-start interval = [%v,%v], %v", lower, upper, err)
	}
	if _, err := controller.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if len(link.commands) != 2 {
		t.Fatalf("frame trigger commands = %d, want 2", len(link.commands))
	}
	assertFrameTrigger(t, link.commands[0], 1)
	assertFrameTrigger(t, link.commands[1], 0)
	wantEvents := [][2]uint16{
		{mmWaveLinkRFAsyncMessageID, mmWaveLinkRFFrameStartEventID},
		{mmWaveLinkRFAsyncMessageID, mmWaveLinkRFFrameEndEventID},
	}
	if !reflect.DeepEqual(link.events, wantEvents) {
		t.Fatalf("frame events = %v, want %v", link.events, wantEvents)
	}
}

func TestControllerFrameStartIntervalUnavailableBeforeSuccessfulStart(t *testing.T) {
	controller := newFakeController(mustDebugControllerPlan(t), &fakeControllerLink{})
	controller.state = controllerStateConfigured
	if _, _, err := controller.StartInterval(); err == nil {
		t.Fatal("frame-start interval unexpectedly available before start")
	}
	controller.link = &fakeControllerLink{executeHook: func(mmWaveLinkCommand) (mmWaveLinkMessage, error) {
		return mmWaveLinkMessage{}, errors.New("injected trigger failure")
	}}
	if _, err := controller.Start(context.Background()); err == nil {
		t.Fatal("Start unexpectedly succeeded")
	}
	if _, _, err := controller.StartInterval(); err == nil {
		t.Fatal("frame-start interval unexpectedly available after failed start")
	}
}

func TestControllerAwaitsNaturalFiniteFrameEndWithoutStopTrigger(t *testing.T) {
	link := &fakeControllerLink{}
	controller := newFakeController(mustDebugControllerPlan(t), link)
	controller.state = controllerStateRunning

	response, err := controller.AwaitEnd(context.Background())
	if err != nil {
		t.Fatalf("AwaitEnd: %v", err)
	}
	if response != "IWR6843 finite frame ended" {
		t.Fatalf("response = %q", response)
	}
	if controller.state != controllerStateConfigured {
		t.Fatalf("controller state = %d, want configured", controller.state)
	}
	if len(link.commands) != 0 {
		t.Fatalf("natural frame end sent %d commands", len(link.commands))
	}
	wantEvents := [][2]uint16{{mmWaveLinkRFAsyncMessageID, mmWaveLinkRFFrameEndEventID}}
	if !reflect.DeepEqual(link.events, wantEvents) {
		t.Fatalf("frame events = %v, want %v", link.events, wantEvents)
	}
}

func TestControllerNaturalFiniteFrameEndFailurePoisonsState(t *testing.T) {
	waitFailure := errors.New("injected frame-end wait failure")
	tests := []struct {
		name     string
		waitHook func(uint16, uint16) (mmWaveLinkMessage, error)
		want     error
	}{
		{
			name: "wait",
			waitHook: func(uint16, uint16) (mmWaveLinkMessage, error) {
				return mmWaveLinkMessage{}, waitFailure
			},
			want: waitFailure,
		},
		{
			name: "validation",
			waitHook: func(_ uint16, subblock uint16) (mmWaveLinkMessage, error) {
				return controllerAsyncEvent(subblock, []byte{1}), nil
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			link := &fakeControllerLink{waitHook: test.waitHook}
			controller := newFakeController(mustDebugControllerPlan(t), link)
			controller.state = controllerStateRunning

			_, err := controller.AwaitEnd(context.Background())
			var stateFailure *TargetStateError
			if !errors.As(err, &stateFailure) {
				t.Fatalf("AwaitEnd error = %v, want TargetStateError", err)
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("AwaitEnd error = %v, want %v", err, test.want)
			}
			if controller.state != controllerStateUnknown {
				t.Fatalf("controller state = %d, want unknown", controller.state)
			}
			if len(link.commands) != 0 || len(link.events) != 1 {
				t.Fatalf("natural frame end I/O: commands=%d events=%d", len(link.commands), len(link.events))
			}
		})
	}
}

func TestControllerRejectsNaturalFrameEndBeforeStartWithoutIO(t *testing.T) {
	link := &fakeControllerLink{}
	controller := newFakeController(mustDebugControllerPlan(t), link)
	controller.state = controllerStateConfigured

	if _, err := controller.AwaitEnd(context.Background()); err == nil {
		t.Fatal("AwaitEnd unexpectedly succeeded")
	}
	if controller.state != controllerStateConfigured {
		t.Fatalf("controller state = %d, want configured", controller.state)
	}
	if len(link.commands) != 0 || len(link.events) != 0 {
		t.Fatalf("rejected natural frame end caused I/O: commands=%d events=%d", len(link.commands), len(link.events))
	}
}

func TestControllerFreshStopPerformsNoIO(t *testing.T) {
	link := &fakeControllerLink{}
	controller := newFakeController(mustDebugControllerPlan(t), link)
	if response, err := controller.Stop(context.Background()); err != nil || !strings.Contains(response, "already stopped") {
		t.Fatalf("fresh Stop = (%q, %v)", response, err)
	}
	if len(link.commands) != 0 || len(link.events) != 0 {
		t.Fatalf("local operations caused I/O: commands=%d events=%d", len(link.commands), len(link.events))
	}
}

func TestControllerAcceptsKnownStoppedStatuses(t *testing.T) {
	for _, code := range []uint16{mmWaveLinkStatusFrameAlreadyEnded, mmWaveLinkStatusFrameNotConfigured} {
		t.Run(strconv.Itoa(int(code)), func(t *testing.T) {
			link := &fakeControllerLink{executeHook: func(mmWaveLinkCommand) (mmWaveLinkMessage, error) {
				return mmWaveLinkMessage{}, &mmWaveLinkStatusError{
					statusCode: code,
					subblockID: mmWaveLinkRFFrameTriggerUniqueID,
				}
			}}
			controller := newFakeController(mustDebugControllerPlan(t), link)
			controller.state = controllerStateRunning
			if _, err := controller.Stop(context.Background()); err != nil {
				t.Fatalf("Stop status %d: %v", code, err)
			}
			if len(link.commands) != 1 || len(link.events) != 0 {
				t.Fatalf("status %d I/O: commands=%d events=%d", code, len(link.commands), len(link.events))
			}
		})
	}
}

func TestControllerDoesNotConvergeStatusForAnotherCommand(t *testing.T) {
	link := &fakeControllerLink{executeHook: func(mmWaveLinkCommand) (mmWaveLinkMessage, error) {
		return mmWaveLinkMessage{}, &mmWaveLinkStatusError{
			statusCode: mmWaveLinkStatusFrameAlreadyEnded,
			subblockID: mmWaveLinkRFDynamicConfigMessageID*rhcpMaxSubblocks + mmWaveLinkRFFrameSubblockID,
		}
	}}
	controller := newFakeController(mustDebugControllerPlan(t), link)
	controller.state = controllerStateRunning
	_, err := controller.Stop(context.Background())
	var stateFailure *TargetStateError
	if !errors.As(err, &stateFailure) {
		t.Fatalf("Stop error = %v, want TargetStateError", err)
	}
	if controller.state != controllerStateUnknown || len(link.commands) != 1 || len(link.events) != 0 {
		t.Fatalf(
			"wrong-command status converged: state=%d commands=%d events=%d",
			controller.state,
			len(link.commands),
			len(link.events),
		)
	}
}

func TestControllerNeverRetriesUnknownStart(t *testing.T) {
	want := errors.New("unknown write result")
	link := &fakeControllerLink{executeHook: func(mmWaveLinkCommand) (mmWaveLinkMessage, error) {
		return mmWaveLinkMessage{}, want
	}}
	controller := newFakeController(mustDebugControllerPlan(t), link)
	controller.state = controllerStateConfigured
	if _, err := controller.Start(context.Background()); err == nil {
		t.Fatal("first Start unexpectedly succeeded")
	}
	if _, err := controller.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("second Start error = %v", err)
	}
	if len(link.commands) != 1 {
		t.Fatalf("start commands = %d, want exactly 1", len(link.commands))
	}
}

func TestControllerCloseOnlyClosesD2XX(t *testing.T) {
	transport := &fakeControllerTransport{}
	link := &fakeControllerLink{}
	controller := newFakeController(mustDebugControllerPlan(t), link)
	controller.transport = transport
	controller.state = controllerStateRunning
	if err := controller.Close(); err != nil {
		t.Fatal(err)
	}
	if transport.closeCalls != 1 || len(link.commands) != 0 || len(link.events) != 0 {
		t.Fatalf("Close side effects: closes=%d commands=%d events=%d", transport.closeCalls, len(link.commands), len(link.events))
	}
}

func validOptions(t *testing.T) Options {
	t.Helper()
	return Options{
		EnhancedPort: "COM3",
		Assets:       fixtureSubmissionAssets(t),
		Selectors: Selectors{
			SPI: d2xx.Selector{Description: "AR-DevPack-EVM-012 A"},
			IRQ: d2xx.Selector{Description: "AR-DevPack-EVM-012 B"},
		},
		Plan: mustDebugControllerPlan(t),
	}
}

func mustDebugControllerPlan(t *testing.T) Plan {
	t.Helper()
	plan, err := buildPlan(goldenIWR6843Plan(t))
	if err != nil {
		t.Fatalf("buildPlan: %v", err)
	}
	return plan
}

func validFirmwareReceipt() firmwareSubmissionReceipt {
	return firmwareSubmissionReceipt{State: firmwareSubmittedUnverified, BSSBlocks: 1, MSSBlocks: 1}
}

func validControllerDiagnostics() mmWaveLinkDeviceDiagnostics {
	mss := mmWaveLinkFirmwareVersion{FirmwareMajor: 2, FirmwareMinor: 0, FirmwareBuild: 0, FirmwareDebug: 3}
	rf := mmWaveLinkFirmwareVersion{FirmwareMajor: 6, FirmwareMinor: 2, FirmwareBuild: 1, FirmwareDebug: 5}
	return mmWaveLinkDeviceDiagnostics{MSS: mss, RF: rf, RFPowerupStatus: mmWaveLinkRFPowerupStatusDone}
}

func newFakeController(plan Plan, link controllerLink) *Controller {
	return &Controller{
		link:        link,
		transport:   &fakeControllerTransport{},
		plan:        plan,
		diagnostics: validControllerDiagnostics(),
		state:       controllerStateBootstrapped,
	}
}

func assertFrameTrigger(t *testing.T, command mmWaveLinkCommand, want uint32) {
	t.Helper()
	if command.direction != rhcpDirectionHostToBSS || command.messageID != mmWaveLinkRFFrameTriggerMessageID ||
		len(command.subblocks) != 1 || command.subblocks[0].id != mmWaveLinkRFFrameTriggerSubblockID ||
		len(command.subblocks[0].data) != 4 || binary.LittleEndian.Uint32(command.subblocks[0].data) != want {
		t.Fatalf("frame trigger = %+v, want value %d", command, want)
	}
}

type fakeControllerEnhanced struct {
	calls      *[]string
	receipt    firmwareSubmissionReceipt
	submitErr  error
	closeErr   error
	closeCalls int
}

func (enhanced *fakeControllerEnhanced) submitFirmware(context.Context, Assets) (firmwareSubmissionReceipt, error) {
	if enhanced.calls != nil {
		*enhanced.calls = append(*enhanced.calls, "submit-firmware")
	}
	return enhanced.receipt, enhanced.submitErr
}

func (enhanced *fakeControllerEnhanced) close() error {
	enhanced.closeCalls++
	if enhanced.calls != nil {
		*enhanced.calls = append(*enhanced.calls, "close-enhanced")
	}
	return enhanced.closeErr
}

type fakeControllerTransport struct {
	calls      *[]string
	closeErr   error
	closeCalls int
}

func (*fakeControllerTransport) SPIWrite(context.Context, []byte) error { return nil }
func (*fakeControllerTransport) SPIRead(context.Context, []byte) error  { return nil }
func (*fakeControllerTransport) WaitIRQ(context.Context, bool) error    { return nil }
func (transport *fakeControllerTransport) Close() error {
	transport.closeCalls++
	if transport.calls != nil {
		*transport.calls = append(*transport.calls, "close-d2xx")
	}
	return transport.closeErr
}

type fakeControllerLink struct {
	commands    []mmWaveLinkCommand
	events      [][2]uint16
	executeHook func(mmWaveLinkCommand) (mmWaveLinkMessage, error)
	waitHook    func(uint16, uint16) (mmWaveLinkMessage, error)
}

func (link *fakeControllerLink) execute(
	_ context.Context,
	command mmWaveLinkCommand,
) (mmWaveLinkMessage, error) {
	link.commands = append(link.commands, cloneMMWaveLinkCommand(command))
	if link.executeHook != nil {
		return link.executeHook(command)
	}
	return mmWaveLinkMessage{}, nil
}

func (link *fakeControllerLink) waitEvent(
	_ context.Context,
	messageID uint16,
	subblockID uint16,
) (mmWaveLinkMessage, error) {
	link.events = append(link.events, [2]uint16{messageID, subblockID})
	if link.waitHook != nil {
		return link.waitHook(messageID, subblockID)
	}
	if subblockID == mmWaveLinkRFInitEventSubblockID {
		data := make([]byte, mmWaveLinkRFInitEventDataLength)
		binary.LittleEndian.PutUint32(data, mmWaveLinkRFInitSuccessMask)
		return controllerAsyncEvent(subblockID, data), nil
	}
	return controllerAsyncEvent(subblockID, nil), nil
}

func controllerAsyncEvent(subblockID uint16, data []byte) mmWaveLinkMessage {
	return mmWaveLinkMessage{
		direction:    rhcpDirectionBSSToHost,
		messageClass: rhcpMessageClassAsync,
		messageID:    mmWaveLinkRFAsyncMessageID,
		subblocks: []mmWaveLinkSubblock{{
			id:   subblockID,
			data: append([]byte(nil), data...),
		}},
	}
}
