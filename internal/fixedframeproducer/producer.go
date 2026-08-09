// Package fixedframeproducer adapts one raw fixed-size camera frame stream to
// the mmwcli sensor-producer protocol.
package fixedframeproducer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"mmwcli/internal/multisensor"
	"mmwcli/internal/multisensorcapture"
	"mmwcli/internal/sensorproducer"
)

const (
	ProducerName        = "mmwcli-fixed-frames"
	ProducerVersion     = "1"
	JPEGProducerName    = "mmwcli-jpeg-stream"
	JPEGProducerVersion = "1"
	JPEGFormat          = "image.jpeg.v1"
	MaximumFrameBytes   = uint64(sensorproducer.MaxPayloadBytes)
)

type producer struct {
	plan       multisensorcapture.SourcePlan
	frameBytes int
	jpeg       bool
	childArgv  []string
	stderr     io.Writer
	controls   *sensorproducer.ControlDecoder
	records    *sensorproducer.Encoder

	sessionID      string
	nextControlSeq uint64
	nextRecordSeq  uint64
	itemCount      uint64
	payloadBytes   uint64
	payloadHash    hash.Hash
	child          *cameraChild
}

type controlResult struct {
	control sensorproducer.Control
	err     error
}

// Run serves one sensor-producer connection and launches cameraArgv on ARM.
// The child must write headerless fixed-size frames to its stdout.
func Run(
	ctx context.Context,
	plan multisensorcapture.SourcePlan,
	frameBytes uint64,
	cameraArgv []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
) error {
	return run(ctx, plan, frameBytes, false, cameraArgv, stdin, stdout, stderr)
}

// RunJPEG serves one sensor-producer connection whose child writes a
// concatenated stream of complete JPEG images.
func RunJPEG(
	ctx context.Context,
	plan multisensorcapture.SourcePlan,
	cameraArgv []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
) error {
	return run(ctx, plan, plan.Limits.MaxItemBytes, true, cameraArgv, stdin, stdout, stderr)
}

func run(
	ctx context.Context,
	plan multisensorcapture.SourcePlan,
	frameBytes uint64,
	jpeg bool,
	cameraArgv []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateConfiguration(plan, frameBytes, jpeg, cameraArgv, stdin, stdout); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	controls, err := sensorproducer.NewControlDecoder(stdin)
	if err != nil {
		return err
	}
	records, err := sensorproducer.NewEncoder(stdout)
	if err != nil {
		return err
	}
	if stderr == nil {
		stderr = io.Discard
	}
	runner := &producer{
		plan: plan, frameBytes: int(frameBytes), jpeg: jpeg,
		childArgv: append([]string(nil), cameraArgv...),
		stderr:    stderr, controls: controls, records: records,
		nextControlSeq: 1, nextRecordSeq: 1, payloadHash: sha256.New(),
	}
	return runner.run(ctx)
}

func validateConfiguration(
	plan multisensorcapture.SourcePlan,
	frameBytes uint64,
	jpeg bool,
	cameraArgv []string,
	stdin io.Reader,
	stdout io.Writer,
) error {
	if stdin == nil || stdout == nil {
		return errors.New("camera producer stdin and stdout are required")
	}
	if plan.Kind != multisensor.SourceCamera {
		return errors.New("camera producer requires a camera source plan")
	}
	clock := plan.Clock
	if clock.TimestampSemantics != multisensor.TimestampDeliveryObserved ||
		clock.ClockID != multisensor.DeliveryObservedClockID(plan.SourceID) ||
		clock.TickHz != uint64(time.Second/time.Nanosecond) || clock.WrapTicks != 0 {
		return errors.New("camera producer requires the source delivery_observed 1GHz clock")
	}
	if plan.SyncEventSemantics != multisensorcapture.SyncEventSemanticsNone {
		return errors.New("camera producer requires sync_event_semantics=none")
	}
	if err := plan.Limits.Validate(); err != nil {
		return fmt.Errorf("camera producer limits: %w", err)
	}
	maximumInt := uint64(^uint(0) >> 1)
	if frameBytes == 0 || frameBytes > maximumInt ||
		frameBytes > MaximumFrameBytes ||
		frameBytes > plan.Limits.MaxItemBytes || frameBytes > plan.Limits.MaxPayloadBytes {
		return errors.New("frame_bytes is outside the producer or source limits")
	}
	if jpeg {
		if plan.Payload.Format != JPEGFormat {
			return fmt.Errorf("JPEG producer requires payload format %q", JPEGFormat)
		}
		if frameBytes != plan.Limits.MaxItemBytes {
			return errors.New("JPEG producer maximum item size must match the source limits")
		}
	}
	if len(cameraArgv) == 0 || cameraArgv[0] == "" {
		return errors.New("camera child argv[0] is required")
	}
	for index, argument := range cameraArgv {
		if strings.IndexByte(argument, 0) >= 0 {
			return fmt.Errorf("camera child argv[%d] contains NUL", index)
		}
	}
	return nil
}

func (runner *producer) run(ctx context.Context) error {
	ready, err := runner.nextControl(ctx)
	if err != nil {
		return err
	}
	if ready.Command == sensorproducer.CommandCancel {
		return runner.cancel(ready)
	}
	if ready.Command != sensorproducer.CommandReady {
		return runner.reject(ready, errors.New("expected READY"))
	}
	if err := runner.writeACK(ready, true, ""); err != nil {
		return err
	}

	arm, err := runner.nextControl(ctx)
	if err != nil {
		return err
	}
	if arm.Command == sensorproducer.CommandCancel {
		return runner.cancel(arm)
	}
	if arm.Command != sensorproducer.CommandArm {
		return runner.reject(arm, errors.New("expected ARM"))
	}
	child, err := startCameraChild(ctx, runner.childArgv, runner.stderr)
	if err != nil {
		failure := fmt.Errorf("start camera child: %w", err)
		return runner.reject(arm, failure)
	}
	runner.child = child
	defer func() {
		_, _ = runner.child.terminate()
	}()
	if err := runner.writeACK(arm, true, ""); err != nil {
		return err
	}

	start, err := runner.nextControl(ctx)
	if err != nil {
		return err
	}
	if start.Command == sensorproducer.CommandCancel {
		return runner.cancel(start)
	}
	if start.Command != sensorproducer.CommandStart {
		return runner.reject(start, errors.New("expected START"))
	}
	if err := runner.writeACK(start, true, ""); err != nil {
		return err
	}
	if err := runner.writeSession(); err != nil {
		return err
	}
	return runner.stream(ctx)
}

func (runner *producer) stream(ctx context.Context) error {
	controls := make(chan controlResult, 1)
	go func() {
		control, err := runner.nextControl(ctx)
		controls <- controlResult{control: control, err: err}
	}()
	var events <-chan frameEvent
	if runner.jpeg {
		events = runner.child.streamJPEG(runner.frameBytes)
	} else {
		events = runner.child.streamFixed(runner.frameBytes)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case result := <-controls:
			if result.err != nil {
				return runner.fail(fmt.Errorf("read terminal producer control: %w", result.err))
			}
			switch result.control.Command {
			case sensorproducer.CommandStop:
				return runner.stop(result.control)
			case sensorproducer.CommandCancel:
				return runner.cancel(result.control)
			default:
				return runner.reject(result.control, errors.New("expected STOP or CANCEL"))
			}
		case event, ok := <-events:
			if !ok {
				return runner.fail(errors.New("camera frame reader stopped before STOP"))
			}
			if event.readErr != nil {
				_, waitErr := runner.child.terminate()
				failure := classifyFrameFailure(event, waitErr, runner.frameBytes, runner.jpeg)
				return errors.Join(failure, runner.writeFailure(failure))
			}
			if err := runner.writeItem(event.payload); err != nil {
				return err
			}
			close(event.consumed)
		}
	}
}

func (runner *producer) nextControl(ctx context.Context) (sensorproducer.Control, error) {
	result := make(chan controlResult, 1)
	go func() {
		control, err := runner.controls.Read()
		result <- controlResult{control: control, err: err}
	}()
	select {
	case <-ctx.Done():
		return sensorproducer.Control{}, ctx.Err()
	case decoded := <-result:
		if decoded.err != nil {
			return sensorproducer.Control{}, decoded.err
		}
		control := decoded.control
		if control.SourceID != runner.plan.SourceID {
			return sensorproducer.Control{}, fmt.Errorf(
				"control source_id %q does not match plan source_id %q",
				control.SourceID,
				runner.plan.SourceID,
			)
		}
		if runner.sessionID == "" {
			runner.sessionID = control.SessionID
		} else if control.SessionID != runner.sessionID {
			return sensorproducer.Control{}, errors.New("control session_id changed")
		}
		if control.Seq != runner.nextControlSeq {
			return sensorproducer.Control{}, fmt.Errorf(
				"control sequence is %d, want %d",
				control.Seq,
				runner.nextControlSeq,
			)
		}
		runner.nextControlSeq++
		return control, nil
	}
}

func (runner *producer) writeACK(control sensorproducer.Control, ok bool, message string) error {
	record, err := sensorproducer.NewACKRecord(control, ok, message)
	if err != nil {
		return err
	}
	return runner.records.Write(record)
}

func (runner *producer) writeSession() error {
	return runner.writeRecord(sensorproducer.FrameSession, multisensorcapture.ProducerSessionMetadata{
		Schema: multisensorcapture.ProducerSessionSchema, Kind: runner.plan.Kind,
		Producer: runner.plan.Producer, Limits: runner.plan.Limits, Payload: runner.plan.Payload,
		Clock: runner.plan.Clock, ClockObservations: []multisensor.ClockObservation{},
		AffineSegments:      []multisensor.AffineSegment{},
		SyncEventSemantics:  runner.plan.SyncEventSemantics,
		ApplicationMetadata: runner.plan.ApplicationMetadata,
	}, nil)
}

func (runner *producer) writeItem(payload []byte) error {
	frameBytes := uint64(len(payload))
	if frameBytes == 0 || frameBytes > runner.plan.Limits.MaxItemBytes {
		return runner.fail(errors.New("camera child produced an item outside the source limits"))
	}
	if runner.itemCount >= runner.plan.Limits.MaxItems ||
		runner.payloadBytes > runner.plan.Limits.MaxPayloadBytes-frameBytes {
		return runner.fail(errors.New("camera child output exceeds the source limits"))
	}
	metadata := multisensorcapture.ProducerItemMetadata{
		Schema: multisensorcapture.ProducerItemSchema, ItemIndex: runner.itemCount,
		Tick: 0, WrapCount: 0, DurationTicks: 0, SyncEventID: multisensor.NoSyncEventID,
	}
	if err := runner.writeRecord(sensorproducer.FrameItem, metadata, payload); err != nil {
		return err
	}
	_, _ = runner.payloadHash.Write(payload)
	runner.itemCount++
	runner.payloadBytes += frameBytes
	return nil
}

func (runner *producer) stop(control sensorproducer.Control) error {
	alreadyExited, waitErr := runner.child.terminate()
	if err := runner.writeACK(control, true, ""); err != nil {
		return err
	}
	if alreadyExited {
		failure := errors.New("camera child exited before STOP")
		if waitErr != nil {
			failure = fmt.Errorf("camera child failed before STOP: %w", waitErr)
		}
		return errors.Join(failure, runner.writeFailure(failure))
	}
	if err := runner.writeRecord(sensorproducer.FrameEnd, multisensorcapture.ProducerEndMetadata{
		Schema: multisensorcapture.ProducerEndSchema, ItemCount: runner.itemCount,
		PayloadBytes: runner.payloadBytes, PayloadSHA256: hex.EncodeToString(runner.payloadHash.Sum(nil)),
	}, nil); err != nil {
		return err
	}
	return runner.writeEOF()
}

func (runner *producer) cancel(control sensorproducer.Control) error {
	if runner.child != nil {
		_, _ = runner.child.terminate()
	}
	if err := runner.writeACK(control, true, ""); err != nil {
		return err
	}
	return runner.writeEOF()
}

func (runner *producer) reject(control sensorproducer.Control, cause error) error {
	if runner.child != nil {
		_, _ = runner.child.terminate()
	}
	message := boundedErrorMessage(cause)
	ackErr := runner.writeACK(control, false, message)
	if ackErr != nil {
		return errors.Join(cause, ackErr)
	}
	return errors.Join(cause, runner.writeFailure(cause))
}

func (runner *producer) fail(cause error) error {
	if runner.child != nil {
		_, _ = runner.child.terminate()
	}
	return errors.Join(cause, runner.writeFailure(cause))
}

func (runner *producer) writeFailure(cause error) error {
	if err := runner.writeRecord(
		sensorproducer.FrameError,
		sensorproducer.ErrorMetadata{Message: boundedErrorMessage(cause)},
		nil,
	); err != nil {
		return err
	}
	return runner.writeEOF()
}

func (runner *producer) writeEOF() error {
	return runner.writeRecord(sensorproducer.FrameEOF, multisensorcapture.ProducerEOFMetadata{
		Schema: multisensorcapture.ProducerEOFSchema,
	}, nil)
}

func (runner *producer) writeRecord(
	frameType sensorproducer.FrameType,
	metadata any,
	payload []byte,
) error {
	if runner.nextRecordSeq == 0 {
		return errors.New("sensor producer record sequence is exhausted")
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("encode camera producer metadata: %w", err)
	}
	sequence := runner.nextRecordSeq
	if sequence == math.MaxUint64 {
		runner.nextRecordSeq = 0
	} else {
		runner.nextRecordSeq++
	}
	return runner.records.Write(sensorproducer.Record{
		Type: frameType, SessionID: runner.sessionID, SourceID: runner.plan.SourceID,
		Seq: sequence, Metadata: encoded, Payload: payload,
	})
}

func classifyFrameFailure(event frameEvent, waitErr error, frameBytes int, jpeg bool) error {
	if jpeg && event.readBytes != 0 {
		return fmt.Errorf("camera child produced an invalid JPEG item: %w", event.readErr)
	}
	if event.readBytes != 0 {
		return fmt.Errorf(
			"camera child produced a partial frame: got %d of %d bytes",
			event.readBytes,
			frameBytes,
		)
	}
	if waitErr != nil {
		return fmt.Errorf("camera child failed before STOP: %w", waitErr)
	}
	if errors.Is(event.readErr, io.EOF) || errors.Is(event.readErr, io.ErrUnexpectedEOF) {
		if jpeg && errors.Is(event.readErr, io.ErrUnexpectedEOF) {
			return errors.New("camera child produced a truncated JPEG before STOP")
		}
		return errors.New("camera child reached EOF before STOP")
	}
	return fmt.Errorf("read camera child frame: %w", event.readErr)
}

func boundedErrorMessage(err error) string {
	message := err.Error()
	for len(message) > sensorproducer.MaxErrorBytes {
		_, size := utf8.DecodeLastRuneInString(message)
		if size == 0 {
			break
		}
		message = message[:len(message)-size]
	}
	if message == "" {
		return "camera producer failed"
	}
	return message
}
