package multisensorcapture

import (
	"encoding/json"
	"strings"
	"testing"

	"mmwcli/internal/multisensor"
	"mmwcli/internal/sensorproducer"
)

func TestParsePlanAcceptsClosedExternalCameraContract(t *testing.T) {
	plan := validPlan(true, multisensor.SourceLimits{MaxItems: 4, MaxItemBytes: 16, MaxPayloadBytes: 64})
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParsePlan(encoded)
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if parsed.Schema != PlanSchema || len(parsed.Sources) != 1 || parsed.Sources[0].Kind != multisensor.SourceCamera {
		t.Fatalf("unexpected parsed plan: %+v", parsed)
	}
}

func TestParsePlanRejectsOpenOrAmbiguousRoutes(t *testing.T) {
	valid := validPlan(false, multisensor.SourceLimits{MaxItems: 4, MaxItemBytes: 16, MaxPayloadBytes: 64})
	encoded, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		encoded string
		match   string
	}{
		{
			name: "unknown delivery timestamp",
			encoded: strings.Replace(string(encoded), `"sync_event_semantics":"none"`,
				`"sync_event_semantics":"none","delivery_timestamp_ns":1`, 1),
			match: "unknown field",
		},
		{
			name:    "duplicate schema",
			encoded: strings.Replace(string(encoded), `{"schema":`, `{"schema":"duplicate","schema":`, 1),
			match:   "duplicate",
		},
		{
			name:    "trailing value",
			encoded: string(encoded) + `{}`,
			match:   "trailing",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParsePlan([]byte(test.encoded))
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("ParsePlan error = %v, want substring %q", err, test.match)
			}
		})
	}
}

func TestPlanRejectsRadarAliasAndBounds(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Plan)
		match  string
	}{
		{
			name: "radar is implicit",
			mutate: func(plan *Plan) {
				plan.Sources[0].Kind = multisensor.SourceRadar
				plan.Sources[0].Clock.TimestampSemantics = multisensor.TimestampFrameStart
			},
			match: "radar is implicit",
		},
		{
			name: "event semantics closed",
			mutate: func(plan *Plan) {
				plan.Sources[0].SyncEventSemantics = "delivery_time"
			},
			match: "sync_event_semantics",
		},
		{
			name: "queue bounded",
			mutate: func(plan *Plan) {
				plan.Sources[0].QueueSize = sensorproducer.MaxQueueSize + 1
			},
			match: "queue_size",
		},
		{
			name: "command required",
			mutate: func(plan *Plan) {
				plan.Sources[0].Argv = nil
			},
			match: "argv count",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := validPlan(false, multisensor.SourceLimits{MaxItems: 4, MaxItemBytes: 16, MaxPayloadBytes: 64})
			test.mutate(&plan)
			if err := plan.Validate(); err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("Validate error = %v, want substring %q", err, test.match)
			}
		})
	}
}

func validPlan(required bool, limits multisensor.SourceLimits) Plan {
	return Plan{
		Schema: PlanSchema,
		Sources: []SourcePlan{
			{
				SourceID: "camera-0", Kind: multisensor.SourceCamera, Required: required,
				Argv: []string{"fake-camera"}, Producer: multisensor.Producer{Name: "fake-camera", Version: "1.0"},
				Limits: limits, Payload: multisensor.PayloadContract{Filename: "frames.bin", Format: "camera.rgb8.v1"},
				Clock: multisensor.Clock{
					ClockID: "camera-0-clock", TickHz: 1_000_000,
					TimestampSemantics: multisensor.TimestampExposureMidpoint,
				},
				SyncEventSemantics:  SyncEventSemanticsNone,
				ApplicationMetadata: multisensor.ApplicationMetadata{},
			},
		},
		ApplicationMetadata: multisensor.ApplicationMetadata{},
	}
}
