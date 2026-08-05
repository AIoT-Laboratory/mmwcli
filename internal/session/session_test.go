package session

import (
	"context"
	"errors"
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
	events    *[]string
	stopCalls int
	stopHook  func(context.Context, int) error
}

func (f *fakeRadar) VerifyPlatform() (string, error) {
	*f.events = append(*f.events, "version")
	return "Platform : xWR68xx\nDone\n", nil
}
func (f *fakeRadar) VerifyPlatformContext(context.Context) (string, error) {
	return f.VerifyPlatform()
}
func (f *fakeRadar) Stop() (string, error) {
	*f.events = append(*f.events, "sensorStop")
	return "Done\n", nil
}
func (f *fakeRadar) StopContext(ctx context.Context) (string, error) {
	*f.events = append(*f.events, "sensorStop")
	f.stopCalls++
	if f.stopHook != nil {
		return "", f.stopHook(ctx, f.stopCalls)
	}
	return "Done\n", nil
}
func (f *fakeRadar) Apply(radar.CapturePlan) error {
	*f.events = append(*f.events, "apply")
	return nil
}
func (f *fakeRadar) ApplyContext(_ context.Context, plan radar.CapturePlan) error {
	return f.Apply(plan)
}
func (f *fakeRadar) Start() (string, error) {
	*f.events = append(*f.events, "sensorStart")
	return "Done\n", nil
}
func (f *fakeRadar) StartContext(context.Context) (string, error) { return f.Start() }
func (f *fakeRadar) StartWithoutReconfiguration() (string, error) {
	*f.events = append(*f.events, "sensorStart 0")
	return "Done\n", nil
}
func (f *fakeRadar) StartWithoutReconfigurationContext(context.Context) (string, error) {
	return f.StartWithoutReconfiguration()
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
func (f *fakeDCA) StartRecordConvergent(ctx context.Context) (dca.Response, error) {
	*f.events = append(*f.events, "dcaStart")
	if f.startHook != nil {
		return f.startHook(ctx)
	}
	return dca.Response{Command: dca.CommandStartRecord}, nil
}
func (f *fakeDCA) StopRecord(ctx context.Context) (dca.Response, error) {
	*f.events = append(*f.events, "dcaStop")
	f.stopCalls++
	if f.stopHook != nil {
		return f.stopHook(ctx, f.stopCalls)
	}
	return dca.Response{Command: dca.CommandStopRecord}, nil
}
func (f *fakeDCA) TakeAsyncStatuses() []dca.Response { return nil }
func (f *fakeDCA) DrainAsyncStatuses(context.Context, time.Duration) ([]dca.Response, error) {
	*f.events = append(*f.events, "dcaDrain")
	return append([]dca.Response(nil), f.drainStatuses...), nil
}

type fakeReceiver struct {
	events    *[]string
	stats     dca.CaptureStats
	waitHook  func(context.Context) (dca.CaptureStats, error)
	closeHook func()
	closeErr  error
}

func (f *fakeReceiver) Start(_ context.Context, output io.WriterAt) error {
	*f.events = append(*f.events, "receiverStart")
	_, err := output.WriteAt([]byte("adc"), 0)
	return err
}
func (f *fakeReceiver) WaitForFirst(context.Context) error {
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

func TestReuseCaptureSendsNoConfigurationAndArmsBeforeStart(t *testing.T) {
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
	plan, err := radar.BuildCapturePlan(radar.StudioCLI, commands, radar.ReuseConfiguration)
	if err != nil {
		t.Fatal(err)
	}
	plan.BytesPerFrame = 3
	plan.ExpectedBytes = 3
	output, err := capturefile.Create(filepath.Join(t.TempDir(), "capture.bin"))
	if err != nil {
		t.Fatal(err)
	}
	events := []string{}
	now := time.Now()
	receiver := &fakeReceiver{
		events: &events,
		stats: dca.CaptureStats{
			PacketsReceived: 1,
			OutputBytes:     3,
			FirstPacketAt:   now,
			LastPacketAt:    now,
		},
	}
	waitCalls := 0
	var cleanupDrainBudget time.Duration
	receiver.waitHook = func(ctx context.Context) (dca.CaptureStats, error) {
		waitCalls++
		if waitCalls == 2 {
			deadline, ok := ctx.Deadline()
			if !ok {
				return receiver.stats, errors.New("cleanup data drain has no absolute deadline")
			}
			cleanupDrainBudget = time.Until(deadline)
		}
		return receiver.stats, nil
	}
	options := DefaultOptions()
	options.ReceiverConfig.IdleTimeout = time.Millisecond
	options.DrainTimeout = time.Millisecond
	var configuredReceiver dca.ReceiverConfig
	_, err = Run(
		context.Background(),
		&fakeRadar{events: &events},
		&fakeDCA{events: &events},
		func(config dca.ReceiverConfig) (DataReceiver, error) {
			configuredReceiver = config
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
		"version", "sensorStop", "dcaStop", "dcaConfigure",
		"receiverStart", "dcaStart", "sensorStart 0", "receiverFirst", "receiverWait",
		"sensorStop", "receiverWait", "dcaStop", "dcaDrain", "receiverClose",
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v\nwant   = %#v", events, want)
	}
	if configuredReceiver.ExpectedOutputBytes != plan.ExpectedBytes {
		t.Fatalf(
			"receiver expected bytes = %d, want %d",
			configuredReceiver.ExpectedOutputBytes,
			plan.ExpectedBytes,
		)
	}
	if configuredReceiver.IdleTimeout < dca.RawModeTailFlushGuard {
		t.Fatalf("receiver idle timeout = %s, want at least %s", configuredReceiver.IdleTimeout, dca.RawModeTailFlushGuard)
	}
	if cleanupDrainBudget < dca.RawModeTailFlushGuard-100*time.Millisecond {
		t.Fatalf("cleanup drain budget = %s, want at least %s", cleanupDrainBudget, dca.RawModeTailFlushGuard)
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
	options := DefaultOptions()
	options.DrainTimeout = 20 * time.Millisecond
	_, err = Run(
		ctx,
		&fakeRadar{events: &events},
		dcaControl,
		func(dca.ReceiverConfig) (DataReceiver, error) { return receiver, nil },
		plan,
		output,
		options,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context cancellation", err)
	}
	if containsEvent(events, "sensorStart") || containsEvent(events, "sensorStart 0") {
		t.Fatalf("radar was started after cancellation: %#v", events)
	}
	if containsEvent(events, "receiverWait") {
		t.Fatalf("cleanup waited for data although radar never started: %#v", events)
	}
	if dcaControl.stopCalls != 2 {
		t.Fatalf("DCA StopRecord calls = %d, want initial convergence plus final stop", dcaControl.stopCalls)
	}
	assertPartRetained(t, finalPath)
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
	_, err = Run(
		ctx,
		&fakeRadar{events: &events},
		&fakeDCA{events: &events},
		func(dca.ReceiverConfig) (DataReceiver, error) { return receiver, nil },
		plan,
		output,
		DefaultOptions(),
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want sticky cancellation", err)
	}
	assertPartRetained(t, finalPath)
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
		func(dca.ReceiverConfig) (DataReceiver, error) { return receiver, nil },
		plan,
		output,
		DefaultOptions(),
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
	assertPartRetained(t, finalPath)
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
		stopHook: func(ctx context.Context, call int) error {
			if call == 1 {
				return nil
			}
			<-ctx.Done()
			return ctx.Err()
		},
	}
	dcaControl := &fakeDCA{events: &events}
	options := DefaultOptions()
	options.RadarCleanupTimeout = 20 * time.Millisecond
	started := time.Now()
	_, err = Run(
		context.Background(),
		radarControl,
		dcaControl,
		func(dca.ReceiverConfig) (DataReceiver, error) { return receiver, nil },
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
	assertPartRetained(t, finalPath)
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
		func(dca.ReceiverConfig) (DataReceiver, error) {
			return &fakeReceiver{events: &events}, nil
		},
		plan,
		output,
		DefaultOptions(),
	)
	if err == nil {
		t.Fatal("Run succeeded despite StartRecord convergence failure")
	}
	if containsEvent(events, "receiverWait") {
		t.Fatalf("failed StartRecord triggered a pointless data drain: %#v", events)
	}
	if dcaControl.stopCalls != 1 {
		t.Fatalf("session retried StopRecord after StartRecordConvergent: %d calls", dcaControl.stopCalls)
	}
	assertPartRetained(t, finalPath)
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
	_, err = Run(
		context.Background(),
		&fakeRadar{events: &events},
		&fakeDCA{events: &events},
		func(dca.ReceiverConfig) (DataReceiver, error) { return receiver, nil },
		plan,
		output,
		DefaultOptions(),
	)
	if err == nil || !strings.Contains(err.Error(), "arrived too early") {
		t.Fatalf("Run error = %v, want stale pre-start packet failure", err)
	}
	assertPartRetained(t, finalPath)
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
		func(dca.ReceiverConfig) (DataReceiver, error) { return receiver, nil },
		plan,
		output,
		DefaultOptions(),
	)
	if err == nil || !strings.Contains(err.Error(), "fatal async status") {
		t.Fatalf("Run error = %v, want fatal async cleanup failure", err)
	}
	assertPartRetained(t, finalPath)
}

func TestValidateResultUsesPacketOffsetsAcrossDCAAggregation(t *testing.T) {
	issued := time.Now()
	tests := []struct {
		name          string
		plan          radar.CapturePlan
		anchorEnd     int64
		anchorFrame   uint64
		anchorDelay   time.Duration
		tailDelay     time.Duration
		wantEarlyFail bool
	}{
		{
			name: "baseline cadence",
			plan: radar.CapturePlan{
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
			plan: radar.CapturePlan{
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
			plan: radar.CapturePlan{
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
			plan: radar.CapturePlan{
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
			plan: radar.CapturePlan{
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
		plan radar.CapturePlan
		want time.Duration
	}{
		{
			name: "small frames aggregate beyond default",
			plan: radar.CapturePlan{
				BytesPerFrame:  64,
				InfiniteFrames: true,
				FramePeriod:    1342 * time.Millisecond,
			},
			want: 31866 * time.Millisecond,
		},
		{
			name: "finite output is only a delayed short tail",
			plan: radar.CapturePlan{
				BytesPerFrame:  64,
				ExpectedBytes:  640,
				NumberOfFrames: 10,
				FramePeriod:    100 * time.Millisecond,
			},
			want: 4500 * time.Millisecond,
		},
		{
			name: "baseline packet in first frame",
			plan: radar.CapturePlan{
				BytesPerFrame:  262_144,
				ExpectedBytes:  26_214_400,
				NumberOfFrames: 100,
				FramePeriod:    100 * time.Millisecond,
			},
			want: 1100 * time.Millisecond,
		},
		{
			name: "first payload may require the whole selected frame",
			plan: radar.CapturePlan{
				BytesPerFrame:  dca.MaximumDataPayloadSize,
				InfiniteFrames: true,
				FramePeriod:    1342 * time.Millisecond,
			},
			want: 2342 * time.Millisecond,
		},
		{
			name: "single short frame waits for tail without a frame offset",
			plan: radar.CapturePlan{
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

func TestMinimumReceiverIdleTimeoutAccountsForInfinitePacketAggregation(t *testing.T) {
	tests := []struct {
		name string
		plan radar.CapturePlan
		want time.Duration
	}{
		{
			name: "small infinite frames",
			plan: radar.CapturePlan{
				BytesPerFrame:  64,
				InfiniteFrames: true,
				FramePeriod:    200 * time.Millisecond,
			},
			want: 7400 * time.Millisecond,
		},
		{
			name: "one payload per frame remains tail bounded",
			plan: radar.CapturePlan{
				BytesPerFrame:  dca.MaximumDataPayloadSize,
				InfiniteFrames: true,
				FramePeriod:    100 * time.Millisecond,
			},
			want: dca.RawModeTailFlushGuard,
		},
		{
			name: "finite one small frame still watches for aggregated overflow",
			plan: radar.CapturePlan{
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

	overflow := radar.CapturePlan{
		BytesPerFrame:  64,
		InfiniteFrames: true,
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

func TestRunRejectsMaximumDurationOverflowBeforeHardware(t *testing.T) {
	plan := sessionTestPlan(t)
	plan.BytesPerFrame = dca.MaximumDataPayloadSize
	plan.ExpectedBytes = 2 * int64(dca.MaximumDataPayloadSize)
	plan.NumberOfFrames = 2
	plan.FramePeriod = time.Duration(math.MaxInt64 / 2)
	output, err := capturefile.Create(filepath.Join(t.TempDir(), "overflow.bin"))
	if err != nil {
		t.Fatal(err)
	}
	events := []string{}
	receiverCreated := false
	_, err = Run(
		context.Background(),
		&fakeRadar{events: &events},
		&fakeDCA{events: &events},
		func(dca.ReceiverConfig) (DataReceiver, error) {
			receiverCreated = true
			return &fakeReceiver{events: &events}, nil
		},
		plan,
		output,
		DefaultOptions(),
	)
	if err == nil || !strings.Contains(err.Error(), "maximum") {
		t.Fatalf("Run error = %v, want maximum-duration preflight failure", err)
	}
	if receiverCreated || len(events) != 0 {
		t.Fatalf("duration preflight touched hardware dependencies: receiver=%t events=%v", receiverCreated, events)
	}
}

func TestRunRejectsUnsupportedDCAConfigBeforeHardware(t *testing.T) {
	plan := sessionTestPlan(t)
	output, err := capturefile.Create(filepath.Join(t.TempDir(), "unsupported-dca.bin"))
	if err != nil {
		t.Fatal(err)
	}
	events := []string{}
	options := DefaultOptions()
	options.FPGAConfig.Timer = 31
	_, err = Run(
		context.Background(),
		&fakeRadar{events: &events},
		&fakeDCA{events: &events},
		func(dca.ReceiverConfig) (DataReceiver, error) {
			return &fakeReceiver{events: &events}, nil
		},
		plan,
		output,
		options,
	)
	if err == nil || !strings.Contains(err.Error(), "raw capture requires") {
		t.Fatalf("Run error = %v, want raw DCA contract failure", err)
	}
	if len(events) != 0 {
		t.Fatalf("DCA contract preflight touched hardware dependencies: %v", events)
	}
}

func sessionTestPlan(t *testing.T) radar.CapturePlan {
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
	plan, err := radar.BuildCapturePlan(radar.StudioCLI, commands, radar.FullConfiguration)
	if err != nil {
		t.Fatal(err)
	}
	plan.BytesPerFrame = 3
	plan.ExpectedBytes = 3
	return plan
}

func containsEvent(events []string, wanted string) bool {
	for _, event := range events {
		if event == wanted {
			return true
		}
	}
	return false
}

func assertPartRetained(t *testing.T, finalPath string) {
	t.Helper()
	if _, err := os.Stat(finalPath); !os.IsNotExist(err) {
		t.Fatalf("final output exists after failure: %v", err)
	}
	if _, err := os.Stat(finalPath + ".part"); err != nil {
		t.Fatalf("part file was not retained: %v", err)
	}
}
