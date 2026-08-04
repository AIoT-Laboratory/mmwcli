package app

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"mmwcli/internal/radar"
)

func TestREPLVerifiesStudioProtocolAndContinuesAfterExplicitError(t *testing.T) {
	client := &fakeREPLClient{
		verification: "version\nPlatform : xWR68xx\nDone\n",
		responses: map[string]string{
			"customCfg 1": "customCfg 1\nDone\n",
			"badCfg":      "badCfg\nError -50\n",
			"customRun":   "customRun\nDone\n",
		},
		errors: map[string]error{
			"badCfg": &radar.CommandError{Command: "badCfg", Code: -50, Description: "invalid command"},
		},
	}
	input := strings.NewReader("% comment\ncustomCfg 1 // inline\nbadCfg\ncustomRun\n")
	var stdout, stderr bytes.Buffer
	err := runREPL(
		[]string{"--port", "COM3"},
		input,
		&stdout,
		&stderr,
		fakeREPLOpener(t, client),
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(client.commands, ",") != "customCfg 1,badCfg,customRun" {
		t.Fatalf("commands = %v", client.commands)
	}
	for _, expected := range []string{"Platform : xWR68xx", "customCfg 1", "Error -50", "customRun"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("stdout does not contain %q:\n%s", expected, stdout.String())
		}
	}
	if !strings.Contains(stderr.String(), "invalid command") {
		t.Fatalf("stderr = %s", stderr.String())
	}
	if client.verifyCalls != 1 || client.closeCalls != 1 {
		t.Fatalf("verify calls = %d, close calls = %d", client.verifyCalls, client.closeCalls)
	}
}

func TestREPLStopsAfterUnknownResult(t *testing.T) {
	unknown := &radar.ResponseError{Command: "first", Cause: context.DeadlineExceeded}
	client := &fakeREPLClient{
		verification: "Platform : xWR68xx\nDone\n",
		errors:       map[string]error{"first": unknown},
	}
	var stdout, stderr bytes.Buffer
	err := runREPL(
		[]string{"--port", "COM3"},
		strings.NewReader("first\nsecond\n"),
		&stdout,
		&stderr,
		fakeREPLOpener(t, client),
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("repl error = %v", err)
	}
	if strings.Join(client.commands, ",") != "first" {
		t.Fatalf("commands = %v", client.commands)
	}
	if client.closeCalls != 1 {
		t.Fatalf("close calls = %d", client.closeCalls)
	}
}

func TestREPLRejectsInvalidCommandBeforeSend(t *testing.T) {
	for _, line := range []string{strings.Repeat("x", maximumREPLCommandBytes+1), "bad\x00command"} {
		client := &fakeREPLClient{verification: "Platform : xWR68xx\nDone\n"}
		var stdout, stderr bytes.Buffer
		err := runREPL(
			[]string{"--port", "COM3"},
			strings.NewReader(line+"\n"),
			&stdout,
			&stderr,
			fakeREPLOpener(t, client),
		)
		if err == nil {
			t.Fatalf("line %q was accepted", line)
		}
		if len(client.commands) != 0 {
			t.Fatalf("invalid line reached client: %v", client.commands)
		}
	}
}

func TestREPLAcceptsMaximumCommandLength(t *testing.T) {
	command := strings.Repeat("x", maximumREPLCommandBytes)
	client := &fakeREPLClient{
		verification: "Platform : xWR68xx\nDone\n",
		responses:    map[string]string{command: "Done\n"},
	}
	var stdout, stderr bytes.Buffer
	err := runREPL(
		[]string{"--port", "COM3"},
		strings.NewReader(command+"\n"),
		&stdout,
		&stderr,
		fakeREPLOpener(t, client),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(client.commands) != 1 || client.commands[0] != command {
		t.Fatalf("commands = %v", client.commands)
	}
}

func TestREPLStopsWhenPlatformVerificationFails(t *testing.T) {
	client := &fakeREPLClient{
		verification: "Platform : xWR1843\nDone\n",
		verifyError:  errors.New("unsupported platform"),
	}
	var stdout, stderr bytes.Buffer
	err := runREPL(
		[]string{"--port", "COM3"},
		strings.NewReader("customRun\n"),
		&stdout,
		&stderr,
		fakeREPLOpener(t, client),
	)
	if err == nil {
		t.Fatal("platform verification failure was accepted")
	}
	if len(client.commands) != 0 || client.closeCalls != 1 {
		t.Fatalf("commands = %v, close calls = %d", client.commands, client.closeCalls)
	}
}

func TestREPLHelpDoesNotOpenSerial(t *testing.T) {
	opened := false
	var stdout, stderr bytes.Buffer
	err := runREPL(
		[]string{"--help"},
		strings.NewReader(""),
		&stdout,
		&stderr,
		func(string, int, time.Duration) (replClient, error) {
			opened = true
			return nil, nil
		},
	)
	if err != nil || opened {
		t.Fatalf("error = %v, opened = %t", err, opened)
	}
	if !strings.Contains(stdout.String(), "mmwcli repl --port PORT") {
		t.Fatalf("help = %q", stdout.String())
	}
}

func TestREPLValidatesArgumentsBeforeOpen(t *testing.T) {
	opened := false
	var stdout, stderr bytes.Buffer
	err := runREPL(nil, strings.NewReader(""), &stdout, &stderr, func(string, int, time.Duration) (replClient, error) {
		opened = true
		return nil, nil
	})
	if err == nil || opened {
		t.Fatalf("error = %v, opened = %t", err, opened)
	}
}

func fakeREPLOpener(t *testing.T, client replClient) replClientOpener {
	t.Helper()
	return func(port string, baud int, timeout time.Duration) (replClient, error) {
		if port != "COM3" || baud != 921600 || timeout != 10*time.Second {
			t.Fatalf("open(%q, %d, %s)", port, baud, timeout)
		}
		return client, nil
	}
}

type fakeREPLClient struct {
	verification string
	verifyError  error
	responses    map[string]string
	errors       map[string]error
	commands     []string
	verifyCalls  int
	closeCalls   int
}

func (client *fakeREPLClient) VerifyPlatformContext(context.Context) (string, error) {
	client.verifyCalls++
	return client.verification, client.verifyError
}

func (client *fakeREPLClient) SendCommandContext(_ context.Context, command string) (string, error) {
	client.commands = append(client.commands, command)
	return client.responses[command], client.errors[command]
}

func (client *fakeREPLClient) Close() error {
	client.closeCalls++
	return nil
}
