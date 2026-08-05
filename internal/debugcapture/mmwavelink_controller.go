package debugcapture

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"mmwcli/internal/radar"
)

const (
	mmWaveLinkRFFrameTriggerMessageID  = 0x00a
	mmWaveLinkRFFrameTriggerSubblockID = 0
	mmWaveLinkRFFrameTriggerUniqueID   = mmWaveLinkRFFrameTriggerMessageID*rhcpMaxSubblocks + mmWaveLinkRFFrameTriggerSubblockID
	mmWaveLinkRFFrameStartEventID      = 11
	mmWaveLinkRFFrameEndEventID        = 15

	mmWaveLinkStatusFrameAlreadyEnded  = 21
	mmWaveLinkStatusFrameNotConfigured = 22
)

// ErrReuseConfigurationUnsupported reports that SOP2 debug capture always
// downloads firmware and applies the complete mmWaveLink configuration.
var ErrReuseConfigurationUnsupported = errors.New("debug-capture does not support starting without configuration")

// ControllerOptions binds one fully preflighted configuration and one explicit
// pair of D2XX interfaces to an xWR6843 in SOP2 mode.
type ControllerOptions struct {
	EnhancedPort string
	Assets       Assets
	Selectors    D2XXSelectors
	Plan         Plan
}

// TargetStateError marks a failure after firmware submission. The controller
// never resets the target automatically; an operator must explicitly restore a
// known boot state before trying another debug-capture session.
type TargetStateError struct{ Err error }

func (failure *TargetStateError) Error() string {
	return "debug-capture target state requires an explicit reset: " + failure.Err.Error()
}

func (failure *TargetStateError) Unwrap() error       { return failure.Err }
func (failure *TargetStateError) CleanupFailed() bool { return true }

type controllerLink interface {
	execute(context.Context, mmWaveLinkCommand) (mmWaveLinkMessage, error)
	waitEvent(context.Context, uint16, uint16) (mmWaveLinkMessage, error)
}

type controllerEnhancedConnection interface {
	submitFirmware(context.Context, Assets) (firmwareSubmissionReceipt, error)
	close() error
}

type controllerTransport interface {
	mmWaveLinkTransport
	Close() error
}

type controllerBackend struct {
	openEnhanced func(context.Context, string) (controllerEnhancedConnection, error)
	openD2XX     func(context.Context, D2XXSelectors) (controllerTransport, error)
	bootstrap    func(context.Context, controllerTransport) (controllerLink, mmWaveLinkDeviceDiagnostics, error)
}

type controllerState uint8

const (
	controllerStateBootstrapped controllerState = iota
	controllerStateConfigured
	controllerStateRunning
	controllerStateUnknown
)

// Controller implements the radar lifecycle used by a capture session. It is
// bound to the immutable Plan supplied to OpenController.
type Controller struct {
	mu          sync.Mutex
	link        controllerLink
	transport   controllerTransport
	plan        Plan
	diagnostics mmWaveLinkDeviceDiagnostics
	state       controllerState
	closed      bool
	closeErr    error
}

// OpenController submits BSS then MSS firmware over the explicit Enhanced COM
// port, opens the explicit D2XX A/B interfaces, and completes the mmWaveLink
// identity/version bootstrap. Every offline preflight runs before either
// hardware transport is opened.
func OpenController(ctx context.Context, options ControllerOptions) (*Controller, error) {
	return openControllerWithBackend(ctx, options, controllerBackend{
		openEnhanced: func(ctx context.Context, port string) (controllerEnhancedConnection, error) {
			return openEnhancedCOMConnection(ctx, port)
		},
		openD2XX: func(ctx context.Context, selectors D2XXSelectors) (controllerTransport, error) {
			return OpenD2XXTransport(ctx, selectors)
		},
		bootstrap: func(
			ctx context.Context,
			transport controllerTransport,
		) (controllerLink, mmWaveLinkDeviceDiagnostics, error) {
			client, err := newMMWaveLinkClient(transport)
			if err != nil {
				return nil, mmWaveLinkDeviceDiagnostics{}, err
			}
			diagnostics, err := bootstrapMMWaveLink(ctx, client)
			return client, diagnostics, err
		},
	})
}

func openControllerWithBackend(
	ctx context.Context,
	options ControllerOptions,
	backend controllerBackend,
) (*Controller, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := preflightControllerOptions(options); err != nil {
		return nil, err
	}
	if backend.openEnhanced == nil || backend.openD2XX == nil || backend.bootstrap == nil {
		return nil, errors.New("debug-capture controller backend is incomplete")
	}

	enhanced, err := backend.openEnhanced(ctx, options.EnhancedPort)
	if err != nil {
		return nil, err
	}
	if enhanced == nil {
		return nil, errors.New("Enhanced COM opener returned a nil connection")
	}
	receipt, err := enhanced.submitFirmware(ctx, options.Assets)
	if err != nil {
		closeErr := enhanced.close()
		var submitted *firmwareSubmissionError
		if errors.As(err, &submitted) && submitted.RequiresReset {
			return nil, targetStateFailure(errors.Join(err, closeErr))
		}
		return nil, errors.Join(err, closeErr)
	}
	if receipt.State != firmwareSubmittedUnverified || receipt.BSSBlocks <= 0 || receipt.MSSBlocks <= 0 {
		return nil, targetStateFailure(errors.Join(
			errors.New("Enhanced COM firmware submission returned an invalid receipt"),
			enhanced.close(),
		))
	}

	transport, err := backend.openD2XX(ctx, options.Selectors)
	if err != nil {
		return nil, targetStateFailure(errors.Join(err, enhanced.close()))
	}
	if transport == nil {
		return nil, targetStateFailure(errors.Join(
			errors.New("D2XX opener returned a nil transport"),
			enhanced.close(),
		))
	}
	link, diagnostics, err := backend.bootstrap(ctx, transport)
	if err != nil {
		return nil, targetStateFailure(errors.Join(err, transport.Close(), enhanced.close()))
	}
	if link == nil {
		return nil, targetStateFailure(errors.Join(
			errors.New("mmWaveLink bootstrap returned a nil client"),
			transport.Close(),
			enhanced.close(),
		))
	}
	if err := enhanced.close(); err != nil {
		return nil, targetStateFailure(errors.Join(err, transport.Close()))
	}
	return &Controller{
		link:        link,
		transport:   transport,
		plan:        options.Plan,
		diagnostics: diagnostics,
		state:       controllerStateBootstrapped,
	}, nil
}

func preflightControllerOptions(options ControllerOptions) error {
	if options.EnhancedPort == "" || strings.TrimSpace(options.EnhancedPort) == "" {
		return errors.New("debug-capture Enhanced COM port is required; ports are never scanned")
	}
	if options.EnhancedPort != strings.TrimSpace(options.EnhancedPort) {
		return fmt.Errorf("debug-capture Enhanced COM port %q has leading or trailing whitespace", options.EnhancedPort)
	}
	if strings.IndexByte(options.EnhancedPort, 0) >= 0 {
		return errors.New("debug-capture Enhanced COM port contains NUL")
	}
	if _, err := preflightFirmwareSubmission(options.Assets); err != nil {
		return err
	}
	if err := options.Selectors.validate(); err != nil {
		return err
	}
	rebuilt, err := BuildPlan(options.Plan.source)
	if err != nil {
		return fmt.Errorf("invalid debug-capture mmWaveLink plan: %w", err)
	}
	if !reflect.DeepEqual(rebuilt, options.Plan) {
		return errors.New("debug-capture mmWaveLink plan does not match its immutable source configuration")
	}
	return nil
}

// VerifyPlatformContext reports the MSS/RF firmware gate completed during
// OpenController. It performs no additional hardware I/O.
func (controller *Controller) VerifyPlatformContext(ctx context.Context) (string, error) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if err := controller.readyLocked(ctx); err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"xWR68xx debug-capture MSS %s RF %s",
		formatMMWaveLinkFirmwareVersion(controller.diagnostics.MSS),
		formatMMWaveLinkFirmwareVersion(controller.diagnostics.RF),
	), nil
}

// ApplyContext sends the exact immutable operation plan. It validates the RF
// initialization calibration event before any later configuration command.
func (controller *Controller) ApplyContext(ctx context.Context, source radar.CapturePlan) error {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if err := controller.readyLocked(ctx); err != nil {
		return err
	}
	if !controller.plan.matchesCapturePlan(source) {
		return errors.New("capture plan does not match the debug-capture controller plan")
	}
	if controller.state != controllerStateBootstrapped {
		return fmt.Errorf("debug-capture configuration cannot be applied in controller state %d", controller.state)
	}

	for index, operation := range controller.plan.operationsCopy() {
		response, err := controller.link.execute(ctxOrBackground(ctx), operation.command)
		if err != nil {
			controller.state = controllerStateUnknown
			return targetStateFailure(fmt.Errorf("apply mmWaveLink operation %d: %w", index, err))
		}
		if len(response.subblocks) != 0 {
			controller.state = controllerStateUnknown
			return targetStateFailure(fmt.Errorf(
				"mmWaveLink operation %d response has %d sub-blocks; expected a header-only response",
				index,
				len(response.subblocks),
			))
		}
		if operation.await == nil {
			continue
		}
		event, err := controller.link.waitEvent(ctxOrBackground(ctx), operation.await.messageID, operation.await.subblockID)
		if err != nil {
			controller.state = controllerStateUnknown
			return targetStateFailure(fmt.Errorf("wait after mmWaveLink operation %d: %w", index, err))
		}
		if err := operation.await.validate(event); err != nil {
			controller.state = controllerStateUnknown
			return targetStateFailure(fmt.Errorf("validate event after mmWaveLink operation %d: %w", index, err))
		}
	}
	controller.state = controllerStateConfigured
	return nil
}

// StartContext sends one frame-start trigger and waits for one strict
// frame-start event. A failed or unknown start is never retried.
func (controller *Controller) StartContext(ctx context.Context) (string, error) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if err := controller.readyLocked(ctx); err != nil {
		return "", err
	}
	if controller.state != controllerStateConfigured {
		return "", fmt.Errorf("debug-capture start requires an applied, stopped configuration; state=%d", controller.state)
	}
	controller.state = controllerStateUnknown
	if err := controller.frameTriggerLocked(ctx, true, mmWaveLinkRFFrameStartEventID); err != nil {
		return "", targetStateFailure(fmt.Errorf("start debug-capture frame: %w", err))
	}
	controller.state = controllerStateRunning
	return "debug-capture frame started", nil
}

func (controller *Controller) StartWithoutReconfigurationContext(ctx context.Context) (string, error) {
	if err := contextError(ctx); err != nil {
		return "", err
	}
	return "", ErrReuseConfigurationUnsupported
}

// StopContext is a local no-op only for the fresh, post-bootstrap state. Once
// frame configuration has been sent, it emits exactly one stop trigger and
// accepts the two documented already-stopped status codes as convergence.
func (controller *Controller) StopContext(ctx context.Context) (string, error) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if err := controller.readyLocked(ctx); err != nil {
		return "", err
	}
	if controller.state == controllerStateBootstrapped {
		return "debug-capture frame already stopped after bootstrap", nil
	}
	if controller.state != controllerStateConfigured && controller.state != controllerStateRunning {
		return "", fmt.Errorf("debug-capture stop cannot continue from unknown controller state %d", controller.state)
	}

	controller.state = controllerStateUnknown
	err := controller.frameTriggerLocked(ctx, false, mmWaveLinkRFFrameEndEventID)
	if isKnownStoppedStatus(err) {
		controller.state = controllerStateConfigured
		return "debug-capture frame already stopped", nil
	}
	if err != nil {
		return "", targetStateFailure(fmt.Errorf("stop debug-capture frame: %w", err))
	}
	controller.state = controllerStateConfigured
	return "debug-capture frame stopped", nil
}

func (controller *Controller) frameTriggerLocked(ctx context.Context, start bool, eventSubblock uint16) error {
	data := make([]byte, 4)
	if start {
		binary.LittleEndian.PutUint32(data, 1)
	}
	response, err := controller.link.execute(ctxOrBackground(ctx), mmWaveLinkCommand{
		direction: rhcpDirectionHostToBSS,
		messageID: mmWaveLinkRFFrameTriggerMessageID,
		subblocks: []mmWaveLinkSubblock{{id: mmWaveLinkRFFrameTriggerSubblockID, data: data}},
	})
	if err != nil {
		return err
	}
	if len(response.subblocks) != 0 {
		return fmt.Errorf("frame-trigger response has %d sub-blocks; expected a header-only response", len(response.subblocks))
	}
	event, err := controller.link.waitEvent(ctxOrBackground(ctx), mmWaveLinkRFAsyncMessageID, eventSubblock)
	if err != nil {
		return err
	}
	return (mmWaveLinkPlanEvent{
		direction:  rhcpDirectionBSSToHost,
		messageID:  mmWaveLinkRFAsyncMessageID,
		subblockID: eventSubblock,
		dataLength: 0,
	}).validate(event)
}

func isKnownStoppedStatus(err error) bool {
	var status *mmWaveLinkStatusError
	if !errors.As(err, &status) || status.subblockID != mmWaveLinkRFFrameTriggerUniqueID {
		return false
	}
	return status.statusCode == mmWaveLinkStatusFrameAlreadyEnded ||
		status.statusCode == mmWaveLinkStatusFrameNotConfigured
}

func (controller *Controller) readyLocked(ctx context.Context) error {
	if controller == nil || controller.link == nil || controller.transport == nil {
		return errors.New("debug-capture controller is not initialized")
	}
	if controller.closed {
		return errors.New("debug-capture controller is closed")
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if controller.state == controllerStateUnknown {
		return errors.New("debug-capture controller cannot continue from an unknown target state; explicit reset required")
	}
	return nil
}

// Close releases D2XX handles only. It never sends radar commands or resets
// the target and is safe to call more than once.
func (controller *Controller) Close() error {
	if controller == nil {
		return nil
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.closed {
		return controller.closeErr
	}
	controller.closed = true
	if controller.transport != nil {
		controller.closeErr = controller.transport.Close()
	}
	return controller.closeErr
}

func targetStateFailure(err error) error {
	if err == nil {
		return nil
	}
	var marked *TargetStateError
	if errors.As(err, &marked) {
		return err
	}
	return &TargetStateError{Err: err}
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func ctxOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func formatMMWaveLinkFirmwareVersion(version mmWaveLinkFirmwareVersion) string {
	return fmt.Sprintf(
		"%d.%d.%d.%d",
		version.FirmwareMajor,
		version.FirmwareMinor,
		version.FirmwareBuild,
		version.FirmwareDebug,
	)
}
