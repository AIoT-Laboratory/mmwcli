package sensorproducer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestProcessAdapterTransfersRawPayloadAndCleanEOF(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var stderr bytes.Buffer
	process, err := StartProcess(
		ctx,
		helperCommand("happy"),
		"session-01",
		"camera.left",
		ProcessOptions{Stderr: &stderr, QueueSize: 4},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = process.Kill()
		_ = process.Wait(context.Background())
	}()
	client := process.Client()
	for _, operation := range []func(context.Context) error{client.Ready, client.Arm, client.Start} {
		if err := operation(ctx); err != nil {
			t.Fatal(err)
		}
	}

	session := nextRecord(t, ctx, client)
	if session.Type != FrameSession || string(session.Metadata) != `{"clock":"monotonic"}` {
		t.Fatalf("SESSION = %+v", session)
	}
	item := nextRecord(t, ctx, client)
	wantPayload := []byte{0, 1, 0xff, 0, '\n', '{', '}'}
	if item.Type != FrameItem || item.SessionID != "session-01" || item.SourceID != "camera.left" ||
		string(item.Metadata) != `{"index":0,"format":"gray8"}` || !slices.Equal(item.Payload, wantPayload) {
		t.Fatalf("ITEM = %+v", item)
	}
	if bytes.Contains(item.Payload, []byte("base64")) {
		t.Fatal("raw payload unexpectedly contains base64 text")
	}
	if err := client.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if record := nextRecord(t, ctx, client); record.Type != FrameEnd {
		t.Fatalf("terminal record = %+v", record)
	}
	if record := nextRecord(t, ctx, client); record.Type != FrameEOF {
		t.Fatalf("EOF record = %+v", record)
	}
	if err := process.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Next(ctx); !errors.Is(err, io.EOF) {
		t.Fatalf("Next after EOF = %v", err)
	}
	if !strings.Contains(stderr.String(), "fake sensor producer started") {
		t.Fatalf("injected stderr = %q", stderr.String())
	}
}

func TestProcessAdapterCancelAndDeadlineCleanUpChild(t *testing.T) {
	t.Run("matched CANCEL", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		process, err := StartProcess(ctx, helperCommand("happy"), "session-cancel", "camera.left", ProcessOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if err := process.Client().Ready(ctx); err != nil {
			t.Fatal(err)
		}
		if err := process.Cancel(ctx); err != nil {
			t.Fatal(err)
		}
		if process.Client().Phase() != PhaseComplete {
			t.Fatalf("phase = %d", process.Client().Phase())
		}
		if err := process.Wait(ctx); err != nil {
			t.Fatalf("first Wait: %v", err)
		}
		if err := process.Wait(ctx); err != nil {
			t.Fatalf("repeated Wait: %v", err)
		}
	})

	t.Run("already terminal CANCEL is not a convergence failure", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		process, err := StartProcess(ctx, helperCommand("happy"), "session-terminal", "camera.left", ProcessOptions{})
		if err != nil {
			t.Fatal(err)
		}
		client := process.Client()
		for _, operation := range []func(context.Context) error{client.Ready, client.Arm, client.Start} {
			if err := operation(ctx); err != nil {
				t.Fatal(err)
			}
		}
		_ = nextRecord(t, ctx, client)
		_ = nextRecord(t, ctx, client)
		if err := client.Stop(ctx); err != nil {
			t.Fatal(err)
		}
		_ = nextRecord(t, ctx, client)
		_ = nextRecord(t, ctx, client)
		if err := process.Wait(ctx); err != nil {
			t.Fatal(err)
		}
		if err := process.Cancel(ctx); err != nil {
			t.Fatalf("Cancel after terminal exit: %v", err)
		}
		expired, expire := context.WithCancel(context.Background())
		expire()
		if err := process.Wait(expired); err != nil {
			t.Fatalf("Wait with expired context after reap: %v", err)
		}
	})

	t.Run("rejected CANCEL with a reaped child is an ordinary source failure", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		process, err := StartProcess(ctx, helperCommand("cancel-rejected"), "session-rejected", "camera.left", ProcessOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if err := process.Client().Ready(ctx); err != nil {
			t.Fatal(err)
		}
		err = process.Cancel(ctx)
		if err == nil || !strings.Contains(err.Error(), "reject cancellation") {
			t.Fatalf("Cancel error = %v", err)
		}
		if IsConvergenceError(err) {
			t.Fatalf("reaped cancellation rejection was misclassified: %v", err)
		}
		if err := process.Wait(ctx); err == nil || IsConvergenceError(err) {
			t.Fatalf("repeated Wait error = %v", err)
		}
	})

	t.Run("deadline interrupts blocked ACK", func(t *testing.T) {
		lifetime, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		process, err := StartProcess(lifetime, helperCommand("noack"), "session-timeout", "camera.left", ProcessOptions{})
		if err != nil {
			t.Fatal(err)
		}
		deadline, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		started := time.Now()
		err = process.Cancel(deadline)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Cancel error = %v", err)
		}
		if !IsConvergenceError(err) {
			t.Fatalf("Cancel should preserve missed convergence deadline: %v", err)
		}
		if elapsed := time.Since(started); elapsed > 2*time.Second {
			t.Fatalf("Cancel cleanup took %s", elapsed)
		}
		select {
		case <-process.done:
		case <-time.After(2 * time.Second):
			t.Fatal("child was not reaped after cancellation")
		}
		if reapedErr := process.Wait(context.Background()); IsConvergenceError(reapedErr) {
			t.Fatalf("reaped child retained convergence failure: %v", reapedErr)
		}
	})
}

func TestProcessWaitDeadlineReportsUnprovenConvergence(t *testing.T) {
	lifetime, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	process, err := StartProcess(lifetime, helperCommand("noack"), "session-wait-timeout", "camera.left", ProcessOptions{})
	if err != nil {
		t.Fatal(err)
	}
	deadline, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = process.Wait(deadline)
	if !errors.Is(err, context.DeadlineExceeded) || !IsConvergenceError(err) {
		t.Fatalf("Wait error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Wait exceeded its deadline: %s", elapsed)
	}
	// Windows may report access denied when the child exits between Kill and
	// TerminateProcess. Reaping within the next bounded wait is authoritative.
	_ = process.Kill()
	reaped, reapedCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer reapedCancel()
	if err := process.Wait(reaped); IsConvergenceError(err) {
		t.Fatalf("killed child was not reaped: %v", err)
	}
}

func TestProcessAdapterRejectsTransportEOFWithoutEOFRecord(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	process, err := StartProcess(ctx, helperCommand("missing-eof"), "session-bad-eof", "camera.left", ProcessOptions{})
	if err != nil {
		t.Fatal(err)
	}
	client := process.Client()
	for _, operation := range []func(context.Context) error{client.Ready, client.Arm, client.Start} {
		if err := operation(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if record := nextRecord(t, ctx, client); record.Type != FrameSession {
		t.Fatalf("SESSION = %+v", record)
	}
	if err := process.Wait(ctx); !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), "before an EOF frame") {
		t.Fatalf("Wait error = %v", err)
	}
}

func TestSensorProducerHelperProcess(t *testing.T) {
	mode, ok := helperMode(os.Args)
	if !ok {
		return
	}
	if err := runSensorProducerHelper(mode); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

func helperCommand(mode string) []string {
	return []string{os.Args[0], "-test.run=^TestSensorProducerHelperProcess$", "--", mode}
}

func helperMode(arguments []string) (string, bool) {
	for index, argument := range arguments {
		if argument == "--" && index+1 < len(arguments) {
			return arguments[index+1], true
		}
	}
	return "", false
}

func runSensorProducerHelper(mode string) error {
	_, _ = fmt.Fprintln(os.Stderr, "fake sensor producer started")
	controls, err := NewControlDecoder(os.Stdin)
	if err != nil {
		return err
	}
	frames, err := NewEncoder(os.Stdout)
	if err != nil {
		return err
	}
	var sessionID, sourceID string
	var expectedControlSeq uint64 = 1
	var dataSeq uint64
	for {
		control, err := controls.Read()
		if err != nil {
			return err
		}
		if sessionID == "" {
			sessionID, sourceID = control.SessionID, control.SourceID
		}
		if control.SessionID != sessionID || control.SourceID != sourceID || control.Seq != expectedControlSeq {
			return fmt.Errorf("unexpected control: %+v", control)
		}
		expectedControlSeq++
		if mode == "noack" {
			_, err := controls.Read()
			return err
		}
		accepted := !(mode == "cancel-rejected" && control.Command == CommandCancel)
		message := ""
		if !accepted {
			message = "reject cancellation"
		}
		ack, err := NewACKRecord(control, accepted, message)
		if err != nil {
			return err
		}
		if err := frames.Write(ack); err != nil {
			return err
		}
		switch control.Command {
		case CommandStart:
			dataSeq++
			if err := frames.Write(helperRecord(FrameSession, sessionID, sourceID, dataSeq, `{"clock":"monotonic"}`, nil)); err != nil {
				return err
			}
			if mode == "missing-eof" {
				return nil
			}
			dataSeq++
			payload := []byte{0, 1, 0xff, 0, '\n', '{', '}'}
			if err := frames.Write(helperRecord(FrameItem, sessionID, sourceID, dataSeq, `{"index":0,"format":"gray8"}`, payload)); err != nil {
				return err
			}
		case CommandStop:
			dataSeq++
			if err := frames.Write(helperRecord(FrameEnd, sessionID, sourceID, dataSeq, `{"items":1}`, nil)); err != nil {
				return err
			}
			dataSeq++
			return frames.Write(helperRecord(FrameEOF, sessionID, sourceID, dataSeq, `{}`, nil))
		case CommandCancel:
			if mode == "cancel-rejected" {
				return nil
			}
			dataSeq++
			return frames.Write(helperRecord(FrameEOF, sessionID, sourceID, dataSeq, `{}`, nil))
		}
	}
}

func helperRecord(
	frameType FrameType,
	sessionID string,
	sourceID string,
	seq uint64,
	metadata string,
	payload []byte,
) Record {
	return Record{
		Type: frameType, SessionID: sessionID, SourceID: sourceID, Seq: seq,
		Metadata: []byte(metadata), Payload: payload,
	}
}

func nextRecord(t *testing.T, ctx context.Context, client *Client) Record {
	t.Helper()
	record, err := client.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return record
}
