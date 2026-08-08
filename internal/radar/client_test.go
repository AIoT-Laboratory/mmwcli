package radar

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type scriptedExchange struct {
	command  string
	response string
}

type scriptedTransport struct {
	script   []scriptedExchange
	commands []string
	current  *bytes.Reader
	closed   bool
}

type zeroReadTransport struct {
	writes []string
}

type trickleTransport struct {
	writes []string
	delay  time.Duration
}

type purgingTransport struct {
	stale    bytes.Buffer
	current  *bytes.Reader
	purges   int
	commands []string
	response string
}

type cancelRecoveryTransport struct {
	mu           sync.Mutex
	commands     []string
	current      *bytes.Reader
	readDeadline time.Time
}

func (p *purgingTransport) PurgeInput() error {
	p.purges++
	p.stale.Reset()
	return nil
}

func (p *purgingTransport) Write(payload []byte) (int, error) {
	p.commands = append(p.commands, strings.TrimSuffix(string(payload), "\n"))
	response := p.response
	if response == "" {
		response = "sensorStop\nError -57\n"
	}
	p.current = bytes.NewReader([]byte(response))
	return len(payload), nil
}

func (p *purgingTransport) Read(buffer []byte) (int, error) {
	if p.stale.Len() != 0 {
		return p.stale.Read(buffer)
	}
	if p.current == nil {
		return 0, errors.New("read before command")
	}
	return p.current.Read(buffer)
}

func (p *purgingTransport) Close() error                     { return nil }
func (p *purgingTransport) SetReadDeadline(time.Time) error  { return nil }
func (p *purgingTransport) SetWriteDeadline(time.Time) error { return nil }

func (z *zeroReadTransport) Write(payload []byte) (int, error) {
	z.writes = append(z.writes, string(payload))
	return len(payload), nil
}

func (z *zeroReadTransport) Read([]byte) (int, error) { return 0, nil }

func (z *zeroReadTransport) Close() error                     { return nil }
func (z *zeroReadTransport) SetReadDeadline(time.Time) error  { return nil }
func (z *zeroReadTransport) SetWriteDeadline(time.Time) error { return nil }

func (t *trickleTransport) Write(payload []byte) (int, error) {
	t.writes = append(t.writes, string(payload))
	return len(payload), nil
}
func (t *trickleTransport) Read(buffer []byte) (int, error) {
	time.Sleep(t.delay)
	buffer[0] = 'x'
	return 1, nil
}
func (t *trickleTransport) Close() error                     { return nil }
func (t *trickleTransport) SetReadDeadline(time.Time) error  { return nil }
func (t *trickleTransport) SetWriteDeadline(time.Time) error { return nil }

func (s *scriptedTransport) Write(payload []byte) (int, error) {
	if s.closed {
		return 0, errors.New("closed")
	}
	command := strings.TrimSuffix(string(payload), "\n")
	if len(s.script) == 0 {
		return 0, fmt.Errorf("unexpected command %q", command)
	}
	next := s.script[0]
	if command != next.command {
		return 0, fmt.Errorf("command = %q, want %q", command, next.command)
	}
	s.script = s.script[1:]
	s.commands = append(s.commands, command)
	s.current = bytes.NewReader([]byte(next.response))
	return len(payload), nil
}

func (s *scriptedTransport) Read(buffer []byte) (int, error) {
	if s.closed {
		return 0, errors.New("closed")
	}
	if s.current == nil {
		return 0, errors.New("read before command")
	}
	return s.current.Read(buffer)
}

func (s *scriptedTransport) Close() error {
	s.closed = true
	return nil
}

func (s *scriptedTransport) SetReadDeadline(time.Time) error  { return nil }
func (s *scriptedTransport) SetWriteDeadline(time.Time) error { return nil }

func (t *cancelRecoveryTransport) Write(payload []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	command := strings.TrimSuffix(string(payload), "\n")
	t.commands = append(t.commands, command)
	if command == "version" {
		t.current = bytes.NewReader([]byte("Platform                : xWR68xx\nDone\n"))
	} else if command == "sensorStop" {
		t.current = bytes.NewReader([]byte("Done\nsensorStop\nDone\n"))
	} else {
		t.current = nil
	}
	return len(payload), nil
}

func (t *cancelRecoveryTransport) Read(buffer []byte) (int, error) {
	t.mu.Lock()
	if t.current != nil {
		count, err := t.current.Read(buffer)
		t.mu.Unlock()
		return count, err
	}
	deadline := t.readDeadline
	t.mu.Unlock()
	remaining := time.Until(deadline)
	if remaining > 0 {
		time.Sleep(remaining)
	}
	return 0, os.ErrDeadlineExceeded
}

func (t *cancelRecoveryTransport) Close() error { return nil }
func (t *cancelRecoveryTransport) SetReadDeadline(deadline time.Time) error {
	t.mu.Lock()
	t.readDeadline = deadline
	t.mu.Unlock()
	return nil
}
func (t *cancelRecoveryTransport) SetWriteDeadline(time.Time) error { return nil }

func newScriptedClient(t *testing.T, dialect Dialect, script ...scriptedExchange) (*Client, *scriptedTransport) {
	t.Helper()
	transport := &scriptedTransport{script: append([]scriptedExchange(nil), script...)}
	client, err := NewClient(transport, dialect, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return client, transport
}

func TestStudioStopUsesPlatformGateAndTreatsMinus54AsSuccess(t *testing.T) {
	client, transport := newScriptedClient(t, StudioCLI,
		scriptedExchange{command: "version", response: "Error: diagnostic line\r\nPlatform : xWR68xx\r\nDone\r\n"},
		scriptedExchange{command: "sensorStop", response: "Error -54\r\n"},
		scriptedExchange{command: "sensorStart 0", response: "Done\r\n"},
	)
	response, err := client.Stop()
	if err != nil || !strings.Contains(response, "Error -54") {
		t.Fatalf("Stop = %q, %v", response, err)
	}
	if _, err := client.StartWithoutReconfiguration(); err != nil {
		t.Fatal(err)
	}
	want := "version|sensorStop|sensorStart 0"
	if got := strings.Join(transport.commands, "|"); got != want {
		t.Fatalf("commands = %q, want %q", got, want)
	}
}

func TestStudioPlatformMismatchBlocksStateCommand(t *testing.T) {
	client, transport := newScriptedClient(t, StudioCLI,
		scriptedExchange{command: "version", response: "Platform : xWR18xx\nDone\n"},
	)
	if _, err := client.Start(); err == nil || !strings.Contains(err.Error(), "unsupported platform") {
		t.Fatalf("Start error = %v", err)
	}
	if got := strings.Join(transport.commands, "|"); got != "version" {
		t.Fatalf("state command escaped platform gate: %q", got)
	}
}

func TestStudioSendsVersionBeforeStart(t *testing.T) {
	client, transport := newScriptedClient(t, StudioCLI,
		scriptedExchange{command: "version", response: "Platform                : xWR68xx\nDone\n"},
		scriptedExchange{command: "sensorStart", response: "Done\n"},
	)
	if _, err := client.Start(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(transport.commands, "|"); got != "version|sensorStart" {
		t.Fatalf("commands = %q", got)
	}
}

func TestStudioPlatformMismatchBlocksStateWrites(t *testing.T) {
	plan, err := BuildCapturePlan(StudioCLI, validCommands(), FullConfiguration)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		run  func(*Client) error
	}{
		{name: "apply", run: func(client *Client) error { return client.Apply(plan) }},
		{name: "start", run: func(client *Client) error { _, err := client.Start(); return err }},
		{name: "stop", run: func(client *Client) error { _, err := client.Stop(); return err }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, transport := newScriptedClient(t, StudioCLI,
				scriptedExchange{command: "version", response: "Platform : xWR18xx\nDone\n"},
			)
			if err := test.run(client); err == nil || !strings.Contains(err.Error(), "unsupported platform") {
				t.Fatalf("state command error = %v", err)
			}
			if got := strings.Join(transport.commands, "|"); got != "version" {
				t.Fatalf("state write escaped platform gate: %q", got)
			}
		})
	}
}

func TestStudioCaptureLifecycleVerifiesBeforeFirstStateWrite(t *testing.T) {
	plan, err := BuildCapturePlan(StudioCLI, validCommands(), FullConfiguration)
	if err != nil {
		t.Fatal(err)
	}
	script := []scriptedExchange{
		{command: "version", response: "Platform                : xWR68xx\nDone\n"},
		{command: "sensorStop", response: "Done\n"},
	}
	for _, command := range plan.ConfigurationCommands {
		script = append(script, scriptedExchange{command: command, response: "Done\n"})
	}
	script = append(script,
		scriptedExchange{command: "sensorStart", response: "Done\n"},
		scriptedExchange{command: "sensorStop", response: "Done\n"},
	)
	client, transport := newScriptedClient(t, StudioCLI, script...)
	if _, err := client.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := client.Apply(plan); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Stop(); err != nil {
		t.Fatal(err)
	}
	want := append([]string{"version", "sensorStop"}, plan.ConfigurationCommands...)
	want = append(want, "sensorStart", "sensorStop")
	if got := strings.Join(transport.commands, "|"); got != strings.Join(want, "|") {
		t.Fatalf("capture lifecycle commands = %q, want %q", got, strings.Join(want, "|"))
	}
}

func TestApplySendsConfigurationButNotStart(t *testing.T) {
	plan, err := BuildCapturePlan(StudioCLI, validCommands(), FullConfiguration)
	if err != nil {
		t.Fatal(err)
	}
	script := []scriptedExchange{{command: "version", response: "Platform:xWR68xx\nDone\n"}}
	for _, command := range plan.ConfigurationCommands {
		script = append(script, scriptedExchange{command: command, response: command + "\nDone\n"})
	}
	client, transport := newScriptedClient(t, StudioCLI, script...)
	if err := client.Apply(plan); err != nil {
		t.Fatal(err)
	}
	if got := transport.commands[len(transport.commands)-1]; got == "sensorStart" || got == "sensorStart 0" {
		t.Fatalf("Apply sent start early: %q", got)
	}
	if len(transport.script) != 0 {
		t.Fatalf("unused script entries: %d", len(transport.script))
	}
}

func TestApplyRejectsReuseAndDialectMismatchWithoutIO(t *testing.T) {
	reuse, err := BuildCapturePlan(StudioCLI, validCommands(), ReuseConfiguration)
	if err != nil {
		t.Fatal(err)
	}
	client, transport := newScriptedClient(t, StudioCLI)
	if err := client.Apply(reuse); err == nil {
		t.Fatal("Apply accepted reuse plan")
	}
	if len(transport.commands) != 0 {
		t.Fatal("Apply touched transport for invalid reuse plan")
	}

	mismatchedPlan, err := BuildCapturePlan(StudioCLI, validCommands(), FullConfiguration)
	if err != nil {
		t.Fatal(err)
	}
	mismatchedPlan.Dialect = Dialect{}
	if err := client.Apply(mismatchedPlan); err == nil {
		t.Fatal("Apply accepted mismatched dialect")
	}
}

func TestApplyRevalidatesFabricatedPlanBeforeIO(t *testing.T) {
	client, transport := newScriptedClient(t, StudioCLI)
	plan := CapturePlan{
		Dialect:               StudioCLI,
		Mode:                  FullConfiguration,
		ConfigurationCommands: []string{"flushCfg", "deviceRestart"},
	}
	if err := client.Apply(plan); err == nil {
		t.Fatal("Apply accepted fabricated unsafe plan")
	}
	if len(transport.commands) != 0 {
		t.Fatal("Apply touched transport before revalidating plan")
	}
}

func TestSendCommandReturnsTypedMappedError(t *testing.T) {
	client, _ := newScriptedClient(t, StudioCLI,
		scriptedExchange{command: "sensorStart", response: "sensorStart\nError -57\n"},
	)
	_, err := client.SendCommand("sensorStart")
	var commandError *CommandError
	if !errors.As(err, &commandError) {
		t.Fatalf("error type = %T: %v", err, err)
	}
	if commandError.Code != -57 || commandError.Description != "data-path configuration failed" {
		t.Fatalf("command error = %+v", commandError)
	}
}

func TestSendCommandIgnoresDiagnosticErrorLine(t *testing.T) {
	client, _ := newScriptedClient(t, StudioCLI,
		scriptedExchange{command: "version", response: "Error: unavailable optional field\nDone\n"},
	)
	if _, err := client.SendCommand("version"); err != nil {
		t.Fatalf("diagnostic line became failure: %v", err)
	}
}

func TestSendCommandReportsUnknownResultAtEOF(t *testing.T) {
	client, _ := newScriptedClient(t, StudioCLI,
		scriptedExchange{command: "sensorStart", response: "accepted but unfinished\n"},
	)
	response, err := client.SendCommand("sensorStart")
	var responseError *ResponseError
	if !errors.As(err, &responseError) || !errors.Is(err, io.EOF) {
		t.Fatalf("error = %T %v", err, err)
	}
	if response != "accepted but unfinished\n" || responseError.Command != "sensorStart" {
		t.Fatalf("response error = %#v / %q", responseError, response)
	}
}

func TestZeroLengthSerialReadBecomesBoundedUnknownResult(t *testing.T) {
	transport := &zeroReadTransport{}
	client, err := NewClient(transport, StudioCLI, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.SendCommand("sensorStart")
	var responseError *ResponseError
	if !errors.As(err, &responseError) || !errors.Is(err, ErrReadTimeout) {
		t.Fatalf("zero read error = %T %v", err, err)
	}
	if len(transport.writes) != 1 {
		t.Fatalf("writes = %d, want exactly one", len(transport.writes))
	}
}

func TestTricklingResponseCannotRenewContextDeadline(t *testing.T) {
	transport := &trickleTransport{delay: 2 * time.Millisecond}
	client, err := NewClient(transport, StudioCLI, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = client.SendCommandContext(ctx, "sensorStart")
	var responseError *ResponseError
	if !errors.As(err, &responseError) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("trickle error = %T %v", err, err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("trickle renewed an absolute deadline: %s", elapsed)
	}
	if len(transport.writes) != 1 {
		t.Fatalf("writes = %d, want one", len(transport.writes))
	}
}

func TestSendCommandBoundsResponse(t *testing.T) {
	for _, suffix := range []string{"", "\n"} {
		client, _ := newScriptedClient(t, StudioCLI,
			scriptedExchange{command: "version", response: strings.Repeat("x", 33) + suffix},
		)
		client.maximumResponse = 32
		response, err := client.SendCommand("version")
		var responseError *ResponseError
		if !errors.As(err, &responseError) || !strings.Contains(err.Error(), "exceeded") {
			t.Fatalf("oversized response error = %v", err)
		}
		if len(response) > client.maximumResponse {
			t.Fatalf("retained response bytes = %d, maximum = %d", len(response), client.maximumResponse)
		}
	}
}

func TestSendCommandRejectsMultilineInjectionBeforeIO(t *testing.T) {
	client, transport := newScriptedClient(t, StudioCLI)
	if _, err := client.SendCommand("sensorStop\nsensorStart"); err == nil {
		t.Fatal("multiline command accepted")
	}
	if len(transport.commands) != 0 {
		t.Fatal("invalid command touched transport")
	}
}

func TestSendCommandPurgesLateTerminalBeforeNextCommand(t *testing.T) {
	transport := &purgingTransport{}
	transport.stale.WriteString("Done\n")
	client, err := NewClient(transport, StudioCLI, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.SendCommand("sensorStop")
	var commandError *CommandError
	if !errors.As(err, &commandError) || commandError.Code != -57 {
		t.Fatalf("error = %T %v, stale Done may have satisfied sensorStop", err, err)
	}
	if transport.purges != 1 {
		t.Fatalf("purges = %d, want 1", transport.purges)
	}
}

func TestDesynchronizedCommandRequiresItsOwnEchoBeforeTerminal(t *testing.T) {
	transport := &purgingTransport{response: "Done\nsensorStop\nError -57\n"}
	client, err := NewClient(transport, StudioCLI, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client.desynchronized = true
	_, err = client.SendCommand("sensorStop")
	var commandError *CommandError
	if !errors.As(err, &commandError) || commandError.Code != -57 {
		t.Fatalf("error = %T %v, late Done may have satisfied recovery", err, err)
	}
}

func TestCanceledCommandReleasesMutexForRecovery(t *testing.T) {
	transport := &cancelRecoveryTransport{}
	client, err := NewClient(transport, StudioCLI, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(10*time.Millisecond, cancel)
	started := time.Now()
	_, err = client.StartContext(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("StartContext error = %T %v", err, err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("cancellation took %s", elapsed)
	}
	recoveryContext, recoveryCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer recoveryCancel()
	if _, err := client.StopContext(recoveryContext); err != nil {
		t.Fatalf("StopContext after cancellation: %v", err)
	}
	transport.mu.Lock()
	commands := strings.Join(transport.commands, "|")
	transport.mu.Unlock()
	if commands != "version|sensorStart|sensorStop" {
		t.Fatalf("commands = %q", commands)
	}
}

func TestClientCloseIsIdempotent(t *testing.T) {
	client, transport := newScriptedClient(t, StudioCLI)
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if !transport.closed {
		t.Fatal("transport not closed")
	}
	if _, err := client.SendCommand("sensorStop"); err == nil {
		t.Fatal("closed client accepted command")
	}
}
