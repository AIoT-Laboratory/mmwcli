package fixedframeproducer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"mmwcli/internal/multisensor"
	"mmwcli/internal/multisensorcapture"
	"mmwcli/internal/sensorproducer"
)

const (
	testSessionID = "session-fixed-frames"
	testSourceID  = "camera-0"
)

var (
	testFrame0 = []byte{0x01, 0x02, 0x03, 0x04}
	testFrame1 = []byte{0x10, 0x20, 0x30, 0x40}
)

func TestRunStreamsTwoFixedFramesAndStopsCleanly(t *testing.T) {
	harness := newProducerHarness(t, "two")
	harness.start(t)

	session := nextRecord(t, harness, sensorproducer.FrameSession)
	metadata := decodeMetadata[multisensorcapture.ProducerSessionMetadata](t, session.Metadata)
	if metadata.Schema != multisensorcapture.ProducerSessionSchema ||
		metadata.Kind != multisensor.SourceCamera || metadata.Clock != harness.plan.Clock ||
		len(metadata.ClockObservations) != 0 || len(metadata.AffineSegments) != 0 {
		t.Fatalf("SESSION metadata = %+v", metadata)
	}

	for index, wantPayload := range [][]byte{testFrame0, testFrame1} {
		record := nextRecord(t, harness, sensorproducer.FrameItem)
		item := decodeMetadata[multisensorcapture.ProducerItemMetadata](t, record.Metadata)
		if item.Schema != multisensorcapture.ProducerItemSchema || item.ItemIndex != uint64(index) ||
			item.Tick != 0 || item.WrapCount != 0 || item.DurationTicks != 0 ||
			item.SyncEventID != multisensor.NoSyncEventID || !slices.Equal(record.Payload, wantPayload) {
			t.Fatalf("ITEM %d = %+v payload=%x", index, item, record.Payload)
		}
	}
	if err := harness.client.Stop(harness.ctx); err != nil {
		t.Fatal(err)
	}
	endRecord := nextRecord(t, harness, sensorproducer.FrameEnd)
	end := decodeMetadata[multisensorcapture.ProducerEndMetadata](t, endRecord.Metadata)
	payload := append(append([]byte(nil), testFrame0...), testFrame1...)
	digest := sha256.Sum256(payload)
	if end.Schema != multisensorcapture.ProducerEndSchema || end.ItemCount != 2 ||
		end.PayloadBytes != uint64(len(payload)) || end.PayloadSHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("END metadata = %+v", end)
	}
	nextRecord(t, harness, sensorproducer.FrameEOF)
	if err := harness.client.Wait(harness.ctx); err != nil {
		t.Fatal(err)
	}
	if err := harness.wait(t); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(harness.stderr.String(), "fixed-frame camera helper") {
		t.Fatalf("child stderr = %q", harness.stderr.String())
	}
}

func TestRunReportsTruncatedAndFailedCameraChildren(t *testing.T) {
	tests := []struct {
		name      string
		mode      string
		itemCount int
		message   string
	}{
		{name: "partial frame", mode: "partial", itemCount: 1, message: "partial frame"},
		{name: "early EOF", mode: "early", message: "EOF before STOP"},
		{name: "child failure", mode: "failure", message: "failed before STOP"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newProducerHarness(t, test.mode)
			harness.start(t)
			nextRecord(t, harness, sensorproducer.FrameSession)
			for range test.itemCount {
				nextRecord(t, harness, sensorproducer.FrameItem)
			}
			failureRecord := nextRecord(t, harness, sensorproducer.FrameError)
			failure := decodeMetadata[sensorproducer.ErrorMetadata](t, failureRecord.Metadata)
			if !strings.Contains(failure.Message, test.message) {
				t.Fatalf("ERROR message = %q, want %q", failure.Message, test.message)
			}
			nextRecord(t, harness, sensorproducer.FrameEOF)
			if err := harness.client.Wait(harness.ctx); err == nil {
				t.Fatal("client Wait unexpectedly succeeded")
			}
			if err := harness.wait(t); err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("Run error = %v, want %q", err, test.message)
			}
		})
	}
}

func TestRunCancelAcknowledgesAndTerminatesWithoutEnd(t *testing.T) {
	harness := newProducerHarness(t, "block")
	harness.start(t)
	nextRecord(t, harness, sensorproducer.FrameSession)
	if err := harness.client.Cancel(harness.ctx); err != nil {
		t.Fatal(err)
	}
	nextRecord(t, harness, sensorproducer.FrameEOF)
	if err := harness.client.Wait(harness.ctx); err != nil {
		t.Fatal(err)
	}
	if err := harness.wait(t); err != nil {
		t.Fatal(err)
	}
}

func TestRunRejectsNonDeliveryObservedContracts(t *testing.T) {
	plan := validSourcePlan()
	plan.Clock = multisensor.Clock{
		ClockID: "camera-device", TickHz: 1_000_000,
		TimestampSemantics: multisensor.TimestampExposureMidpoint,
	}
	err := Run(
		context.Background(), plan, 4, helperCommand("block"),
		bytes.NewReader(nil), io.Discard, io.Discard,
	)
	if err == nil || !strings.Contains(err.Error(), "delivery_observed") {
		t.Fatalf("Run error = %v", err)
	}
}

func TestFixedFrameCameraHelper(t *testing.T) {
	mode, ok := helperMode(os.Args)
	if !ok {
		return
	}
	_, _ = fmt.Fprintln(os.Stderr, "fixed-frame camera helper")
	switch mode {
	case "two":
		writeHelperBytes(testFrame0)
		writeHelperBytes(testFrame1)
		time.Sleep(time.Hour)
	case "partial":
		writeHelperBytes(testFrame0)
		writeHelperBytes([]byte{0xaa, 0xbb})
	case "early":
	case "failure":
		os.Exit(3)
	case "block":
		time.Sleep(time.Hour)
	default:
		_, _ = fmt.Fprintln(os.Stderr, "unknown helper mode", mode)
		os.Exit(2)
	}
	os.Exit(0)
}

type producerHarness struct {
	ctx    context.Context
	cancel context.CancelFunc
	client *sensorproducer.Client
	plan   multisensorcapture.SourcePlan
	stderr bytes.Buffer
	done   chan error
}

func newProducerHarness(t *testing.T, mode string) *producerHarness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	producerInput, controlOutput := io.Pipe()
	recordInput, producerOutput := io.Pipe()
	harness := &producerHarness{
		ctx: ctx, cancel: cancel, plan: validSourcePlan(), done: make(chan error, 1),
	}
	go func() {
		err := Run(
			ctx, harness.plan, uint64(len(testFrame0)), helperCommand(mode),
			producerInput, producerOutput, &harness.stderr,
		)
		_ = producerInput.Close()
		_ = producerOutput.Close()
		harness.done <- err
	}()
	client, err := sensorproducer.NewClient(
		controlOutput,
		recordInput,
		testSessionID,
		testSourceID,
		sensorproducer.ClientOptions{},
	)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	harness.client = client
	t.Cleanup(func() {
		cancel()
		_ = client.Close()
	})
	return harness
}

func (harness *producerHarness) start(t *testing.T) {
	t.Helper()
	for _, operation := range []func(context.Context) error{
		harness.client.Ready,
		harness.client.Arm,
		harness.client.Start,
	} {
		if err := operation(harness.ctx); err != nil {
			t.Fatal(err)
		}
	}
}

func (harness *producerHarness) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-harness.done:
		return err
	case <-harness.ctx.Done():
		t.Fatalf("Run did not return: %v", harness.ctx.Err())
		return harness.ctx.Err()
	}
}

func nextRecord(
	t *testing.T,
	harness *producerHarness,
	want sensorproducer.FrameType,
) sensorproducer.Record {
	t.Helper()
	record, err := harness.client.Next(harness.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if record.Type != want {
		t.Fatalf("record type = %d, want %d", record.Type, want)
	}
	return record
}

func decodeMetadata[T any](t *testing.T, encoded []byte) T {
	t.Helper()
	var result T
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func validSourcePlan() multisensorcapture.SourcePlan {
	return multisensorcapture.SourcePlan{
		SourceID: testSourceID, Kind: multisensor.SourceCamera, Required: true,
		Argv: []string{"mmwcli", "sensor-producer", "fixed-frames"}, QueueSize: 4,
		Producer: multisensor.Producer{Name: "mmwcli-fixed-frames", Version: "1"},
		Limits:   multisensor.SourceLimits{MaxItems: 8, MaxItemBytes: 4, MaxPayloadBytes: 32},
		Payload:  multisensor.PayloadContract{Filename: "frames.bin", Format: "camera.raw.fixed.v1"},
		Clock: multisensor.Clock{
			ClockID: multisensor.DeliveryObservedClockID(testSourceID),
			TickHz:  1_000_000_000, TimestampSemantics: multisensor.TimestampDeliveryObserved,
		},
		SyncEventSemantics:  multisensorcapture.SyncEventSemanticsNone,
		ApplicationMetadata: multisensor.ApplicationMetadata{},
	}
}

func helperCommand(mode string) []string {
	return []string{os.Args[0], "-test.run=^TestFixedFrameCameraHelper$", "--", mode}
}

func helperMode(arguments []string) (string, bool) {
	for index, argument := range arguments {
		if argument == "--" && index+1 < len(arguments) {
			return arguments[index+1], true
		}
	}
	return "", false
}

func writeHelperBytes(payload []byte) {
	if _, err := os.Stdout.Write(payload); err != nil && !errors.Is(err, os.ErrClosed) {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(4)
	}
}
