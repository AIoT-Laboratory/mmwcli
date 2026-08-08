package multisensor

import (
	"reflect"
	"testing"
)

func TestNewSessionIDProducesUniqueUUIDv4Values(t *testing.T) {
	seen := make(map[string]struct{})
	for range 64 {
		value, err := NewSessionID()
		if err != nil {
			t.Fatal(err)
		}
		if !sessionIDPattern.MatchString(value) {
			t.Fatalf("NewSessionID() = %q", value)
		}
		if _, duplicate := seen[value]; duplicate {
			t.Fatalf("NewSessionID produced duplicate %q", value)
		}
		seen[value] = struct{}{}
	}
}

func TestNewSoftwareBarrierSessionComputesAggregateTotals(t *testing.T) {
	want, indexes, _ := twoSourceFixture(t)
	got, err := NewSoftwareBarrierSession(want.SessionID, want.Sources, want.ApplicationMetadata)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("session differs:\n got %+v\nwant %+v", got, want)
	}
	if err := got.ValidateWithIndexes(indexes); err != nil {
		t.Fatal(err)
	}
}

func TestNewSoftwareBarrierSessionRejectsInvalidSources(t *testing.T) {
	session, _, _ := twoSourceFixture(t)
	if _, err := NewSoftwareBarrierSession(session.SessionID, nil, ApplicationMetadata{}); err == nil {
		t.Fatal("empty source list was accepted")
	}
	session.Sources[0].Clock.ClockID = HostClockID
	if _, err := NewSoftwareBarrierSession(
		session.SessionID,
		session.Sources,
		session.ApplicationMetadata,
	); err == nil {
		t.Fatal("source reusing the host clock id was accepted")
	}
}
