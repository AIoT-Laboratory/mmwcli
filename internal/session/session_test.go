package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"mmwcli/internal/capturefile"
	"mmwcli/internal/dca"
	"mmwcli/internal/radar"
)

type fakeRadar struct {
	events     *[]string
	stopCalls  int
	stopHook   func(context.Context, int) error
	startErr   error
	lower      time.Time
	upper      time.Time
	awaitCalls int
	awaitHook  func(context.Context) error
}

func (f *fakeRadar) VerifyPlatform() (string, error) {
	*f.events = append(*f.events, "version")
	return "Platform : xWR68xx\nDone\n", nil
}
func (f *fakeRadar) Verify(context.Context) (string, error) {
	return f.VerifyPlatform()
}
func (f *fakeRadar) Stop(ctx context.Context) (string, error) {
	*f.events = append(*f.events, "sensorStop")
	f.stopCalls++
	if f.stopHook != nil {
		return "", f.stopHook(ctx, f.stopCalls)
	}
	return "Done\n", nil
}
func (f *fakeRadar) Apply(_ context.Context, _ radar.Plan) error {
	*f.events = append(*f.events, "apply")
	return nil
}
func (f *fakeRadar) Start(context.Context) (string, error) {
	f.lower = time.Now()
	*f.events = append(*f.events, "sensorStart")
	f.upper = time.Now()
	return "Done\n", f.startErr
}

func (f *fakeRadar) AwaitEnd(ctx context.Context) (string, error) {
	*f.events = append(*f.events, "frameEnd")
	f.awaitCalls++
	if f.awaitHook != nil {
		return "", f.awaitHook(ctx)
	}
	return "finite frame ended", nil
}

func (f *fakeRadar) StartInterval() (time.Time, time.Time, error) {
	return f.lower, f.upper, nil
}

type fakeFiniteFrameRadar struct {
	*fakeRadar
	awaitCalls int
	awaitHook  func(context.Context) error
}

type fakeStartInterval struct {
	*fakeRadar
	lower time.Time
	upper time.Time
	err   error
}

func (f *fakeStartInterval) StartInterval() (time.Time, time.Time, error) {
	return f.lower, f.upper, f.err
}

type fakeObservedFrameStartRadar struct {
	*fakeFiniteFrameRadar
	lower time.Time
	upper time.Time
}

func (f *fakeObservedFrameStartRadar) Start(ctx context.Context) (string, error) {
	f.lower = time.Now()
	response, err := f.fakeFiniteFrameRadar.fakeRadar.Start(ctx)
	f.upper = time.Now()
	return response, err
}

func (f *fakeObservedFrameStartRadar) StartInterval() (time.Time, time.Time, error) {
	return f.lower, f.upper, nil
}

func (f *fakeFiniteFrameRadar) AwaitEnd(ctx context.Context) (string, error) {
	*f.events = append(*f.events, "frameEnd")
	f.awaitCalls++
	if f.awaitHook != nil {
		return "", f.awaitHook(ctx)
	}
	return "finite frame ended", nil
}

type fakeDCA struct {
	events        *[]string
	startHook     func(context.Context) (dca.Response, error)
	stopHook      func(context.Context, int) (dca.Response, error)
	drainStatuses []dca.Response
	stopCalls     int
}

func (f *fakeDCA) Ping(context.Context) (dca.Response, error) {
	*f.events = append(*f.events, "dcaPing")
	return dca.Response{Command: dca.CommandSystemAlive}, nil
}
func (f *fakeDCA) Execute(_ context.Context, command dca.Command, _ []byte) (dca.Response, error) {
	*f.events = append(*f.events, command.String())
	return dca.Response{Command: command}, nil
}
func (f *fakeDCA) Configure(context.Context, dca.FPGAConfig, int) (dca.ConfigurationResponses, error) {
	*f.events = append(*f.events, "dcaConfigure")
	return dca.ConfigurationResponses{}, nil
}
func (f *fakeDCA) Start(ctx context.Context) (dca.Response, error) {
	*f.events = append(*f.events, "dcaStart")
	if f.startHook != nil {
		return f.startHook(ctx)
	}
	return dca.Response{Command: dca.CommandStartRecord}, nil
}
func (f *fakeDCA) Stop(ctx context.Context) (dca.Response, error) {
	*f.events = append(*f.events, "dcaStop")
	f.stopCalls++
	if f.stopHook != nil {
		return f.stopHook(ctx, f.stopCalls)
	}
	return dca.Response{Command: dca.CommandStopRecord}, nil
}
func (f *fakeDCA) TakeStatuses() []dca.Response { return nil }
func (f *fakeDCA) DrainStatuses(context.Context, time.Duration) ([]dca.Response, error) {
	*f.events = append(*f.events, "dcaDrain")
	return append([]dca.Response(nil), f.drainStatuses...), nil
}

type fakeReceiver struct {
	events    *[]string
	stats     dca.CaptureStats
	payload   []byte
	waitHook  func(context.Context) (dca.CaptureStats, error)
	closeHook func()
	closeErr  error
}

type fakeParticipant struct {
	events    *[]string
	armErr    error
	startErr  error
	finishErr error
	finished  []bool
	windows   []radarStartWindow
}

type silentParticipant struct{}

func (*silentParticipant) Arm(context.Context) error          { return nil }
func (*silentParticipant) Start(context.Context) error        { return nil }
func (*silentParticipant) Finish(context.Context, bool) error { return nil }
func (*silentParticipant) SetRadarStart(time.Time, time.Time) {}

type radarStartWindow struct {
	lower time.Time
	upper time.Time
}

type fakeObservedParticipant struct {
	*fakeParticipant
	windows []radarStartWindow
}

func (f *fakeObservedParticipant) SetRadarStart(lower, upper time.Time) {
	*f.events = append(*f.events, "participantSetRadarStart")
	f.windows = append(f.windows, radarStartWindow{lower: lower, upper: upper})
}

func (f *fakeParticipant) Arm(context.Context) error {
	*f.events = append(*f.events, "participantArm")
	return f.armErr
}

func (f *fakeParticipant) Start(context.Context) error {
	*f.events = append(*f.events, "participantStart")
	return f.startErr
}

func (f *fakeParticipant) Finish(_ context.Context, complete bool) error {
	*f.events = append(*f.events, fmt.Sprintf("participantFinish:%t", complete))
	f.finished = append(f.finished, complete)
	return f.finishErr
}

func (f *fakeParticipant) SetRadarStart(lower, upper time.Time) {
	f.windows = append(f.windows, radarStartWindow{lower: lower, upper: upper})
}

func preparedSession(t *testing.T, plan radar.Plan) Prepared {
	t.Helper()
	prepared, err := Prepare(
		plan,
		dca.DefaultFPGAConfig(),
		dca.DefaultReceiverConfig(),
		25,
		3*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	prepared.Participant = &silentParticipant{}
	return prepared
}

func TestPrepareStreamKeepsReceiverUnboundedAndStrict(t *testing.T) {
	plan := streamTestPlan(t)
	prepared, err := PrepareStream(
		plan,
		dca.DefaultFPGAConfig(),
		dca.DefaultReceiverConfig(),
		25,
		3*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.streaming || prepared.receiver.ExpectedOutputBytes != 0 ||
		prepared.receiver.MaxOutputBytes != math.MaxInt64 || !prepared.receiver.RejectMalformed ||
		!prepared.receiver.RequireFreshStart ||
		prepared.receiver.CadenceFrameBytes != 0 || prepared.receiver.CadenceFramePeriod != 0 {
		t.Fatalf("stream receiver config = %+v", prepared.receiver)
	}
	if _, err := PrepareStream(
		sessionTestPlan(t),
		dca.DefaultFPGAConfig(),
		dca.DefaultReceiverConfig(),
		25,
		3*time.Second,
	); err == nil {
		t.Fatal("PrepareStream accepted a finite plan")
	}
}

func TestStreamRunsUntilCancellationAndUsesExplicitRadarStop(t *testing.T) {
	plan := streamTestPlan(t)
	prepared, err := PrepareStream(
		plan,
		dca.DefaultFPGAConfig(),
		dca.DefaultReceiverConfig(),
		25,
		3*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	prepared.timings.drain = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := []string{}
	prepared.Log = func(message string) {
		if message == "radar started" {
			events = append(events, "radarStartedLog")
		}
	}
	waits := 0
	now := time.Now()
	receiver := &fakeReceiver{
		events: &events,
		stats: dca.CaptureStats{
			PacketsReceived: 1, OutputBytes: 3, FirstPacketAt: now, LastPacketAt: now,
		},
	}
	receiver.waitHook = func(context.Context) (dca.CaptureStats, error) {
		waits++
		if waits == 1 {
			cancel()
			return receiver.stats, context.Canceled
		}
		return receiver.stats, nil
	}
	radarControl := &fakeRadar{events: &events}
	dcaControl := &fakeDCA{events: &events}
	output, err := os.CreateTemp(t.TempDir(), "stream-*.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	_, err = Stream(
		ctx,
		radarControl,
		dcaControl,
		func(dca.ReceiverConfig) (Receiver, error) { return receiver, nil },
		plan,
		output,
		prepared,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Stream error = %v, want cancellation", err)
	}
	if radarControl.stopCalls != 2 || radarControl.awaitCalls != 0 {
		t.Fatalf("radar cleanup calls: stop=%d await=%d", radarControl.stopCalls, radarControl.awaitCalls)
	}
	if dcaControl.stopCalls != 2 {
		t.Fatalf("DCA stop calls = %d, want initial and cleanup stops", dcaControl.stopCalls)
	}
	want := []string{
		"version", "sensorStop", "dcaStop", "dcaConfigure", "apply",
		"receiverStart", "dcaStart", "sensorStart", "radarStartedLog", "receiverFirst", "receiverWait",
		"sensorStop", "receiverWait", "dcaStop", "dcaDrain", "receiverClose",
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v\nwant   = %#v", events, want)
	}
}

func TestStreamRejectsUnexpectedReceiverEnd(t *testing.T) {
	plan := streamTestPlan(t)
	prepared, err := PrepareStream(
		plan,
		dca.DefaultFPGAConfig(),
		dca.DefaultReceiverConfig(),
		25,
		3*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	events := []string{}
	now := time.Now()
	receiver := &fakeReceiver{
		events: &events,
		stats: dca.CaptureStats{
			PacketsReceived: 1, OutputBytes: 3, FirstPacketAt: now, LastPacketAt: now,
		},
	}
	output, err := os.CreateTemp(t.TempDir(), "ended-stream-*.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	_, err = Stream(
		context.Background(),
		&fakeRadar{events: &events},
		&fakeDCA{events: &events},
		func(dca.ReceiverConfig) (Receiver, error) { return receiver, nil },
		plan,
		output,
		prepared,
	)
	if err == nil || !strings.Contains(err.Error(), "ended unexpectedly") {
		t.Fatalf("Stream error = %v", err)
	}
}

func (f *fakeReceiver) Start(_ context.Context, output io.WriterAt) error {
	*f.events = append(*f.events, "receiverStart")
	payload := f.payload
	if payload == nil {
		payload = []byte("adc")
	}
	_, err := output.WriteAt(payload, 0)
	return err
}
func (f *fakeReceiver) WaitFirst(context.Context) error {
	*f.events = append(*f.events, "receiverFirst")
	if f.stats.PacketsReceived > 0 && f.stats.CadenceAnchorAt.IsZero() {
		now := time.Now()
		f.stats.FirstPacketAt = now
		f.stats.LastPacketAt = now
		f.stats.EarliestImpliedStartAt = now
		f.stats.CadenceAnchorAt = now
		f.stats.CadenceAnchorEndOffset = f.stats.OutputBytes
		f.stats.CadenceAnchorFrame = 0
	}
	return nil
}
func (f *fakeReceiver) Wait(ctx context.Context) (dca.CaptureStats, error) {
	*f.events = append(*f.events, "receiverWait")
	if f.waitHook != nil {
		return f.waitHook(ctx)
	}
	return f.stats, nil
}
func (f *fakeReceiver) Stats() dca.CaptureStats { return f.stats }
func (f *fakeReceiver) Close() error {
	*f.events = append(*f.events, "receiverClose")
	if f.closeHook != nil {
		f.closeHook()
	}
	return f.closeErr
}

type sessionFrameSinkFunc func(context.Context, uint64, []byte) error

func (function sessionFrameSinkFunc) WriteFrame(
	ctx context.Context,
	index uint64,
	payload []byte,
) error {
	return function(ctx, index, payload)
}

func TestRunRejectsTypedNilOutputBeforeHardware(t *testing.T) {
	events := []string{}
	plan := sessionTestPlan(t)
	var output *capturefile.File
	_, err := Run(
		context.Background(),
		&fakeRadar{events: &events},
		&fakeDCA{events: &events},
		func(dca.ReceiverConfig) (Receiver, error) {
			t.Fatal("receiver factory called for unusable output")
			return nil, nil
		},
		plan,
		output,
		preparedSession(t, plan),
	)
	if err == nil || !strings.Contains(err.Error(), "dependencies are incomplete") {
		t.Fatalf("Run error = %v, want incomplete dependencies", err)
	}
	if len(events) != 0 {
		t.Fatalf("hardware events = %v, want none", events)
	}
}

func TestParticipantRunsInsideCaptureLifecycle(t *testing.T) {
	plan := sessionTestPlan(t)
	finalPath := filepath.Join(t.TempDir(), "participant.bin")
	output, err := capturefile.Create(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	events := []string{}
	participant := &fakeObservedParticipant{fakeParticipant: &fakeParticipant{events: &events}}
	receiver := &fakeReceiver{
		events: &events,
		stats: dca.CaptureStats{
			PacketsReceived: 1,
			OutputBytes:     plan.ExpectedBytes,
		},
	}
	options := preparedSession(t, plan)
	options.Participant = participant
	options.Log = func(message string) {
		if message == "radar started" {
			events = append(events, "radarStartedLog")
		}
	}

	_, err = Run(
		context.Background(),
		&fakeFiniteFrameRadar{fakeRadar: &fakeRadar{events: &events}},
		&fakeDCA{events: &events},
		func(dca.ReceiverConfig) (Receiver, error) {
			return receiver, nil
		},
		plan,
		output,
		options,
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"version", "sensorStop", "dcaStop", "dcaConfigure", "apply",
		"participantArm", "receiverStart", "dcaStart", "participantStart", "sensorStart", "radarStartedLog",
		"receiverFirst", "participantSetRadarStart", "receiverWait", "frameEnd", "receiverWait", "dcaStop", "dcaDrain",
		"receiverClose", "participantFinish:true",
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v\nwant   = %#v", events, want)
	}
	if !reflect.DeepEqual(participant.finished, []bool{true}) {
		t.Fatalf("participant outcomes = %v, want [true]", participant.finished)
	}
	if len(participant.windows) != 1 || participant.windows[0].upper.Before(participant.windows[0].lower) {
		t.Fatalf("radar start windows = %#v", participant.windows)
	}
	if !participant.windows[0].upper.Equal(receiver.stats.FirstPacketAt) {
		t.Fatalf("observer upper = %v, receiver FirstPacketAt = %v", participant.windows[0].upper, receiver.stats.FirstPacketAt)
	}
	if _, err := os.Stat(finalPath); err != nil {
		t.Fatalf("participant capture was not committed: %v", err)
	}
}

func TestParticipantUsesControllerObservedStartInterval(t *testing.T) {
	plan := sessionTestPlan(t)
	output, err := capturefile.Create(filepath.Join(t.TempDir(), "participant-event.bin"))
	if err != nil {
		t.Fatal(err)
	}
	events := []string{}
	participant := &fakeObservedParticipant{fakeParticipant: &fakeParticipant{events: &events}}
	radarControl := &fakeObservedFrameStartRadar{fakeFiniteFrameRadar: &fakeFiniteFrameRadar{
		fakeRadar: &fakeRadar{events: &events},
	}}
	receiver := &fakeReceiver{
		events: &events,
		stats: dca.CaptureStats{
			PacketsReceived: 1,
			OutputBytes:     plan.ExpectedBytes,
		},
	}
	options := preparedSession(t, plan)
	options.Participant = participant

	if _, err := Run(
		context.Background(),
		radarControl,
		&fakeDCA{events: &events},
		func(dca.ReceiverConfig) (Receiver, error) { return receiver, nil },
		plan,
		output,
		options,
	); err != nil {
		t.Fatal(err)
	}
	if len(participant.windows) != 1 ||
		!participant.windows[0].lower.Equal(radarControl.lower) ||
		!participant.windows[0].upper.Equal(radarControl.upper) {
		t.Fatalf(
			"participant frame-start window = %#v, controller = [%v,%v]",
			participant.windows,
			radarControl.lower,
			radarControl.upper,
		)
	}
	if participant.windows[0].upper.After(receiver.stats.FirstPacketAt) {
		t.Fatalf(
			"controller event did not tighten first-packet upper bound: event=%v packet=%v",
			participant.windows[0].upper,
			receiver.stats.FirstPacketAt,
		)
	}
}

func TestResolveStartIntervalUsesControllerEventIntersection(t *testing.T) {
	events := []string{}
	commandLower := time.Now()
	eventLower := commandLower.Add(time.Millisecond)
	eventUpper := commandLower.Add(2 * time.Millisecond)
	firstPacketUpper := commandLower.Add(3 * time.Millisecond)
	radarControl := &fakeStartInterval{
		fakeRadar: &fakeRadar{events: &events},
		lower:     eventLower,
		upper:     eventUpper,
	}
	lower, upper, err := resolveStartInterval(
		radarControl,
		commandLower,
		firstPacketUpper,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !lower.Equal(eventLower) || !upper.Equal(eventUpper) {
		t.Fatalf("resolved frame-start interval = [%v,%v]", lower, upper)
	}
}

func TestResolveStartIntervalRejectsDisjointEvidence(t *testing.T) {
	events := []string{}
	commandLower := time.Now()
	firstPacketUpper := commandLower.Add(time.Millisecond)
	radarControl := &fakeStartInterval{
		fakeRadar: &fakeRadar{events: &events},
		lower:     firstPacketUpper.Add(time.Millisecond),
		upper:     firstPacketUpper.Add(2 * time.Millisecond),
	}
	if _, _, err := resolveStartInterval(
		radarControl,
		commandLower,
		firstPacketUpper,
	); err == nil || !strings.Contains(err.Error(), "do not overlap") {
		t.Fatalf("disjoint frame-start interval error = %v", err)
	}
}

func TestRadarStartFailureDoesNotNotifyParticipant(t *testing.T) {
	plan := sessionTestPlan(t)
	finalPath := filepath.Join(t.TempDir(), "radar-start-failure.bin")
	output, err := capturefile.Create(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	events := []string{}
	wantErr := errors.New("radar start failed")
	participant := &fakeObservedParticipant{fakeParticipant: &fakeParticipant{events: &events}}
	options := preparedSession(t, plan)
	options.Participant = participant

	_, err = Run(
		context.Background(),
		&fakeRadar{events: &events, startErr: wantErr},
		&fakeDCA{events: &events},
		func(dca.ReceiverConfig) (Receiver, error) {
			return &fakeReceiver{events: &events}, nil
		},
		plan,
		output,
		options,
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run error = %v, want %v", err, wantErr)
	}
	if len(participant.windows) != 0 || containsEvent(events, "participantSetRadarStart") {
		t.Fatalf("observer notified after failed radar start: windows=%v events=%v", participant.windows, events)
	}
	if !containsEvent(events, "sensorStart") {
		t.Fatalf("radar start was not attempted: %v", events)
	}
}

func TestParticipantStartFailurePreventsRadarStart(t *testing.T) {
	plan := sessionTestPlan(t)
	finalPath := filepath.Join(t.TempDir(), "participant-start.bin")
	output, err := capturefile.Create(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	events := []string{}
	wantErr := errors.New("producer start failed")
	participant := &fakeParticipant{events: &events, startErr: wantErr}
	options := preparedSession(t, plan)
	options.Participant = participant

	_, err = Run(
		context.Background(),
		&fakeRadar{events: &events},
		&fakeDCA{events: &events},
		func(dca.ReceiverConfig) (Receiver, error) {
			return &fakeReceiver{events: &events}, nil
		},
		plan,
		output,
		options,
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run error = %v, want %v", err, wantErr)
	}
	if containsEvent(events, "sensorStart") {
		t.Fatalf("radar started after participant failure: %v", events)
	}
	if !reflect.DeepEqual(participant.finished, []bool{false}) {
		t.Fatalf("participant outcomes = %v, want [false]", participant.finished)
	}
	assertADCRetained(t, finalPath)
}

func TestParticipantFinishFailurePreventsCommit(t *testing.T) {
	plan := sessionTestPlan(t)
	finalPath := filepath.Join(t.TempDir(), "participant-finish.bin")
	output, err := capturefile.Create(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	events := []string{}
	wantErr := errors.New("producer END missing")
	participant := &fakeParticipant{events: &events, finishErr: wantErr}
	options := preparedSession(t, plan)
	options.Participant = participant

	_, err = Run(
		context.Background(),
		&fakeFiniteFrameRadar{fakeRadar: &fakeRadar{events: &events}},
		&fakeDCA{events: &events},
		func(dca.ReceiverConfig) (Receiver, error) {
			return &fakeReceiver{
				events: &events,
				stats:  dca.CaptureStats{PacketsReceived: 1, OutputBytes: plan.ExpectedBytes},
			}, nil
		},
		plan,
		output,
		options,
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run error = %v, want %v", err, wantErr)
	}
	if !reflect.DeepEqual(participant.finished, []bool{true}) {
		t.Fatalf("participant outcomes = %v, want [true]", participant.finished)
	}
	assertADCRetained(t, finalPath)
}

func TestFiniteCaptureUsesNaturalFrameEndCapability(t *testing.T) {
	plan := sessionTestPlan(t)
	finalPath := filepath.Join(t.TempDir(), "finite-natural-end.bin")
	output, err := capturefile.Create(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	events := []string{}
	radarControl := &fakeFiniteFrameRadar{fakeRadar: &fakeRadar{events: &events}}
	receiver := &fakeReceiver{
		events: &events,
		stats: dca.CaptureStats{
			PacketsReceived:      1,
			PayloadBytesReceived: 3,
			OutputBytes:          plan.ExpectedBytes,
		},
	}

	_, err = Run(
		context.Background(),
		radarControl,
		&fakeDCA{events: &events},
		func(dca.ReceiverConfig) (Receiver, error) { return receiver, nil },
		plan,
		output,
		preparedSession(t, plan),
	)
	if err != nil {
		t.Fatal(err)
	}
	if radarControl.stopCalls != 1 || radarControl.awaitCalls != 1 {
		t.Fatalf(
			"radar cleanup calls: stop=%d await=%d, want initial stop=1 and await=1",
			radarControl.stopCalls,
			radarControl.awaitCalls,
		)
	}
	want := []string{
		"version", "sensorStop", "dcaStop", "dcaConfigure", "apply",
		"receiverStart", "dcaStart", "sensorStart", "receiverFirst", "receiverWait",
		"frameEnd", "receiverWait", "dcaStop", "dcaDrain", "receiverClose",
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v\nwant   = %#v", events, want)
	}
	if _, err := os.Stat(finalPath); err != nil {
		t.Fatalf("finite capture output was not committed: %v", err)
	}
}

func TestFiniteCaptureWithMissingCoverageUsesNaturalFrameEndAndFails(t *testing.T) {
	plan := sessionTestPlan(t)
	finalPath := filepath.Join(t.TempDir(), "finite-hole-natural-end.bin")
	output, err := capturefile.Create(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	events := []string{}
	radarControl := &fakeFiniteFrameRadar{fakeRadar: &fakeRadar{events: &events}}
	receiver := &fakeReceiver{
		events: &events,
		stats: dca.CaptureStats{
			PacketsReceived:      1,
			PayloadBytesReceived: 1,
			OutputBytes:          plan.ExpectedBytes,
			MissingBytes:         2,
		},
	}

	_, err = Run(
		context.Background(),
		radarControl,
		&fakeDCA{events: &events},
		func(dca.ReceiverConfig) (Receiver, error) { return receiver, nil },
		plan,
		output,
		preparedSession(t, plan),
	)
	if err == nil || !strings.Contains(err.Error(), "DCA1000 capture is incomplete: missingBytes=2") {
		t.Fatalf("Run error = %v, want incomplete coverage", err)
	}
	if radarControl.stopCalls != 1 || radarControl.awaitCalls != 1 {
		t.Fatalf(
			"radar cleanup calls: stop=%d await=%d, want initial stop=1 and await=1",
			radarControl.stopCalls,
			radarControl.awaitCalls,
		)
	}
	assertADCRetained(t, finalPath)
}

func TestFiniteCaptureErrorStillUsesExplicitStop(t *testing.T) {
	plan := sessionTestPlan(t)
	finalPath := filepath.Join(t.TempDir(), "finite-error-stop.bin")
	output, err := capturefile.Create(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	events := []string{}
	radarControl := &fakeFiniteFrameRadar{fakeRadar: &fakeRadar{events: &events}}
	receiver := &fakeReceiver{
		events: &events,
		stats: dca.CaptureStats{
			PacketsReceived:      1,
			PayloadBytesReceived: 3,
			OutputBytes:          plan.ExpectedBytes,
		},
	}
	waitCalls := 0
	receiver.waitHook = func(context.Context) (dca.CaptureStats, error) {
		waitCalls++
		if waitCalls == 1 {
			return receiver.stats, context.DeadlineExceeded
		}
		return receiver.stats, nil
	}

	_, err = Run(
		context.Background(),
		radarControl,
		&fakeDCA{events: &events},
		func(dca.ReceiverConfig) (Receiver, error) { return receiver, nil },
		plan,
		output,
		preparedSession(t, plan),
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run error = %v, want finite capture deadline", err)
	}
	if radarControl.stopCalls != 2 || radarControl.awaitCalls != 0 {
		t.Fatalf(
			"radar cleanup calls: stop=%d await=%d, want explicit cleanup stop and no await",
			radarControl.stopCalls,
			radarControl.awaitCalls,
		)
	}
	assertADCRetained(t, finalPath)
}

func TestNaturalFrameEndFailureDoesNotStopAgain(t *testing.T) {
	plan := sessionTestPlan(t)
	events := []string{}
	want := errors.New("unknown natural frame-end result")
	radarControl := &fakeFiniteFrameRadar{
		fakeRadar: &fakeRadar{events: &events},
		awaitHook: func(context.Context) error { return want },
	}
	dcaControl := &fakeDCA{events: &events}
	receiver := &fakeReceiver{events: &events}

	err := cleanup(
		radarControl,
		dcaControl,
		receiver,
		nil,
		true,
		true,
		true,
		true,
		true,
		preparedSession(t, plan),
		func(string) {},
	)
	if !errors.Is(err, want) {
		t.Fatalf("cleanup error = %v, want natural frame-end failure", err)
	}
	if radarControl.awaitCalls != 1 || radarControl.stopCalls != 0 {
		t.Fatalf(
			"radar cleanup calls: await=%d stop=%d, want one await and no second stop",
			radarControl.awaitCalls,
			radarControl.stopCalls,
		)
	}
	wantEvents := []string{"frameEnd", "receiverWait", "dcaStop", "dcaDrain", "receiverClose"}
	if !reflect.DeepEqual(events, wantEvents) || dcaControl.stopCalls != 1 {
		t.Fatalf("cleanup after frame-end failure: events=%v dcaStops=%d", events, dcaControl.stopCalls)
	}
}

func TestCancellationAfterDCAArmDoesNotStartRadar(t *testing.T) {
	plan := sessionTestPlan(t)
	finalPath := filepath.Join(t.TempDir(), "cancel-before-start.bin")
	output, err := capturefile.Create(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := []string{}
	dcaControl := &fakeDCA{
		events: &events,
		startHook: func(context.Context) (dca.Response, error) {
			cancel()
			return dca.Response{Command: dca.CommandStartRecord}, nil
		},
	}
	receiver := &fakeReceiver{events: &events}
	options := preparedSession(t, plan)
	options.timings.drain = 20 * time.Millisecond
	_, err = Run(
		ctx,
		&fakeRadar{events: &events},
		dcaControl,
		func(dca.ReceiverConfig) (Receiver, error) { return receiver, nil },
		plan,
		output,
		options,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context cancellation", err)
	}
	if containsEvent(events, "sensorStart") {
		t.Fatalf("radar was started after cancellation: %#v", events)
	}
	if containsEvent(events, "receiverWait") {
		t.Fatalf("cleanup waited for data although radar never started: %#v", events)
	}
	if dcaControl.stopCalls != 2 {
		t.Fatalf("DCA StopRecord calls = %d, want initial convergence plus final stop", dcaControl.stopCalls)
	}
	assertADCRetained(t, finalPath)
}

func TestCancellationDuringCleanupPreventsCommit(t *testing.T) {
	plan := sessionTestPlan(t)
	finalPath := filepath.Join(t.TempDir(), "cancel-cleanup.bin")
	output, err := capturefile.Create(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := []string{}
	now := time.Now()
	stats := dca.CaptureStats{PacketsReceived: 1, OutputBytes: 3, FirstPacketAt: now, LastPacketAt: now}
	receiver := &fakeReceiver{
		events: &events,
		stats:  stats,
		waitHook: func(context.Context) (dca.CaptureStats, error) {
			cancel()
			return stats, nil
		},
	}
	radarControl := &fakeFiniteFrameRadar{fakeRadar: &fakeRadar{events: &events}}
	_, err = Run(
		ctx,
		radarControl,
		&fakeDCA{events: &events},
		func(dca.ReceiverConfig) (Receiver, error) { return receiver, nil },
		plan,
		output,
		preparedSession(t, plan),
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want sticky cancellation", err)
	}
	if radarControl.stopCalls != 2 || radarControl.awaitCalls != 0 {
		t.Fatalf(
			"radar cleanup calls after cancellation: stop=%d await=%d",
			radarControl.stopCalls,
			radarControl.awaitCalls,
		)
	}
	assertADCRetained(t, finalPath)
}

func TestFiniteDeadlineReportsFinalCoverageAfterReceiverCleanup(t *testing.T) {
	plan := sessionTestPlan(t)
	finalPath := filepath.Join(t.TempDir(), "finite-deadline.bin")
	output, err := capturefile.Create(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	events := []string{}
	now := time.Now()
	receiver := &fakeReceiver{
		events: &events,
		stats: dca.CaptureStats{
			PacketsReceived: 1,
			OutputBytes:     plan.ExpectedBytes,
			FirstPacketAt:   now,
			LastPacketAt:    now,
		},
		waitHook: func(context.Context) (dca.CaptureStats, error) {
			return dca.CaptureStats{}, context.DeadlineExceeded
		},
	}
	receiver.closeHook = func() {
		receiver.stats.MissingBytes = 2
		receiver.stats.SequenceGaps = 1
		receiver.stats.DiscardedBeforeBasePackets = 3
	}

	gotStats, err := Run(
		context.Background(),
		&fakeRadar{events: &events},
		&fakeDCA{events: &events},
		func(dca.ReceiverConfig) (Receiver, error) { return receiver, nil },
		plan,
		output,
		preparedSession(t, plan),
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run error = %v, want deadline identity", err)
	}
	for _, diagnostic := range []string{
		"finite-frame data exceeded planned maximum duration",
		"expected=3",
		"output=3",
		"missing=2",
		"sequenceGaps=1",
		"discardedBeforeBase=3",
	} {
		if !strings.Contains(err.Error(), diagnostic) {
			t.Fatalf("Run error = %v, want diagnostic %q", err, diagnostic)
		}
	}
	if gotStats != receiver.stats {
		t.Fatalf("Run stats = %#v, want finalized %#v", gotStats, receiver.stats)
	}
	assertADCRetained(t, finalPath)
}

func TestRadarCleanupDeadlineStillStopsDCA(t *testing.T) {
	plan := sessionTestPlan(t)
	finalPath := filepath.Join(t.TempDir(), "blocked-radar-stop.bin")
	output, err := capturefile.Create(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	events := []string{}
	now := time.Now()
	receiver := &fakeReceiver{
		events: &events,
		stats:  dca.CaptureStats{PacketsReceived: 1, OutputBytes: 3, FirstPacketAt: now, LastPacketAt: now},
	}
	radarControl := &fakeRadar{
		events: &events,
		awaitHook: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}
	dcaControl := &fakeDCA{events: &events}
	options := preparedSession(t, plan)
	options.timings.radarCleanup = 20 * time.Millisecond
	started := time.Now()
	_, err = Run(
		context.Background(),
		radarControl,
		dcaControl,
		func(dca.ReceiverConfig) (Receiver, error) { return receiver, nil },
		plan,
		output,
		options,
	)
	if err == nil {
		t.Fatal("Run succeeded despite radar cleanup timeout")
	}
	if time.Since(started) > time.Second {
		t.Fatalf("radar cleanup deadline was not bounded: %s", time.Since(started))
	}
	if dcaControl.stopCalls != 2 {
		t.Fatalf("DCA StopRecord calls = %d, want final stop despite radar timeout", dcaControl.stopCalls)
	}
	assertPublishedBytes(t, finalPath, "adc")
}

func TestStartRecordFailureSkipsEmptyDataDrainAndExtraStop(t *testing.T) {
	plan := sessionTestPlan(t)
	finalPath := filepath.Join(t.TempDir(), "start-failed.bin")
	output, err := capturefile.Create(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	events := []string{}
	dcaControl := &fakeDCA{
		events: &events,
		startHook: func(context.Context) (dca.Response, error) {
			return dca.Response{}, &dca.StartConvergenceError{StartErr: errors.New("start response timeout")}
		},
	}
	_, err = Run(
		context.Background(),
		&fakeRadar{events: &events},
		dcaControl,
		func(dca.ReceiverConfig) (Receiver, error) {
			return &fakeReceiver{events: &events}, nil
		},
		plan,
		output,
		preparedSession(t, plan),
	)
	if err == nil {
		t.Fatal("Run succeeded despite StartRecord convergence failure")
	}
	if containsEvent(events, "receiverWait") {
		t.Fatalf("failed StartRecord triggered a pointless data drain: %#v", events)
	}
	if dcaControl.stopCalls != 1 {
		t.Fatalf("session retried StopRecord after Start: %d calls", dcaControl.stopCalls)
	}
	assertADCRetained(t, finalPath)
}

func TestValidateResultRejectsMissingEdgePacketByExactSize(t *testing.T) {
	plan := sessionTestPlan(t)
	now := time.Now()
	stats := dca.CaptureStats{
		PacketsReceived: 1,
		OutputBytes:     plan.ExpectedBytes - 1,
		FirstPacketAt:   now,
		LastPacketAt:    now,
	}
	err := validateResult(plan, stats, time.Second, time.Time{})
	if err == nil || !strings.Contains(err.Error(), "size mismatch") {
		t.Fatalf("validateResult error = %v, want exact-size mismatch", err)
	}
}

func TestRunRejectsCompleteSingleFramePacketReceivedBeforeStart(t *testing.T) {
	plan := sessionTestPlan(t)
	finalPath := filepath.Join(t.TempDir(), "stale-single-frame.bin")
	output, err := capturefile.Create(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	events := []string{}
	stale := time.Now().Add(-time.Second)
	receiver := &fakeReceiver{
		events: &events,
		stats: dca.CaptureStats{
			PacketsReceived:        1,
			PayloadBytesReceived:   3,
			OutputBytes:            3,
			FirstPacketAt:          stale,
			LastPacketAt:           stale,
			EarliestImpliedStartAt: stale,
			CadenceAnchorAt:        stale,
			CadenceAnchorEndOffset: 3,
			CadenceAnchorFrame:     0,
		},
	}
	radarControl := &fakeFiniteFrameRadar{fakeRadar: &fakeRadar{events: &events}}
	_, err = Run(
		context.Background(),
		radarControl,
		&fakeDCA{events: &events},
		func(dca.ReceiverConfig) (Receiver, error) { return receiver, nil },
		plan,
		output,
		preparedSession(t, plan),
	)
	if err == nil || !strings.Contains(err.Error(), "arrived too early") {
		t.Fatalf("Run error = %v, want stale pre-start packet failure", err)
	}
	if radarControl.stopCalls != 2 || radarControl.awaitCalls != 0 {
		t.Fatalf(
			"radar cleanup calls after data validation failure: stop=%d await=%d",
			radarControl.stopCalls,
			radarControl.awaitCalls,
		)
	}
	assertADCRetained(t, finalPath)
}

func TestRunRejectsFatalAsyncStatusDrainedAfterStop(t *testing.T) {
	plan := sessionTestPlan(t)
	finalPath := filepath.Join(t.TempDir(), "fatal-async.bin")
	output, err := capturefile.Create(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	events := []string{}
	receiver := &fakeReceiver{
		events: &events,
		stats: dca.CaptureStats{
			PacketsReceived:      1,
			PayloadBytesReceived: 3,
			OutputBytes:          3,
		},
	}
	dcaControl := &fakeDCA{
		events: &events,
		drainStatuses: []dca.Response{{
			Command: dca.CommandAsyncStatus,
			Status:  dca.SystemStatusDDRFull,
		}},
	}
	_, err = Run(
		context.Background(),
		&fakeRadar{events: &events},
		dcaControl,
		func(dca.ReceiverConfig) (Receiver, error) { return receiver, nil },
		plan,
		output,
		preparedSession(t, plan),
	)
	if err == nil || !strings.Contains(err.Error(), "fatal async status") {
		t.Fatalf("Run error = %v, want fatal async cleanup failure", err)
	}
	assertPublishedBytes(t, finalPath, "adc")
}

func TestValidateResultUsesPacketOffsetsAcrossDCAAggregation(t *testing.T) {
	issued := time.Now()
	tests := []struct {
		name          string
		plan          radar.Plan
		anchorEnd     int64
		anchorFrame   uint64
		anchorDelay   time.Duration
		tailDelay     time.Duration
		wantEarlyFail bool
	}{
		{
			name: "baseline cadence",
			plan: radar.Plan{
				BytesPerFrame:  262_144,
				ExpectedBytes:  26_214_400,
				NumberOfFrames: 100,
				FramePeriod:    100 * time.Millisecond,
			},
			anchorEnd:   26_213_824,
			anchorFrame: 99,
			anchorDelay: 9900 * time.Millisecond,
			tailDelay:   11900 * time.Millisecond,
		},
		{
			name: "baseline cadence compressed",
			plan: radar.Plan{
				BytesPerFrame:  262_144,
				ExpectedBytes:  26_214_400,
				NumberOfFrames: 100,
				FramePeriod:    100 * time.Millisecond,
			},
			anchorEnd:     26_213_824,
			anchorFrame:   99,
			anchorDelay:   8 * time.Second,
			tailDelay:     10 * time.Second,
			wantEarlyFail: true,
		},
		{
			name: "small frames aggregated into full packets",
			plan: radar.Plan{
				BytesPerFrame:  64,
				ExpectedBytes:  6400,
				NumberOfFrames: 100,
				FramePeriod:    100 * time.Millisecond,
			},
			anchorEnd:   5824,
			anchorFrame: 90,
			anchorDelay: 9 * time.Second,
			tailDelay:   11900 * time.Millisecond,
		},
		{
			name: "small frames compressed despite delayed tail",
			plan: radar.Plan{
				BytesPerFrame:  64,
				ExpectedBytes:  6400,
				NumberOfFrames: 100,
				FramePeriod:    100 * time.Millisecond,
			},
			anchorEnd:     5824,
			anchorFrame:   90,
			anchorDelay:   7200 * time.Millisecond,
			tailDelay:     9200 * time.Millisecond,
			wantEarlyFail: true,
		},
		{
			name: "only a delayed short tail is conservatively accepted",
			plan: radar.Plan{
				BytesPerFrame:  64,
				ExpectedBytes:  640,
				NumberOfFrames: 10,
				FramePeriod:    100 * time.Millisecond,
			},
			anchorEnd:   640,
			anchorFrame: 9,
			anchorDelay: 2900 * time.Millisecond,
			tailDelay:   2900 * time.Millisecond,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			anchorAt := issued.Add(test.anchorDelay)
			frameOffset, err := frameOffsetDuration(test.plan.FramePeriod, test.anchorFrame)
			if err != nil {
				t.Fatal(err)
			}
			stats := dca.CaptureStats{
				PacketsReceived:        1,
				OutputBytes:            test.plan.ExpectedBytes,
				FirstPacketAt:          issued.Add(time.Millisecond),
				LastPacketAt:           issued.Add(test.tailDelay),
				EarliestImpliedStartAt: anchorAt.Add(-frameOffset),
				CadenceAnchorAt:        anchorAt,
				CadenceAnchorEndOffset: test.anchorEnd,
				CadenceAnchorFrame:     test.anchorFrame,
			}
			err = validateResult(test.plan, stats, dca.RawModeTailFlushGuard, issued)
			if test.wantEarlyFail {
				if err == nil || !strings.Contains(err.Error(), "arrived too early") {
					t.Fatalf("validateResult error = %v, want early cadence failure", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("valid packet cadence was rejected: %v", err)
			}
		})
	}
}

func TestMinimumFirstPacketTimeoutAccountsForAggregationAndTail(t *testing.T) {
	tests := []struct {
		name string
		plan radar.Plan
		want time.Duration
	}{
		{
			name: "small frames aggregate beyond default",
			plan: radar.Plan{
				BytesPerFrame:  64,
				ExpectedBytes:  64_000,
				NumberOfFrames: 1000,
				FramePeriod:    1342 * time.Millisecond,
			},
			want: 31866 * time.Millisecond,
		},
		{
			name: "finite output is only a delayed short tail",
			plan: radar.Plan{
				BytesPerFrame:  64,
				ExpectedBytes:  640,
				NumberOfFrames: 10,
				FramePeriod:    100 * time.Millisecond,
			},
			want: 4500 * time.Millisecond,
		},
		{
			name: "baseline packet in first frame",
			plan: radar.Plan{
				BytesPerFrame:  262_144,
				ExpectedBytes:  26_214_400,
				NumberOfFrames: 100,
				FramePeriod:    100 * time.Millisecond,
			},
			want: 1100 * time.Millisecond,
		},
		{
			name: "first payload may require the whole selected frame",
			plan: radar.Plan{
				BytesPerFrame:  dca.MaximumDataPayloadSize,
				ExpectedBytes:  10 * int64(dca.MaximumDataPayloadSize),
				NumberOfFrames: 10,
				FramePeriod:    1342 * time.Millisecond,
			},
			want: 2342 * time.Millisecond,
		},
		{
			name: "single short frame waits for tail without a frame offset",
			plan: radar.Plan{
				BytesPerFrame:  64,
				ExpectedBytes:  64,
				NumberOfFrames: 1,
				FramePeriod:    time.Second,
			},
			want: 4500 * time.Millisecond,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := MinimumFirstPacketTimeout(test.plan)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("MinimumFirstPacketTimeout = %s, want %s", got, test.want)
			}
		})
	}
}

func TestMinimumReceiverIdleTimeoutAccountsForPacketAggregation(t *testing.T) {
	tests := []struct {
		name string
		plan radar.Plan
		want time.Duration
	}{
		{
			name: "small frames",
			plan: radar.Plan{
				BytesPerFrame:  64,
				ExpectedBytes:  64_000,
				NumberOfFrames: 1000,
				FramePeriod:    200 * time.Millisecond,
			},
			want: 7400 * time.Millisecond,
		},
		{
			name: "one payload per frame remains tail bounded",
			plan: radar.Plan{
				BytesPerFrame:  dca.MaximumDataPayloadSize,
				ExpectedBytes:  10 * int64(dca.MaximumDataPayloadSize),
				NumberOfFrames: 10,
				FramePeriod:    100 * time.Millisecond,
			},
			want: dca.RawModeTailFlushGuard,
		},
		{
			name: "finite one small frame still watches for aggregated overflow",
			plan: radar.Plan{
				BytesPerFrame:  64,
				ExpectedBytes:  64,
				NumberOfFrames: 1,
				FramePeriod:    time.Second,
			},
			want: 25 * time.Second,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := MinimumReceiverIdleTimeout(test.plan)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("MinimumReceiverIdleTimeout = %s, want %s", got, test.want)
			}
		})
	}

	overflow := radar.Plan{
		BytesPerFrame:  64,
		ExpectedBytes:  128,
		NumberOfFrames: 2,
		FramePeriod:    time.Duration(math.MaxInt64 / 2),
	}
	if _, err := MinimumReceiverIdleTimeout(overflow); err == nil {
		t.Fatal("MinimumReceiverIdleTimeout accepted an overflowing packet gap")
	}
}

func TestFrameOffsetDurationBoundary(t *testing.T) {
	maximumFrame := uint64(math.MaxInt64 / int64(time.Second))
	if _, err := frameOffsetDuration(time.Second, maximumFrame); err != nil {
		t.Fatalf("largest representable frame offset was rejected: %v", err)
	}
	if _, err := frameOffsetDuration(time.Second, maximumFrame+1); err == nil {
		t.Fatal("overflowing frame offset was accepted")
	}
}

func TestPrepareRejectsMaximumDurationOverflow(t *testing.T) {
	plan := sessionTestPlan(t)
	plan.BytesPerFrame = dca.MaximumDataPayloadSize
	plan.ExpectedBytes = 2 * int64(dca.MaximumDataPayloadSize)
	plan.NumberOfFrames = 2
	plan.FramePeriod = time.Duration(math.MaxInt64 / 2)
	_, err := Prepare(plan, dca.DefaultFPGAConfig(), dca.DefaultReceiverConfig(), 25, 3*time.Second)
	if err == nil || !strings.Contains(err.Error(), "maximum") {
		t.Fatalf("Prepare error = %v, want maximum-duration failure", err)
	}
}

func TestPrepareRejectsUnsupportedDCAConfig(t *testing.T) {
	plan := sessionTestPlan(t)
	fpga := dca.DefaultFPGAConfig()
	fpga.Timer = 31
	_, err := Prepare(plan, fpga, dca.DefaultReceiverConfig(), 25, 3*time.Second)
	if err == nil || !strings.Contains(err.Error(), "raw capture requires") {
		t.Fatalf("Prepare error = %v, want raw DCA contract failure", err)
	}
}

func sessionTestPlan(t *testing.T) radar.Plan {
	t.Helper()
	commands := []string{
		"flushCfg",
		"dfeDataOutputMode 1",
		"channelCfg 15 7 0",
		"adcCfg 2 1",
		"adcbufCfg -1 0 1 1 1",
		"profileCfg 0 60 7 3 40 0 0 100 1 256 5000 0 0 30",
		"chirpCfg 0 0 0 0 0 0 0 1",
		"frameCfg 0 0 1 1 10 1 0",
		"lowPower 0 0",
		"lvdsStreamCfg -1 0 1 0",
		"sensorStart",
	}
	plan, err := radar.CommandPlan(commands)
	if err != nil {
		t.Fatal(err)
	}
	plan.BytesPerFrame = 3
	plan.ExpectedBytes = 3
	return plan
}

func streamTestPlan(t *testing.T) radar.Plan {
	plan := sessionTestPlan(t)
	plan.NumberOfFrames = 0
	plan.ExpectedBytes = 0
	return plan
}

func assertPublishedBytes(t *testing.T, finalPath, want string) {
	t.Helper()
	got, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("published bytes = %q, want %q", got, want)
	}
}

func containsEvent(events []string, wanted string) bool {
	for _, event := range events {
		if event == wanted {
			return true
		}
	}
	return false
}

func assertADCRetained(t *testing.T, finalPath string) {
	t.Helper()
	if _, err := os.Stat(finalPath); err != nil {
		t.Fatalf("ADC file was not retained: %v", err)
	}
}
