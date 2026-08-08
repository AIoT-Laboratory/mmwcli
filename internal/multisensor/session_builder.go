package multisensor

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
)

const HostClockID = "host-monotonic"

// NewSessionID returns a lowercase UUIDv4 for one aggregate capture.
func NewSessionID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate multisensor session id: %w", err)
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	encoded := hex.EncodeToString(raw[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" +
		encoded[16:20] + "-" + encoded[20:], nil
}

// NewSoftwareBarrierSession combines already-finalized sources into the sole
// aggregate session shape currently published by mmwcli. Radar and external
// source artifacts remain independently validated by Directory.CommitContext.
func NewSoftwareBarrierSession(
	sessionID string,
	sources []Source,
	metadata ApplicationMetadata,
) (Session, error) {
	if len(sources) == 0 {
		return Session{}, errors.New("multisensor session requires at least one finalized source")
	}
	totals := AggregateTotals{SourceCount: uint64(len(sources))}
	for _, source := range sources {
		if source.Required {
			totals.RequiredSourceCount++
		}
		if source.Outcome != OutcomeComplete {
			continue
		}
		totals.CompleteSourceCount++
		var ok bool
		totals.ItemCount, ok = checkedAddU64(totals.ItemCount, source.ItemCount)
		if !ok {
			return Session{}, errors.New("aggregate item_count overflows uint64")
		}
		totals.PayloadBytes, ok = checkedAddU64(totals.PayloadBytes, source.PayloadBytes)
		if !ok {
			return Session{}, errors.New("aggregate payload_bytes overflows uint64")
		}
	}
	session := Session{
		Schema:               SessionSchema,
		SessionID:            sessionID,
		SynchronizationGrade: SynchronizationSoftwareBarrier,
		HostClock: Clock{
			ClockID: HostClockID, TickHz: 1_000_000_000,
			TimestampSemantics: TimestampHostMonotonic,
		},
		Sources:             append([]Source(nil), sources...),
		SyncEvents:          []SyncEvent{},
		Totals:              totals,
		ApplicationMetadata: metadata,
	}
	if err := session.Validate(); err != nil {
		return Session{}, err
	}
	return session, nil
}
