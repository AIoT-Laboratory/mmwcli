package multisensorcapture

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"math/bits"
	"os"
	"path/filepath"
	"sync"
	"time"

	"mmwcli/internal/multisensor"
	"mmwcli/internal/multisensorstream"
	"mmwcli/internal/sensorproducer"
)

const (
	maximumRecordedItems = (multisensor.MaximumDirectoryIndexBytes - uint64(multisensor.SensorIndexHeaderBytes)) /
		uint64(multisensor.SensorIndexEntryBytes)
	defaultCleanupTimeout = 5 * time.Second
)

type sourceWorker struct {
	plan       SourcePlan
	sessionID  string
	sourceDir  string
	process    ProducerProcess
	hostOrigin time.Time
	itemSink   ItemSink

	drainCancel context.CancelFunc
	done        chan struct{}

	mu          sync.Mutex
	intentional bool
	err         error
	metadata    *ProducerSessionMetadata
	source      multisensor.Source
}

// CleanupFailure marks a failed lifecycle operation that leaves process
// convergence unproven. Plain producer exit and stream errors are not cleanup
// failures: optional sources may record those as failed while the aggregate
// remains publishable.
type CleanupFailure struct {
	operation string
	err       error
}

func (failure *CleanupFailure) Error() string {
	return fmt.Sprintf("producer cleanup %s: %v", failure.operation, failure.err)
}

func (failure *CleanupFailure) Unwrap() error { return failure.err }

// NewCleanupFailure marks an explicit producer cleanup or convergence failure.
// It is intended for bounded producer implementations and offline lifecycle
// fakes; a plain process exit error must be returned unchanged.
func NewCleanupFailure(operation string, err error) error {
	if err == nil {
		return nil
	}
	return &CleanupFailure{operation: operation, err: err}
}

func cleanupFailureOf(err error) error {
	var failure *CleanupFailure
	if errors.As(err, &failure) {
		return failure
	}
	return nil
}

func boundedCleanupContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil || parent.Err() != nil {
		return context.WithTimeout(context.Background(), defaultCleanupTimeout)
	}
	if deadline, ok := parent.Deadline(); ok && time.Until(deadline) <= defaultCleanupTimeout {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, defaultCleanupTimeout)
}

func waitForDrain(worker *sourceWorker, ctx context.Context) error {
	if workerDrained(worker) {
		return nil
	}
	select {
	case <-worker.done:
		return nil
	case <-ctx.Done():
		if workerDrained(worker) {
			return nil
		}
		return NewCleanupFailure("drain", ctx.Err())
	}
}

func workerDrained(worker *sourceWorker) bool {
	select {
	case <-worker.done:
		return true
	default:
		return false
	}
}

func newSourceWorker(
	ctx context.Context,
	sessionID string,
	plan SourcePlan,
	sourceDir string,
	process ProducerProcess,
	hostOrigin time.Time,
	itemSink ItemSink,
	onFailure func(error),
) (*sourceWorker, error) {
	payloadPath := filepath.Join(sourceDir, plan.Payload.Filename)
	payload, err := os.OpenFile(payloadPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("create source payload: %w", err)
	}
	drainCtx, cancel := context.WithCancel(ctx)
	worker := &sourceWorker{
		plan: plan, sessionID: sessionID, sourceDir: sourceDir, process: process,
		hostOrigin: hostOrigin, itemSink: itemSink, drainCancel: cancel, done: make(chan struct{}),
	}
	go func() {
		worker.drain(payload, drainCtx)
		worker.mu.Lock()
		err := worker.err
		intentional := worker.intentional
		worker.mu.Unlock()
		if err != nil && !intentional && onFailure != nil {
			onFailure(err)
		}
		close(worker.done)
	}()
	return worker, nil
}

func (worker *sourceWorker) drain(payload *os.File, ctx context.Context) {
	metadata, index, payloadHash, err := worker.readStream(payload, ctx)
	if err == nil {
		err = payload.Sync()
	}
	closeErr := payload.Close()
	if err == nil {
		err = closeErr
	}
	var source multisensor.Source
	if err == nil {
		source, err = worker.publishIndex(metadata, index, payloadHash)
	}
	worker.mu.Lock()
	worker.err = err
	if metadata != nil {
		copy := *metadata
		copy.ClockObservations = append([]multisensor.ClockObservation(nil), metadata.ClockObservations...)
		copy.AffineSegments = cloneAffineSegments(metadata.AffineSegments)
		copy.ApplicationMetadata = cloneMetadata(metadata.ApplicationMetadata)
		worker.metadata = &copy
	}
	worker.source = source
	worker.mu.Unlock()
}

func (worker *sourceWorker) readStream(
	payload *os.File,
	ctx context.Context,
) (*ProducerSessionMetadata, multisensor.SensorIndex, hash.Hash, error) {
	index := multisensor.SensorIndex{Entries: []multisensor.IndexEntry{}}
	payloadHash := sha256.New()
	var metadata *ProducerSessionMetadata
	expectedSeq := uint64(1)
	ended := false
	eofFrame := false
	for {
		record, err := worker.process.Next(ctx)
		receivedAt := time.Now()
		if errors.Is(err, io.EOF) {
			if !eofFrame {
				return metadata, index, payloadHash, errors.New("producer transport EOF arrived before EOF frame")
			}
			return metadata, index, payloadHash, nil
		}
		if err != nil {
			return metadata, index, payloadHash, err
		}
		if record.SessionID != worker.sessionID || record.SourceID != worker.plan.SourceID {
			return metadata, index, payloadHash, errors.New("producer frame identity does not match the capture plan")
		}
		if record.Seq != expectedSeq {
			return metadata, index, payloadHash, fmt.Errorf(
				"producer frame sequence is %d, want %d", record.Seq, expectedSeq,
			)
		}
		if expectedSeq == ^uint64(0) {
			return metadata, index, payloadHash, errors.New("producer frame sequence is exhausted")
		}
		expectedSeq++
		if record.Type != sensorproducer.FrameItem && len(record.Payload) != 0 {
			return metadata, index, payloadHash, errors.New("non-ITEM producer frame carries payload bytes")
		}

		switch record.Type {
		case sensorproducer.FrameSession:
			if metadata != nil || ended || eofFrame {
				return metadata, index, payloadHash, errors.New("producer SESSION frame is out of order")
			}
			var decoded ProducerSessionMetadata
			if err := decodeMetadata(record.Metadata, &decoded); err != nil {
				return metadata, index, payloadHash, fmt.Errorf("decode producer SESSION metadata: %w", err)
			}
			if err := validateProducerSessionMetadata(worker.plan, decoded); err != nil {
				return metadata, index, payloadHash, err
			}
			metadata = &decoded
		case sensorproducer.FrameItem:
			if metadata == nil || ended || eofFrame {
				return metadata, index, payloadHash, errors.New("producer ITEM frame is out of order")
			}
			var item ProducerItemMetadata
			if err := decodeMetadata(record.Metadata, &item); err != nil {
				return metadata, index, payloadHash, fmt.Errorf("decode producer ITEM metadata: %w", err)
			}
			if item.Schema != ProducerItemSchema {
				return metadata, index, payloadHash, fmt.Errorf(
					"producer ITEM schema is %q, want %q", item.Schema, ProducerItemSchema,
				)
			}
			if item.ItemIndex != uint64(len(index.Entries)) {
				return metadata, index, payloadHash, fmt.Errorf(
					"producer ITEM index is %d, want %d", item.ItemIndex, len(index.Entries),
				)
			}
			if len(record.Payload) == 0 {
				return metadata, index, payloadHash, errors.New("producer ITEM payload is empty")
			}
			if uint64(len(index.Entries)) >= worker.plan.Limits.MaxItems ||
				uint64(len(index.Entries)) >= maximumRecordedItems {
				return metadata, index, payloadHash, errors.New("producer ITEM count exceeds the declared or directory bound")
			}
			payloadSize := uint64(len(record.Payload))
			if payloadSize > worker.plan.Limits.MaxItemBytes || payloadSize > sensorproducer.MaxPayloadBytes {
				return metadata, index, payloadHash, errors.New("producer ITEM payload exceeds the declared bound")
			}
			payloadEnd, ok := checkedAdd(index.PayloadBytes, payloadSize)
			if !ok || payloadEnd > worker.plan.Limits.MaxPayloadBytes {
				return metadata, index, payloadHash, errors.New("producer payload exceeds the declared byte bound")
			}
			if item.SyncEventID != multisensor.NoSyncEventID {
				return metadata, index, payloadHash, errors.New(
					"software_barrier ITEM must use the no-sync-event sentinel",
				)
			}
			if worker.plan.Clock.TimestampSemantics == multisensor.TimestampDeliveryObserved {
				if item.Tick != 0 || item.WrapCount != 0 || item.DurationTicks != 0 {
					return metadata, index, payloadHash, errors.New(
						"delivery_observed producer ITEM tick, wrap_count, and duration_ticks must be zero",
					)
				}
				if receivedAt.Before(worker.hostOrigin) {
					return metadata, index, payloadHash, errors.New(
						"delivery_observed ITEM arrived before the aggregate host origin",
					)
				}
				item.Tick = uint64(receivedAt.Sub(worker.hostOrigin))
			}
			if _, err := multisensor.UnwrapTicks(worker.plan.Clock, item.Tick, item.WrapCount); err != nil {
				return metadata, index, payloadHash, fmt.Errorf("producer ITEM clock: %w", err)
			}
			if err := writeAll(payload, record.Payload); err != nil {
				return metadata, index, payloadHash, fmt.Errorf("write producer payload: %w", err)
			}
			if _, err := payloadHash.Write(record.Payload); err != nil {
				return metadata, index, payloadHash, err
			}
			index.Entries = append(index.Entries, multisensor.IndexEntry{
				ItemIndex: item.ItemIndex, PayloadOffset: index.PayloadBytes, PayloadSize: payloadSize,
				Tick: item.Tick, WrapCount: item.WrapCount, DurationTicks: item.DurationTicks,
				SyncEventID: item.SyncEventID,
			})
			index.PayloadBytes = payloadEnd
			if worker.itemSink != nil {
				err := worker.itemSink.WriteItem(ctx, multisensorstream.Item{
					SourceID: worker.plan.SourceID, ItemIndex: item.ItemIndex,
					Tick: item.Tick, WrapCount: item.WrapCount, DurationTicks: item.DurationTicks,
					SyncEventID: item.SyncEventID, Payload: record.Payload,
				})
				if err != nil {
					return metadata, index, payloadHash, fmt.Errorf("write producer ITEM sink: %w", err)
				}
			}
		case sensorproducer.FrameEnd:
			if metadata == nil || ended || eofFrame {
				return metadata, index, payloadHash, errors.New("producer END frame is out of order")
			}
			var end ProducerEndMetadata
			if err := decodeMetadata(record.Metadata, &end); err != nil {
				return metadata, index, payloadHash, fmt.Errorf("decode producer END metadata: %w", err)
			}
			actualDigest := hex.EncodeToString(payloadHash.Sum(nil))
			if end.Schema != ProducerEndSchema || end.ItemCount != uint64(len(index.Entries)) ||
				end.PayloadBytes != index.PayloadBytes || end.PayloadSHA256 != actualDigest {
				return metadata, index, payloadHash, errors.New("producer END counts or SHA-256 do not match the received payload")
			}
			ended = true
		case sensorproducer.FrameEOF:
			if !ended || eofFrame {
				return metadata, index, payloadHash, errors.New("producer EOF frame is out of order")
			}
			var eof ProducerEOFMetadata
			if err := decodeMetadata(record.Metadata, &eof); err != nil {
				return metadata, index, payloadHash, fmt.Errorf("decode producer EOF metadata: %w", err)
			}
			if eof.Schema != ProducerEOFSchema {
				return metadata, index, payloadHash, fmt.Errorf(
					"producer EOF schema is %q, want %q", eof.Schema, ProducerEOFSchema,
				)
			}
			eofFrame = true
		case sensorproducer.FrameError:
			var failure sensorproducer.ErrorMetadata
			if err := decodeMetadata(record.Metadata, &failure); err != nil || failure.Message == "" {
				return metadata, index, payloadHash, errors.New("producer ERROR metadata is invalid")
			}
			return metadata, index, payloadHash, fmt.Errorf("producer failed: %s", failure.Message)
		default:
			return metadata, index, payloadHash, fmt.Errorf("unexpected producer frame type %d", record.Type)
		}
	}
}

func (worker *sourceWorker) publishIndex(
	metadata *ProducerSessionMetadata,
	index multisensor.SensorIndex,
	payloadHash hash.Hash,
) (multisensor.Source, error) {
	if metadata == nil {
		return multisensor.Source{}, errors.New("producer stream has no SESSION metadata")
	}
	if err := bindDeliveryObservedClock(worker.plan.SourceID, metadata, index); err != nil {
		return multisensor.Source{}, err
	}
	encoded, err := multisensor.EncodeSensorIndex(index, metadata.Limits)
	if err != nil {
		return multisensor.Source{}, err
	}
	if uint64(len(encoded)) > multisensor.MaximumDirectoryIndexBytes {
		return multisensor.Source{}, errors.New("producer index exceeds the directory bound")
	}
	indexPath := filepath.Join(worker.sourceDir, multisensor.IndexFileName)
	indexFile, err := os.OpenFile(indexPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return multisensor.Source{}, fmt.Errorf("create producer index: %w", err)
	}
	if err := writeAll(indexFile, encoded); err != nil {
		return multisensor.Source{}, errors.Join(err, indexFile.Close())
	}
	if err := indexFile.Sync(); err != nil {
		return multisensor.Source{}, errors.Join(err, indexFile.Close())
	}
	if err := indexFile.Close(); err != nil {
		return multisensor.Source{}, err
	}
	indexDigest := sha256.Sum256(encoded)
	source := multisensor.Source{
		SourceID: worker.plan.SourceID, Kind: metadata.Kind, Required: worker.plan.Required,
		Outcome: multisensor.OutcomeComplete, Producer: metadata.Producer, Limits: metadata.Limits,
		Payload: metadata.Payload, ItemCount: uint64(len(index.Entries)), PayloadBytes: index.PayloadBytes,
		Clock: metadata.Clock, ClockObservations: append([]multisensor.ClockObservation(nil), metadata.ClockObservations...),
		AffineSegments: cloneAffineSegments(metadata.AffineSegments),
		Artifacts: []multisensor.Artifact{
			{
				Role: multisensor.ArtifactPayload, Path: metadata.Payload.Filename,
				SizeBytes: index.PayloadBytes, SHA256: hex.EncodeToString(payloadHash.Sum(nil)),
			},
			{
				Role: multisensor.ArtifactIndex, Path: multisensor.IndexFileName,
				SizeBytes: uint64(len(encoded)), SHA256: hex.EncodeToString(indexDigest[:]),
			},
		},
		ApplicationMetadata: cloneMetadata(metadata.ApplicationMetadata),
	}
	if err := validateOneSource(source, index); err != nil {
		return multisensor.Source{}, err
	}
	return source, nil
}

func bindDeliveryObservedClock(
	sourceID string,
	metadata *ProducerSessionMetadata,
	index multisensor.SensorIndex,
) error {
	if metadata.Clock.TimestampSemantics != multisensor.TimestampDeliveryObserved || len(index.Entries) == 0 {
		return nil
	}
	firstTick := index.Entries[0].Tick
	lastTick := index.Entries[len(index.Entries)-1].Tick
	endTick, ok := checkedAdd(lastTick, 1)
	if !ok {
		return errors.New("delivery_observed camera clock range overflows uint64")
	}
	observationID := sourceID + "-delivery-anchor"
	metadata.ClockObservations = []multisensor.ClockObservation{{
		ObservationID: observationID, Tick: firstTick,
		HostBeforeNS: firstTick, HostAfterNS: firstTick,
	}}
	metadata.AffineSegments = []multisensor.AffineSegment{{
		StartUnwrappedTick: firstTick, EndUnwrappedTick: endTick,
		SourceOriginTick: firstTick, HostOriginNS: firstTick,
		ScaleNum: 1, ScaleDen: 1, ObservationIDs: []string{observationID},
	}}
	return nil
}

func (worker *sourceWorker) collect(ctx context.Context) (multisensor.Source, error) {
	if !workerDrained(worker) {
		select {
		case <-worker.done:
		case <-ctx.Done():
			if !workerDrained(worker) {
				cleanupCtx, cancel := boundedCleanupContext(ctx)
				defer cancel()
				cancelErr := worker.process.Cancel(cleanupCtx)
				worker.drainCancel()
				drainErr := waitForDrain(worker, cleanupCtx)
				return multisensor.Source{}, errors.Join(
					NewCleanupFailure("collect context", ctx.Err()),
					cancelErr,
					drainErr,
				)
			}
		}
	}
	waitErr := worker.process.Wait(ctx)
	worker.mu.Lock()
	defer worker.mu.Unlock()
	return cloneSource(worker.source), errors.Join(worker.err, waitErr)
}

func (worker *sourceWorker) abort(ctx context.Context) error {
	cleanupCtx, cancel := boundedCleanupContext(ctx)
	defer cancel()
	worker.mu.Lock()
	worker.intentional = true
	worker.mu.Unlock()
	cancelErr := worker.process.Cancel(cleanupCtx)
	worker.drainCancel()
	drainErr := waitForDrain(worker, cleanupCtx)
	return errors.Join(
		cancelErr,
		drainErr,
	)
}

func failedSource(plan SourcePlan, worker *sourceWorker) (multisensor.Source, error) {
	metadata := ProducerSessionMetadata{
		Schema: ProducerSessionSchema, Kind: plan.Kind, Producer: plan.Producer,
		Limits: plan.Limits, Payload: plan.Payload, Clock: plan.Clock,
		ClockObservations: []multisensor.ClockObservation{}, AffineSegments: []multisensor.AffineSegment{},
		SyncEventSemantics:  plan.SyncEventSemantics,
		ApplicationMetadata: cloneMetadata(plan.ApplicationMetadata),
	}
	if worker != nil {
		worker.mu.Lock()
		if worker.metadata != nil {
			metadata = *worker.metadata
			metadata.ClockObservations = append(
				[]multisensor.ClockObservation(nil), worker.metadata.ClockObservations...,
			)
			metadata.AffineSegments = cloneAffineSegments(worker.metadata.AffineSegments)
			metadata.ApplicationMetadata = cloneMetadata(worker.metadata.ApplicationMetadata)
		}
		worker.mu.Unlock()
	}
	source := multisensor.Source{
		SourceID: plan.SourceID, Kind: metadata.Kind, Required: false,
		Outcome: multisensor.OutcomeFailed, Producer: metadata.Producer, Limits: metadata.Limits,
		Payload: metadata.Payload, Clock: metadata.Clock,
		ClockObservations: metadata.ClockObservations, AffineSegments: metadata.AffineSegments,
		Artifacts: []multisensor.Artifact{}, ApplicationMetadata: metadata.ApplicationMetadata,
	}
	if err := validateFailedSource(source); err != nil {
		return multisensor.Source{}, err
	}
	return source, nil
}

func validateFailedSource(source multisensor.Source) error {
	session := validationSession(multisensor.ApplicationMetadata{}, []multisensor.Source{source}, multisensor.AggregateTotals{
		SourceCount: 1,
	})
	return session.ValidateWithIndexes(map[string]multisensor.SensorIndex{})
}

func removeSourceDirectory(directory *multisensor.Directory, sourceID string) error {
	path, err := directory.SourcePath(sourceID)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove failed source directory %q: %w", sourceID, err)
	}
	return nil
}

func cloneAffineSegments(segments []multisensor.AffineSegment) []multisensor.AffineSegment {
	result := append([]multisensor.AffineSegment(nil), segments...)
	for index := range result {
		result[index].ObservationIDs = append([]string(nil), segments[index].ObservationIDs...)
	}
	return result
}

func writeAll(writer io.Writer, encoded []byte) error {
	for len(encoded) > 0 {
		written, err := writer.Write(encoded)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrNoProgress
		}
		encoded = encoded[written:]
	}
	return nil
}

func checkedAdd(left, right uint64) (uint64, bool) {
	sum, carry := bits.Add64(left, right, 0)
	return sum, carry == 0
}
