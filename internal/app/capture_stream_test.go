package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"mmwcli/internal/capturefile"
	"mmwcli/internal/capturestream"
	"mmwcli/internal/dca"
	"mmwcli/internal/debugcapture"
	"mmwcli/internal/radar"
	"mmwcli/internal/session"
)

const (
	appStreamHeaderSize = 80
	appStreamDomain     = "mmwcli.capture_stream.record.v1\x00"
)

type appStreamRecord struct {
	kind     uint16
	sequence uint64
	item     uint64
	payload  []byte
}

func TestCaptureStreamFlagScopeAndOSFileBoundary(t *testing.T) {
	for _, command := range []string{"studio-cli", "debug-cli"} {
		var stdout, stderr bytes.Buffer
		if code := Run([]string{command, "capture", "--help"}, &stdout, &stderr); code != 0 {
			t.Fatalf("%s help exit code = %d, stderr=%s", command, code, stderr.String())
		}
		if !strings.Contains(stdout.String()+stderr.String(), "-stream") {
			t.Fatalf("%s capture help omits stream: %s%s", command, stdout.String(), stderr.String())
		}
	}

	config := writeValidConfig(t)
	output := filepath.Join(t.TempDir(), "capture-session")
	for _, arguments := range [][]string{
		{
			"studio-cli", "capture", config, output,
			"--port", "__mmwcli_missing_stream_port__", "--stream",
		},
		{
			"debug-cli", "capture", config, output,
			"--enhanced-port", "COM3", "--bss-fw", "bss.bin", "--mss-fw", "mss.bin",
			"--d2xx-serial", "FT1234", "--stream",
		},
	} {
		var stdout, stderr bytes.Buffer
		if code := Run(arguments, &stdout, &stderr); code != 2 {
			t.Fatalf("%s non-file stdout exit code = %d, stdout=%s stderr=%s", arguments[0], code, stdout.String(), stderr.String())
		}
		if !strings.Contains(stderr.String(), "--stream requires stdout to be an OS file or pipe") {
			t.Fatalf("%s missing stdout ownership error: %s", arguments[0], stderr.String())
		}
	}
	assertPathDoesNotExist(t, output)
	assertPathDoesNotExist(t, output+".part")
}

func TestStudioCaptureStreamFailureEmitsAbortAndEOF(t *testing.T) {
	config := writeValidConfig(t)
	configSnapshot, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	output := filepath.Join(root, "capture-session")
	missingPort := filepath.Join(root, "missing-serial-port")
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	readResult := make(chan struct {
		payload []byte
		err     error
	}, 1)
	go func() {
		payload, readErr := io.ReadAll(reader)
		readResult <- struct {
			payload []byte
			err     error
		}{payload: payload, err: readErr}
	}()

	var stderr bytes.Buffer
	code := Run(
		[]string{"studio-cli", "capture", config, output, "--port", missingPort, "--stream"},
		writer,
		&stderr,
	)
	var result struct {
		payload []byte
		err     error
	}
	select {
	case result = <-readResult:
	case <-time.After(5 * time.Second):
		_ = writer.Close()
		_ = reader.Close()
		t.Fatal("capture stream stdout did not close at EOF")
	}
	_ = reader.Close()

	if code != 4 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if result.err != nil {
		t.Fatalf("read capture stream: %v", result.err)
	}
	records := decodeAppCaptureStream(t, result.payload)
	if len(records) != 3 {
		t.Fatalf("record count = %d, want SESSION, CONFIG, ABORT", len(records))
	}
	for index, wantKind := range []uint16{1, 2, 5} {
		record := records[index]
		if record.kind != wantKind || record.sequence != uint64(index) || record.item != 0 {
			t.Fatalf(
				"record %d = kind %d sequence %d item %d",
				index,
				record.kind,
				record.sequence,
				record.item,
			)
		}
	}
	if !bytes.Equal(records[1].payload, configSnapshot) {
		t.Fatal("capture stream CONFIG differs from the planned CFG snapshot")
	}
	var sessionRecord map[string]any
	if err := json.Unmarshal(records[0].payload, &sessionRecord); err != nil {
		t.Fatal(err)
	}
	if sessionRecord["schema"] != "mmwcli.capture_stream.v1" ||
		sessionRecord["mode"] != "studio-cli" {
		t.Fatalf("SESSION identity = %+v", sessionRecord)
	}
	var terminal map[string]any
	if err := json.Unmarshal(records[2].payload, &terminal); err != nil {
		t.Fatal(err)
	}
	emptyDigest := sha256.Sum256(nil)
	if terminal["schema"] != "mmwcli.capture_stream_terminal.v1" ||
		terminal["outcome"] != "abort" ||
		terminal["frames"] != float64(0) ||
		terminal["adc_bytes"] != float64(0) ||
		terminal["adc_sha256"] != hex.EncodeToString(emptyDigest[:]) ||
		terminal["reason_code"] != "capture_failed" {
		t.Fatalf("ABORT = %+v", terminal)
	}
	if !strings.Contains(stderr.String(), "CFG preflight:") ||
		!strings.Contains(stderr.String(), "failed:") {
		t.Fatalf("stderr omits plan or failure diagnostics: %s", stderr.String())
	}
	assertPathDoesNotExist(t, output)
	info, err := os.Stat(output + ".part")
	if err != nil || !info.IsDir() {
		t.Fatalf("--stream did not imply a staged session directory: %v", err)
	}
}

func TestDebugCaptureStreamFailureEmitsModeAbortAndEOF(t *testing.T) {
	config := writeDebugCaptureConfig(t, debugCaptureTestConfig)
	configSnapshot, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "capture-session")
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	readResult := make(chan struct {
		payload []byte
		err     error
	}, 1)
	go func() {
		payload, readErr := io.ReadAll(reader)
		readResult <- struct {
			payload []byte
			err     error
		}{payload: payload, err: readErr}
	}()

	var stderr bytes.Buffer
	var events []string
	wantErr := errors.New("injected capture failure")
	dependencies := preflightOnlyDebugCaptureDependencies(nil)
	dependencies.checkAssets = func(string, string) (debugcapture.Assets, error) {
		events = append(events, "assets")
		return debugcapture.Assets{}, nil
	}
	dependencies.checkNative = func() error {
		events = append(events, "native")
		return nil
	}
	dependencies.dialDCA = func(dca.Options) (debugCaptureDCA, error) {
		events = append(events, "dca")
		return &fakeDebugCaptureDCA{events: &events}, nil
	}
	dependencies.openController = func(
		context.Context,
		debugcapture.ControllerOptions,
	) (debugCaptureController, error) {
		events = append(events, "controller")
		return &fakeDebugCaptureController{events: &events}, nil
	}
	dependencies.runSession = func(
		_ context.Context,
		_ session.Radar,
		_ session.DCAControl,
		_ session.ReceiverFactory,
		_ radar.CapturePlan,
		captureOutput capturefile.Output,
		options session.Options,
	) (dca.CaptureStats, error) {
		events = append(events, "session")
		if options.Mirror == nil || !options.Mirror.BoundTo(captureOutput) {
			t.Fatal("debug stream Mirror is not bound to the authoritative session output")
		}
		return dca.CaptureStats{}, wantErr
	}
	captureErr := runDebugCaptureCaptureWithDependencies(
		append(debugCaptureArguments(config, output), "--stream"),
		writer,
		&stderr,
		dependencies,
	)
	var result struct {
		payload []byte
		err     error
	}
	select {
	case result = <-readResult:
	case <-time.After(5 * time.Second):
		_ = writer.Close()
		_ = reader.Close()
		t.Fatal("debug capture stream stdout did not close at EOF")
	}
	_ = reader.Close()

	if !errors.Is(captureErr, wantErr) {
		t.Fatalf("debug capture error = %v, want %v", captureErr, wantErr)
	}
	wantEvents := []string{"assets", "native", "dca", "controller", "session", "controller-close", "dca-close"}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("debug failure events = %v, want %v", events, wantEvents)
	}
	if result.err != nil {
		t.Fatalf("read debug capture stream: %v", result.err)
	}
	records := decodeAppCaptureStream(t, result.payload)
	if len(records) != 3 {
		t.Fatalf("debug stream record count = %d, want SESSION, CONFIG, ABORT", len(records))
	}
	for index, wantKind := range []uint16{1, 2, 5} {
		if records[index].kind != wantKind || records[index].sequence != uint64(index) || records[index].item != 0 {
			t.Fatalf("debug record %d = %+v, want kind=%d sequence=%d item=0", index, records[index], wantKind, index)
		}
	}
	if !bytes.Equal(records[1].payload, configSnapshot) {
		t.Fatal("debug capture stream CONFIG differs from the planned CFG snapshot")
	}
	var sessionRecord map[string]any
	if err := json.Unmarshal(records[0].payload, &sessionRecord); err != nil {
		t.Fatal(err)
	}
	if sessionRecord["schema"] != "mmwcli.capture_stream.v1" || sessionRecord["mode"] != "debug-cli" {
		t.Fatalf("debug SESSION = %+v", sessionRecord)
	}
	var terminal map[string]any
	if err := json.Unmarshal(records[2].payload, &terminal); err != nil {
		t.Fatal(err)
	}
	emptyDigest := sha256.Sum256(nil)
	if terminal["schema"] != "mmwcli.capture_stream_terminal.v1" ||
		terminal["outcome"] != "abort" ||
		terminal["frames"] != float64(0) ||
		terminal["adc_bytes"] != float64(0) ||
		terminal["adc_sha256"] != hex.EncodeToString(emptyDigest[:]) ||
		terminal["reason_code"] != "capture_failed" {
		t.Fatalf("debug ABORT = %+v", terminal)
	}
	if !strings.Contains(stderr.String(), "CFG preflight:") {
		t.Fatalf("debug stderr omits plan diagnostics: %s", stderr.String())
	}
	assertPathDoesNotExist(t, output)
	info, err := os.Stat(output + ".part")
	if err != nil || !info.IsDir() {
		t.Fatalf("debug capture did not stage a session directory: %v", err)
	}
}

func TestDebugCaptureStreamSuccessPublishesFramesThenCommitAfterCleanup(t *testing.T) {
	configContents := strings.Replace(
		debugCaptureTestConfig,
		"frameCfg 0 1 32 100 100 1 0",
		"frameCfg 0 1 1 1 10 1 0",
		1,
	)
	config := writeDebugCaptureConfig(t, configContents)
	configSnapshot, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	outputPath := filepath.Join(root, "capture-session")
	streamPath := filepath.Join(root, "capture-stream.bin")
	streamFile, err := os.Create(streamPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = streamFile.Close() })

	var events []string
	assertNoTerminal := func() { assertStreamHasFramesWithoutTerminal(t, streamFile) }
	dcaClient := &streamOrderDebugCaptureDCA{
		fakeDebugCaptureDCA: &fakeDebugCaptureDCA{events: &events},
		closeHook:           assertNoTerminal,
	}
	controller := &streamOrderDebugCaptureController{
		fakeDebugCaptureController: &fakeDebugCaptureController{events: &events},
		closeHook:                  assertNoTerminal,
	}
	dependencies := preflightOnlyDebugCaptureDependencies(nil)
	dependencies.checkAssets = func(string, string) (debugcapture.Assets, error) {
		events = append(events, "assets")
		return debugcapture.Assets{}, nil
	}
	dependencies.checkNative = func() error {
		events = append(events, "native")
		return nil
	}
	dependencies.dialDCA = func(dca.Options) (debugCaptureDCA, error) {
		events = append(events, "dca")
		return dcaClient, nil
	}
	dependencies.openController = func(
		context.Context,
		debugcapture.ControllerOptions,
	) (debugCaptureController, error) {
		events = append(events, "controller")
		return controller, nil
	}
	var adcBytes []byte
	var frameCount uint64
	dependencies.runSession = func(
		ctx context.Context,
		_ session.Radar,
		_ session.DCAControl,
		_ session.ReceiverFactory,
		plan radar.CapturePlan,
		captureOutput capturefile.Output,
		options session.Options,
	) (dca.CaptureStats, error) {
		events = append(events, "session")
		if options.Mirror == nil || !options.Mirror.BoundTo(captureOutput) {
			t.Fatal("debug stream Mirror is not bound to the authoritative session output")
		}
		if plan.ExpectedBytes <= 0 || plan.ExpectedBytes > 16<<20 {
			t.Fatalf("unexpected test capture size %d", plan.ExpectedBytes)
		}
		adcBytes = make([]byte, int(plan.ExpectedBytes))
		frameCount = uint64(plan.NumberOfFrames)
		for frameIndex := uint64(0); frameIndex < frameCount; frameIndex++ {
			start := int64(frameIndex) * plan.BytesPerFrame
			end := start + plan.BytesPerFrame
			frame := adcBytes[int(start):int(end)]
			for index := range frame {
				frame[index] = byte((int(frameIndex) + index) % 251)
			}
			written, writeErr := options.Mirror.WriteAt(frame, start)
			if writeErr != nil || written != len(frame) {
				return dca.CaptureStats{}, errors.Join(writeErr, io.ErrShortWrite)
			}
		}
		sealContext, cancelSeal := context.WithTimeout(context.Background(), options.MirrorSealTimeout)
		sealErr := options.Mirror.Seal(sealContext)
		cancelSeal()
		if sealErr != nil {
			return dca.CaptureStats{}, sealErr
		}
		if err := captureOutput.Truncate(plan.ExpectedBytes); err != nil {
			return dca.CaptureStats{}, err
		}
		if err := captureOutput.CommitContext(ctx); err != nil {
			return dca.CaptureStats{}, err
		}
		return dca.CaptureStats{OutputBytes: plan.ExpectedBytes}, nil
	}

	var stderr bytes.Buffer
	if err := runDebugCaptureCaptureWithDependencies(
		append(debugCaptureArguments(config, outputPath), "--stream"),
		streamFile,
		&stderr,
		dependencies,
	); err != nil {
		t.Fatal(err)
	}
	wantEvents := []string{"assets", "native", "dca", "controller", "session", "controller-close", "dca-close"}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("debug success events before terminal observation = %v, want %v", events, wantEvents)
	}
	if _, err := streamFile.Write([]byte{0}); err == nil {
		t.Fatal("successful capture stream stdout remained open after COMMIT")
	}
	streamBytes, err := os.ReadFile(streamPath)
	if err != nil {
		t.Fatal(err)
	}
	records := decodeAppCaptureStream(t, streamBytes)
	if len(records) != int(frameCount)+3 {
		t.Fatalf("record count = %d, want %d", len(records), frameCount+3)
	}
	if records[0].kind != 1 || records[0].sequence != 0 || records[0].item != 0 ||
		records[1].kind != 2 || records[1].sequence != 1 || records[1].item != 0 {
		t.Fatalf("stream prefix = %+v", records[:2])
	}
	if !bytes.Equal(records[1].payload, configSnapshot) {
		t.Fatal("successful debug stream CONFIG differs from the planned CFG snapshot")
	}
	var streamedADC []byte
	for frameIndex := uint64(0); frameIndex < frameCount; frameIndex++ {
		record := records[2+frameIndex]
		if record.kind != 3 || record.sequence != 2+frameIndex || record.item != frameIndex {
			t.Fatalf("FRAME %d = %+v", frameIndex, record)
		}
		streamedADC = append(streamedADC, record.payload...)
	}
	if !bytes.Equal(streamedADC, adcBytes) {
		t.Fatal("streamed FRAME payloads differ from authoritative ADC bytes")
	}
	commit := records[2+frameCount]
	if commit.kind != 4 || commit.sequence != 2+frameCount || commit.item != frameCount {
		t.Fatalf("COMMIT record = %+v", commit)
	}
	var terminal map[string]any
	if err := json.Unmarshal(commit.payload, &terminal); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(adcBytes)
	if terminal["outcome"] != "commit" ||
		terminal["frames"] != float64(frameCount) ||
		terminal["adc_bytes"] != float64(len(adcBytes)) ||
		terminal["adc_sha256"] != hex.EncodeToString(digest[:]) {
		t.Fatalf("COMMIT = %+v", terminal)
	}
	publishedADC, err := os.ReadFile(filepath.Join(outputPath, "adc.bin"))
	if err != nil || !bytes.Equal(publishedADC, adcBytes) {
		t.Fatalf("published ADC differs from streamed ADC: %v", err)
	}
	assertPathDoesNotExist(t, outputPath+".part")
	if !strings.Contains(stderr.String(), "CFG preflight:") ||
		!strings.Contains(stderr.String(), "capture complete:") {
		t.Fatalf("successful stream diagnostics omitted plan or stats: %s", stderr.String())
	}
}

func TestStreamAbortReasonPreservesSpecificFailureOverCancellation(t *testing.T) {
	cleanup := &session.CleanupError{Err: errors.New("cleanup")}
	for _, test := range []struct {
		name string
		err  error
		want capturestream.AbortReason
	}{
		{name: "backpressure", err: errors.Join(context.Canceled, capturestream.ErrMirrorBackpressure), want: capturestream.AbortBackpressure},
		{name: "integrity", err: errors.Join(context.Canceled, capturestream.ErrMirrorIntegrity), want: capturestream.AbortIntegrityFailed},
		{name: "cleanup", err: errors.Join(context.Canceled, cleanup), want: capturestream.AbortCleanupFailed},
		{name: "cancelled", err: context.Canceled, want: capturestream.AbortCancelled},
		{name: "deadline", err: context.DeadlineExceeded, want: capturestream.AbortCancelled},
		{name: "capture", err: errors.New("capture"), want: capturestream.AbortCaptureFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := streamAbortReason(test.err); got != test.want {
				t.Fatalf("streamAbortReason(%v) = %q, want %q", test.err, got, test.want)
			}
		})
	}
}

type streamOrderDebugCaptureDCA struct {
	*fakeDebugCaptureDCA
	closeHook func()
}

func (client *streamOrderDebugCaptureDCA) Close() error {
	err := client.fakeDebugCaptureDCA.Close()
	client.closeHook()
	return err
}

type streamOrderDebugCaptureController struct {
	*fakeDebugCaptureController
	closeHook func()
}

func (controller *streamOrderDebugCaptureController) Close() error {
	err := controller.fakeDebugCaptureController.Close()
	controller.closeHook()
	return err
}

func assertStreamHasFramesWithoutTerminal(t *testing.T, stream *os.File) {
	t.Helper()
	if err := stream.Sync(); err != nil {
		t.Fatal(err)
	}
	info, err := stream.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() <= 0 || info.Size() > 16<<20 {
		t.Fatalf("unexpected provisional stream size %d", info.Size())
	}
	payload := make([]byte, int(info.Size()))
	if _, err := stream.ReadAt(payload, 0); err != nil {
		t.Fatal(err)
	}
	sawFrame := false
	for _, record := range decodeAppCaptureStream(t, payload) {
		if record.kind == 3 {
			sawFrame = true
		}
		if record.kind == 4 || record.kind == 5 {
			t.Fatalf("terminal record %d was emitted before capture client cleanup", record.kind)
		}
	}
	if !sawFrame {
		t.Fatal("capture clients closed before the complete provisional FRAME was emitted")
	}
}

func decodeAppCaptureStream(t *testing.T, stream []byte) []appStreamRecord {
	t.Helper()
	var records []appStreamRecord
	for len(stream) != 0 {
		if len(stream) < appStreamHeaderSize {
			t.Fatalf("truncated stream header: %d bytes", len(stream))
		}
		header := stream[:appStreamHeaderSize]
		if string(header[:8]) != "MMWSTRM1" ||
			binary.LittleEndian.Uint16(header[8:10]) != 1 ||
			binary.LittleEndian.Uint16(header[10:12]) != appStreamHeaderSize {
			t.Fatalf("invalid stream header prefix: %x", header[:16])
		}
		payloadSize := binary.LittleEndian.Uint64(header[32:40])
		if payloadSize > uint64(len(stream)-appStreamHeaderSize) {
			t.Fatalf("record payload %d exceeds remaining stream %d", payloadSize, len(stream))
		}
		end := appStreamHeaderSize + int(payloadSize)
		payload := stream[appStreamHeaderSize:end]
		digestInput := append([]byte(appStreamDomain), header[:48]...)
		digestInput = append(digestInput, payload...)
		digest := sha256.Sum256(digestInput)
		if !bytes.Equal(header[48:80], digest[:]) {
			t.Fatal("capture stream record digest mismatch")
		}
		if binary.LittleEndian.Uint16(header[14:16]) != 0 ||
			binary.LittleEndian.Uint64(header[40:48]) != 0 {
			t.Fatal("capture stream flags or reserved field is nonzero")
		}
		records = append(records, appStreamRecord{
			kind:     binary.LittleEndian.Uint16(header[12:14]),
			sequence: binary.LittleEndian.Uint64(header[16:24]),
			item:     binary.LittleEndian.Uint64(header[24:32]),
			payload:  append([]byte(nil), payload...),
		})
		stream = stream[end:]
	}
	if len(records) == 0 {
		t.Fatal("capture stream is empty")
	}
	return records
}
