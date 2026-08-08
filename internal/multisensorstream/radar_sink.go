package multisensorstream

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"mmwcli/internal/capturestream"
)

// RadarSink converts capturestream frame geometry into provisional radar
// ITEM records on the aggregate's one-gigahertz, non-wrapping frame clock.
type RadarSink struct {
	adapter     *StdoutAdapter
	sourceID    string
	periodTicks uint64
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
	return &RadarSink{adapter: adapter, sourceID: sourceID, periodTicks: periodTicks}, nil
}

// WriteFrame implements capturestream.FrameSink without retaining or mutating
// payload. Frame index zero maps to tick zero; duration is exactly FramePeriod.
func (sink *RadarSink) WriteFrame(
	ctx context.Context,
	index uint64,
	payload []byte,
) error {
	if sink == nil || sink.adapter == nil || sink.periodTicks == 0 {
		return errors.New("multisensor radar sink is nil")
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
