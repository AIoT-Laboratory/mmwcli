package multisensorstream

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"mmwcli/internal/capturestream"
)

// RadarSink converts capturestream frame geometry into provisional radar
// ITEM records on the aggregate's one-gigahertz, non-wrapping frame clock.
type RadarSink struct {
	adapter     *StdoutAdapter
	sourceID    string
	periodTicks uint64

	startOnce  sync.Once
	startReady chan struct{}
	startMu    sync.Mutex
	startErr   error
}

func NewRadarSink(
	adapter *StdoutAdapter,
	sourceID string,
	framePeriod time.Duration,
) (*RadarSink, error) {
	if adapter == nil || adapter.encoder == nil || adapter.output == nil {
		return nil, errors.New("multisensor radar sink adapter is nil")
	}
	if framePeriod <= 0 {
		return nil, errors.New("multisensor radar FramePeriod must be positive")
	}
	periodTicks := uint64(framePeriod)

	adapter.encoderMu.Lock()
	defer adapter.encoderMu.Unlock()
	if adapter.output.closed.Load() {
		return nil, ErrStdoutClosed
	}
	source, exists := adapter.encoder.sources[sourceID]
	if !exists || source.contract.Kind != SourceRadar {
		return nil, fmt.Errorf("multisensor radar sink source %q is not a declared radar", sourceID)
	}
	clock := source.contract.Clock
	if clock.TickHz != uint64(time.Second) || clock.WrapTicks != 0 ||
		clock.TimestampSemantics != TimestampFrameStart {
		return nil, errors.New(
			"multisensor radar sink requires a 1 GHz non-wrapping frame_start clock",
		)
	}
	if maximumIndex := source.contract.Limits.MaxItems - 1; maximumIndex > math.MaxUint64/periodTicks {
		return nil, errors.New("multisensor radar FramePeriod overflows the declared item range")
	}
	return &RadarSink{
		adapter: adapter, sourceID: sourceID, periodTicks: periodTicks,
		startReady: make(chan struct{}),
	}, nil
}

// ReleaseRadarStart allows frame zero to be emitted only after this sink's
// RADAR_START record has been written successfully.
func (sink *RadarSink) ReleaseRadarStart() error {
	if sink == nil || sink.adapter == nil || sink.startReady == nil {
		return errors.New("multisensor radar sink is nil")
	}
	sink.adapter.encoderMu.Lock()
	source := sink.adapter.encoder.sources[sink.sourceID]
	started := source != nil && source.radarStarted
	sink.adapter.encoderMu.Unlock()
	if !started {
		return errors.New("multisensor radar sink requires an emitted RADAR_START")
	}
	if !sink.resolveRadarStart(nil) {
		return errors.New("multisensor radar sink start is already resolved")
	}
	return nil
}

// FailRadarStart releases a blocked frame with the terminal start-evidence
// failure. The first resolution wins.
func (sink *RadarSink) FailRadarStart(err error) error {
	if sink == nil || sink.startReady == nil {
		return errors.New("multisensor radar sink is nil")
	}
	if err == nil {
		return errors.New("multisensor radar sink start failure is nil")
	}
	if !sink.resolveRadarStart(err) {
		return errors.New("multisensor radar sink start is already resolved")
	}
	return nil
}

func (sink *RadarSink) resolveRadarStart(err error) bool {
	resolved := false
	sink.startOnce.Do(func() {
		sink.startMu.Lock()
		sink.startErr = err
		sink.startMu.Unlock()
		close(sink.startReady)
		resolved = true
	})
	return resolved
}

// WriteFrame implements capturestream.FrameSink without retaining or mutating
// payload. Frame index zero maps to tick zero; duration is exactly FramePeriod.
func (sink *RadarSink) WriteFrame(
	ctx context.Context,
	index uint64,
	payload []byte,
) error {
	if sink == nil || sink.adapter == nil || sink.periodTicks == 0 || sink.startReady == nil {
		return errors.New("multisensor radar sink is nil")
	}
	if ctx == nil {
		return errors.New("multisensor radar sink context is nil")
	}
	select {
	case <-sink.startReady:
		sink.startMu.Lock()
		startErr := sink.startErr
		sink.startMu.Unlock()
		if startErr != nil {
			return startErr
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	if index > math.MaxUint64/sink.periodTicks {
		return errors.New("multisensor radar frame tick overflows uint64")
	}
	return sink.adapter.WriteItem(ctx, Item{
		SourceID: sink.sourceID, ItemIndex: index,
		Tick: index * sink.periodTicks, DurationTicks: sink.periodTicks,
		SyncEventID: NoSyncEventID, Payload: payload,
	})
}

var _ capturestream.FrameSink = (*RadarSink)(nil)
