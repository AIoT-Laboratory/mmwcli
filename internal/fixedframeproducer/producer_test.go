package fixedframeproducer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"os"
	"os/exec"
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

func TestRunJPEGStreamsTwoCompleteImagesAndStopsCleanly(t *testing.T) {
	harness := newJPEGProducerHarness(t, "jpeg-two")
	harness.start(t)
	nextRecord(t, harness, sensorproducer.FrameSession)
	payloads := [][]byte{testJPEGFrame(0x20), testJPEGFrame(0xe0)}
	for index, wantPayload := range payloads {
		record := nextRecord(t, harness, sensorproducer.FrameItem)
		item := decodeMetadata[multisensorcapture.ProducerItemMetadata](t, record.Metadata)
		if item.ItemIndex != uint64(index) || !slices.Equal(record.Payload, wantPayload) {
			t.Fatalf("ITEM %d = %+v payload=%x", index, item, record.Payload)
		}
	}
	if err := harness.client.Stop(harness.ctx); err != nil {
		t.Fatal(err)
	}
	end := decodeMetadata[multisensorcapture.ProducerEndMetadata](
		t,
		nextRecord(t, harness, sensorproducer.FrameEnd).Metadata,
	)
	joined := append(append([]byte(nil), payloads[0]...), payloads[1]...)
	digest := sha256.Sum256(joined)
	if end.ItemCount != 2 || end.PayloadBytes != uint64(len(joined)) ||
		end.PayloadSHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("END metadata = %+v", end)
	}
	nextRecord(t, harness, sensorproducer.FrameEOF)
	if err := harness.client.Wait(harness.ctx); err != nil {
		t.Fatal(err)
	}
	if err := harness.wait(t); err != nil {
		t.Fatal(err)
	}
}

func TestRunJPEGRejectsNonJPEGChildOutput(t *testing.T) {
	harness := newJPEGProducerHarness(t, "jpeg-invalid")
	harness.start(t)
	nextRecord(t, harness, sensorproducer.FrameSession)
	failure := decodeMetadata[sensorproducer.ErrorMetadata](
		t,
		nextRecord(t, harness, sensorproducer.FrameError).Metadata,
	)
	if !strings.Contains(failure.Message, "SOI marker") {
		t.Fatalf("ERROR message = %q", failure.Message)
	}
	nextRecord(t, harness, sensorproducer.FrameEOF)
	if err := harness.client.Wait(harness.ctx); err == nil {
		t.Fatal("client Wait unexpectedly succeeded")
	}
	if err := harness.wait(t); err == nil || !strings.Contains(err.Error(), "SOI marker") {
		t.Fatalf("RunJPEG error = %v", err)
	}
}

func TestRunJPEGNeverPublishesAFrameCompletedBeforeStart(t *testing.T) {
	discarded := make(chan uint64, 1)
	harness := newJPEGProducerHarnessWithHooks(
		t,
		"jpeg-preroll",
		&producerHooks{preStartDiscarded: func(sequence uint64) {
			discarded <- sequence
		}},
		validJPEGSourcePlan(),
		nil,
	)
	harness.readyAndArm(t)
	select {
	case sequence := <-discarded:
		if sequence != 1 {
			t.Fatalf("discarded sequence = %d, want 1", sequence)
		}
	case <-harness.ctx.Done():
		t.Fatalf("pre-START JPEG was not parsed and discarded: %v", harness.ctx.Err())
	}
	harness.startCapture(t)
	nextRecord(t, harness, sensorproducer.FrameSession)
	item := nextRecord(t, harness, sensorproducer.FrameItem)
	if !slices.Equal(item.Payload, testJPEGFrame(0xf0)) {
		t.Fatalf("first ITEM is not the post-START JPEG: %x", item.Payload)
	}
	if err := harness.client.Stop(harness.ctx); err != nil {
		t.Fatal(err)
	}
	nextRecord(t, harness, sensorproducer.FrameEnd)
	nextRecord(t, harness, sensorproducer.FrameEOF)
	if err := harness.client.Wait(harness.ctx); err != nil {
		t.Fatal(err)
	}
	if err := harness.wait(t); err != nil {
		t.Fatal(err)
	}
}

func TestRunJPEGReportsTruncatedAndOversizeImages(t *testing.T) {
	tests := []struct {
		name         string
		mode         string
		maxItemBytes uint64
		message      string
	}{
		{name: "truncated", mode: "jpeg-truncated", maxItemBytes: 1 << 20, message: "truncated JPEG"},
		{name: "oversize", mode: "jpeg-oversize", maxItemBytes: 128, message: "exceeds 128 bytes"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := validJPEGSourcePlan()
			plan.Limits.MaxItemBytes = test.maxItemBytes
			plan.Limits.MaxPayloadBytes = test.maxItemBytes * plan.Limits.MaxItems
			harness := newJPEGProducerHarnessWithHooks(t, test.mode, nil, plan, nil)
			harness.start(t)
			nextRecord(t, harness, sensorproducer.FrameSession)
			failure := decodeMetadata[sensorproducer.ErrorMetadata](
				t,
				nextRecord(t, harness, sensorproducer.FrameError).Metadata,
			)
			if !strings.Contains(failure.Message, test.message) {
				t.Fatalf("ERROR message = %q, want %q", failure.Message, test.message)
			}
			nextRecord(t, harness, sensorproducer.FrameEOF)
			if err := harness.client.Wait(harness.ctx); err == nil {
				t.Fatal("client Wait unexpectedly succeeded")
			}
			if err := harness.wait(t); err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("RunJPEG error = %v, want %q", err, test.message)
			}
		})
	}
}

func TestRunJPEGReadsRealFFmpegImage2Pipe(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	command := []string{
		ffmpeg,
		"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=32x24:rate=10",
		"-an", "-c:v", "mjpeg", "-q:v", "5", "-f", "image2pipe", "pipe:1",
	}
	harness := newJPEGProducerHarnessWithHooks(
		t, "", nil, validJPEGSourcePlan(), command,
	)
	harness.start(t)
	nextRecord(t, harness, sensorproducer.FrameSession)
	for index := range 2 {
		record := nextRecord(t, harness, sensorproducer.FrameItem)
		if _, err := jpeg.Decode(bytes.NewReader(record.Payload)); err != nil {
			t.Fatalf("FFmpeg ITEM %d is not a complete JPEG: %v", index, err)
		}
	}
	if err := harness.client.Stop(harness.ctx); err != nil {
		t.Fatal(err)
	}
	nextRecord(t, harness, sensorproducer.FrameEnd)
	nextRecord(t, harness, sensorproducer.FrameEOF)
	if err := harness.client.Wait(harness.ctx); err != nil {
		t.Fatal(err)
	}
	if err := harness.wait(t); err != nil {
		t.Fatal(err)
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
		context.Background(), plan, 4, helperCommand("block", "unused"),
		bytes.NewReader(nil), io.Discard, io.Discard,
	)
	if err == nil || !strings.Contains(err.Error(), "delivery_observed") {
		t.Fatalf("Run error = %v", err)
	}
}

func TestFixedFrameCameraHelper(t *testing.T) {
	mode, releasePath, ok := helperArguments(os.Args)
	if !ok {
		return
	}
	_, _ = fmt.Fprintln(os.Stderr, "fixed-frame camera helper")
	if mode == "jpeg-preroll" {
		writeHelperBytes(testJPEGFrame(0x10))
	} else {
		waitForHelperRelease(releasePath)
	}
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
	case "jpeg-two":
		writeHelperBytes(testJPEGFrame(0x20))
		writeHelperBytes(testJPEGFrame(0xe0))
		time.Sleep(time.Hour)
	case "jpeg-invalid":
		writeHelperBytes([]byte("not-a-jpeg"))
		time.Sleep(time.Hour)
	case "jpeg-preroll":
		waitForHelperRelease(releasePath)
		writeHelperBytes(testJPEGFrame(0xf0))
		time.Sleep(time.Hour)
	case "jpeg-truncated":
		frame := testJPEGFrame(0x80)
		writeHelperBytes(frame[:len(frame)-2])
	case "jpeg-oversize":
		writeHelperBytes(testJPEGFrame(0x80))
		time.Sleep(time.Hour)
	default:
		_, _ = fmt.Fprintln(os.Stderr, "unknown helper mode", mode)
		os.Exit(2)
	}
	os.Exit(0)
}

type producerHarness struct {
	ctx         context.Context
	cancel      context.CancelFunc
	client      *sensorproducer.Client
	plan        multisensorcapture.SourcePlan
	stderr      bytes.Buffer
	done        chan error
	releasePath string
}

func newProducerHarness(t *testing.T, mode string) *producerHarness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	producerInput, controlOutput := io.Pipe()
	recordInput, producerOutput := io.Pipe()
	harness := &producerHarness{
		ctx: ctx, cancel: cancel, plan: validSourcePlan(), done: make(chan error, 1),
		releasePath: filepath.Join(t.TempDir(), "release"),
	}
	go func() {
		err := Run(
			ctx, harness.plan, uint64(len(testFrame0)), helperCommand(mode, harness.releasePath),
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

func newJPEGProducerHarness(t *testing.T, mode string) *producerHarness {
	return newJPEGProducerHarnessWithHooks(t, mode, nil, validJPEGSourcePlan(), nil)
}

func newJPEGProducerHarnessWithHooks(
	t *testing.T,
	mode string,
	hooks *producerHooks,
	plan multisensorcapture.SourcePlan,
	command []string,
) *producerHarness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	producerInput, controlOutput := io.Pipe()
	recordInput, producerOutput := io.Pipe()
	harness := &producerHarness{
		ctx: ctx, cancel: cancel, plan: plan, done: make(chan error, 1),
		releasePath: filepath.Join(t.TempDir(), "release"),
	}
	if command == nil {
		command = helperCommand(mode, harness.releasePath)
	}
	go func() {
		err := run(
			ctx,
			harness.plan,
			harness.plan.Limits.MaxItemBytes,
			true,
			command,
			producerInput,
			producerOutput,
			&harness.stderr,
			hooks,
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
	harness.readyAndArm(t)
	harness.startCapture(t)
}

func (harness *producerHarness) readyAndArm(t *testing.T) {
	t.Helper()
	for _, operation := range []func(context.Context) error{
		harness.client.Ready,
		harness.client.Arm,
	} {
		if err := operation(harness.ctx); err != nil {
			t.Fatal(err)
		}
	}
}

func (harness *producerHarness) startCapture(t *testing.T) {
	t.Helper()
	if err := harness.client.Start(harness.ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(harness.releasePath, []byte("start"), 0o600); err != nil {
		t.Fatal(err)
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
		Producer: multisensor.Producer{Name: ProducerName, Version: ProducerVersion},
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

func validJPEGSourcePlan() multisensorcapture.SourcePlan {
	plan := validSourcePlan()
	plan.Argv = []string{"mmwcli", "sensor-producer", "jpeg-stream"}
	plan.Producer = multisensor.Producer{Name: JPEGProducerName, Version: JPEGProducerVersion}
	plan.Limits = multisensor.SourceLimits{
		MaxItems: 8, MaxItemBytes: 1 << 20, MaxPayloadBytes: 8 << 20,
	}
	plan.Payload = multisensor.PayloadContract{Filename: "frames.jpgs", Format: JPEGFormat}
	return plan
}

func testJPEGFrame(level uint8) []byte {
	imageData := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for y := range 2 {
		for x := range 2 {
			imageData.SetRGBA(x, y, color.RGBA{R: level, G: level, B: level, A: 0xff})
		}
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, imageData, &jpeg.Options{Quality: 80}); err != nil {
		panic(err)
	}
	return encoded.Bytes()
}

func helperCommand(mode, releasePath string) []string {
	return []string{
		os.Args[0], "-test.run=^TestFixedFrameCameraHelper$", "--", mode, releasePath,
	}
}

func helperArguments(arguments []string) (string, string, bool) {
	for index, argument := range arguments {
		if argument == "--" && index+2 < len(arguments) {
			return arguments[index+1], arguments[index+2], true
		}
	}
	return "", "", false
}

func waitForHelperRelease(path string) {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	_, _ = fmt.Fprintln(os.Stderr, "timed out waiting for START release")
	os.Exit(5)
}

func writeHelperBytes(payload []byte) {
	if _, err := os.Stdout.Write(payload); err != nil && !errors.Is(err, os.ErrClosed) {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(4)
	}
}
