package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"mmwcli/internal/multisensor"
	"mmwcli/internal/multisensorcapture"
	"mmwcli/internal/sensorproducer"
)

const (
	appProducerSessionID = "session-app-fixed-frames"
	appProducerSourceID  = "camera-0"
)

var (
	appProducerFrame0 = []byte{0x01, 0x02, 0x03, 0x04}
	appProducerFrame1 = []byte{0x10, 0x20, 0x30, 0x40}
)

func TestSensorProducerFixedFramesStreamsTwoFramesThenStopAndEOF(t *testing.T) {
	harness := newAppProducerHarness(t, "two")
	harness.start(t)

	session := harness.next(t, sensorproducer.FrameSession)
	var metadata multisensorcapture.ProducerSessionMetadata
	if err := json.Unmarshal(session.Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Producer != harness.plan.Sources[0].Producer ||
		metadata.Payload != harness.plan.Sources[0].Payload ||
		metadata.Clock != harness.plan.Sources[0].Clock ||
		metadata.Limits != harness.plan.Sources[0].Limits {
		t.Fatalf("SESSION metadata does not match selected plan source: %+v", metadata)
	}
	for _, want := range [][]byte{appProducerFrame0, appProducerFrame1} {
		record := harness.next(t, sensorproducer.FrameItem)
		if !slices.Equal(record.Payload, want) {
			t.Fatalf("ITEM payload = %x, want %x", record.Payload, want)
		}
	}
	if err := harness.client.Stop(harness.ctx); err != nil {
		t.Fatal(err)
	}
	harness.next(t, sensorproducer.FrameEnd)
	harness.next(t, sensorproducer.FrameEOF)
	if err := harness.client.Wait(harness.ctx); err != nil {
		t.Fatal(err)
	}
	if err := harness.wait(t); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(harness.stderr.String(), "app fixed-frame camera helper") {
		t.Fatalf("stderr = %q", harness.stderr.String())
	}
}

func TestSensorProducerFixedFramesReportsChildExitAndSupportsCancel(t *testing.T) {
	t.Run("child error", func(t *testing.T) {
		harness := newAppProducerHarness(t, "failure")
		harness.start(t)
		harness.next(t, sensorproducer.FrameSession)
		failure := harness.next(t, sensorproducer.FrameError)
		if !strings.Contains(string(failure.Metadata), "failed before STOP") {
			t.Fatalf("ERROR metadata = %s", failure.Metadata)
		}
		harness.next(t, sensorproducer.FrameEOF)
		if err := harness.client.Wait(harness.ctx); err == nil {
			t.Fatal("client Wait unexpectedly succeeded")
		}
		if err := harness.wait(t); err == nil || !strings.Contains(err.Error(), "failed before STOP") {
			t.Fatalf("fixed-frames error = %v", err)
		}
	})

	t.Run("cancel", func(t *testing.T) {
		harness := newAppProducerHarness(t, "block")
		harness.start(t)
		harness.next(t, sensorproducer.FrameSession)
		if err := harness.client.Cancel(harness.ctx); err != nil {
			t.Fatal(err)
		}
		harness.next(t, sensorproducer.FrameEOF)
		if err := harness.client.Wait(harness.ctx); err != nil {
			t.Fatal(err)
		}
		if err := harness.wait(t); err != nil {
			t.Fatal(err)
		}
	})
}

func TestSensorProducerFixedFramesIsDiscoverableAndHasNoAlias(t *testing.T) {
	tests := []struct {
		arguments []string
		code      int
		contains  string
	}{
		{arguments: []string{"help"}, code: 0, contains: "mmwcli sensor-producer fixed-frames"},
		{arguments: []string{"sensor-producer", "--help"}, code: 0, contains: "usage: mmwcli sensor-producer fixed-frames"},
		{arguments: []string{"sensor-producer", "fixed-frames", "--help"}, code: 0, contains: "usage: mmwcli sensor-producer fixed-frames"},
		{arguments: []string{"sensor-producer", "jpeg-stream", "--help"}, code: 0, contains: "usage: mmwcli sensor-producer jpeg-stream"},
		{arguments: []string{"sensor-producer"}, code: 2, contains: "requires fixed-frames"},
		{arguments: []string{"sensor-producer", "frames"}, code: 2, contains: "unknown sensor-producer command"},
		{arguments: []string{"fixed-frames"}, code: 2, contains: "unknown command"},
	}
	for _, test := range tests {
		var stdout, stderr bytes.Buffer
		if code := Run(test.arguments, &stdout, &stderr); code != test.code {
			t.Fatalf("Run(%v) code = %d, want %d", test.arguments, code, test.code)
		}
		output := stdout.String() + stderr.String()
		if !strings.Contains(output, test.contains) {
			t.Fatalf("Run(%v) output = %q, want %q", test.arguments, output, test.contains)
		}
	}
}

func TestSensorProducerFixedFramesRequiresExactPlanSourceBeforeChild(t *testing.T) {
	planPath, _ := writeAppProducerPlan(t)
	var stdout, stderr bytes.Buffer
	err := runSensorProducer(
		[]string{
			"fixed-frames", "--plan", planPath, "--source", "camera-alias",
			"--frame-bytes", "4", "--", os.Args[0],
		},
		bytes.NewReader(nil),
		&stdout,
		&stderr,
	)
	if err == nil || !strings.Contains(err.Error(), `no exact source_id "camera-alias"`) {
		t.Fatalf("runSensorProducer error = %v", err)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("preflight wrote stdout=%x stderr=%q", stdout.Bytes(), stderr.String())
	}
}

func TestAppFixedFrameCameraHelper(t *testing.T) {
	mode, releasePath, ok := appProducerHelperArguments(os.Args)
	if !ok {
		return
	}
	_, _ = fmt.Fprintln(os.Stderr, "app fixed-frame camera helper")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(releasePath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			os.Exit(5)
		}
		time.Sleep(5 * time.Millisecond)
	}
	switch mode {
	case "two":
		appProducerWrite(appProducerFrame0)
		appProducerWrite(appProducerFrame1)
		time.Sleep(time.Hour)
	case "failure":
		os.Exit(3)
	case "block":
		time.Sleep(time.Hour)
	default:
		os.Exit(2)
	}
	os.Exit(0)
}

type appProducerHarness struct {
	ctx         context.Context
	cancel      context.CancelFunc
	client      *sensorproducer.Client
	plan        multisensorcapture.Plan
	stderr      bytes.Buffer
	done        chan error
	releasePath string
}

func newAppProducerHarness(t *testing.T, mode string) *appProducerHarness {
	t.Helper()
	planPath, plan := writeAppProducerPlan(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	producerInput, controlOutput := io.Pipe()
	recordInput, producerOutput := io.Pipe()
	harness := &appProducerHarness{
		ctx: ctx, cancel: cancel, plan: plan, done: make(chan error, 1),
		releasePath: filepath.Join(t.TempDir(), "release"),
	}
	arguments := []string{
		"fixed-frames", "--plan", planPath, "--source", appProducerSourceID,
		"--frame-bytes", "4", "--",
	}
	arguments = append(arguments, appProducerHelperCommand(mode, harness.releasePath)...)
	go func() {
		err := runSensorProducer(arguments, producerInput, producerOutput, &harness.stderr)
		_ = producerInput.Close()
		_ = producerOutput.Close()
		harness.done <- err
	}()
	client, err := sensorproducer.NewClient(
		controlOutput,
		recordInput,
		appProducerSessionID,
		appProducerSourceID,
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

func (harness *appProducerHarness) start(t *testing.T) {
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
	if err := os.WriteFile(harness.releasePath, []byte("start"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (harness *appProducerHarness) next(
	t *testing.T,
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

func (harness *appProducerHarness) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-harness.done:
		return err
	case <-harness.ctx.Done():
		t.Fatalf("sensor-producer command did not return: %v", harness.ctx.Err())
		return harness.ctx.Err()
	}
}

func writeAppProducerPlan(t *testing.T) (string, multisensorcapture.Plan) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "multisensor-plan.json")
	source := multisensorcapture.SourcePlan{
		SourceID: appProducerSourceID, Kind: multisensor.SourceCamera, Required: true,
		Argv: []string{
			"mmwcli", "sensor-producer", "fixed-frames", "--plan", path,
			"--source", appProducerSourceID, "--frame-bytes", "4", "--", "camera-command",
		},
		QueueSize: 4,
		Producer:  multisensor.Producer{Name: "mmwcli-fixed-frames", Version: "1"},
		Limits: multisensor.SourceLimits{
			MaxItems: 8, MaxItemBytes: 4, MaxPayloadBytes: 32,
		},
		Payload: multisensor.PayloadContract{Filename: "frames.bin", Format: "camera.raw.fixed.v1"},
		Clock: multisensor.Clock{
			ClockID: multisensor.DeliveryObservedClockID(appProducerSourceID),
			TickHz:  1_000_000_000, TimestampSemantics: multisensor.TimestampDeliveryObserved,
		},
		SyncEventSemantics:  multisensorcapture.SyncEventSemanticsNone,
		ApplicationMetadata: multisensor.ApplicationMetadata{},
	}
	plan := multisensorcapture.Plan{
		Schema: multisensorcapture.PlanSchema, Sources: []multisensorcapture.SourcePlan{source},
		ApplicationMetadata: multisensor.ApplicationMetadata{},
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, plan
}

func appProducerHelperCommand(mode, releasePath string) []string {
	return []string{
		os.Args[0], "-test.run=^TestAppFixedFrameCameraHelper$", "--", mode, releasePath,
	}
}

func appProducerHelperArguments(arguments []string) (string, string, bool) {
	for index, argument := range arguments {
		if argument == "--" && index+2 < len(arguments) {
			return arguments[index+1], arguments[index+2], true
		}
	}
	return "", "", false
}

func appProducerWrite(payload []byte) {
	if _, err := os.Stdout.Write(payload); err != nil && !errors.Is(err, os.ErrClosed) {
		os.Exit(4)
	}
}
