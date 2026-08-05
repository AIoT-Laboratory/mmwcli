package debugcapture

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestOpenEnhancedCOMConnectionUsesFixedBaudAndInitializes(t *testing.T) {
	transport := &fakeEnhancedCOMTransport{reads: [][]byte{[]byte("00001234\r\n"), nil}}
	openCalls := 0
	var waits []time.Duration
	connection, err := openEnhancedCOMConnectionWithBackend(
		context.Background(),
		"COM3",
		enhancedCOMBackend{
			open: func(name string, baud int, timeout time.Duration) (enhancedCOMTransport, error) {
				openCalls++
				if name != "COM3" || baud != enhancedCOMBaud || timeout != enhancedCOMOpenTimeout {
					t.Fatalf("open arguments = %q, %d, %s", name, baud, timeout)
				}
				return transport, nil
			},
			wait: func(ctx context.Context, duration time.Duration) error {
				waits = append(waits, duration)
				return ctx.Err()
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if openCalls != 1 || connection.probeValue != 0x1234 {
		t.Fatalf("open calls/probe = %d/0x%08X", openCalls, connection.probeValue)
	}
	wantWaits := []time.Duration{400 * time.Millisecond, 400 * time.Millisecond, 100 * time.Millisecond, 100 * time.Millisecond}
	if !slices.Equal(waits, wantWaits) {
		t.Fatalf("waits = %v, want %v", waits, wantWaits)
	}
	if transport.writeCalls != 4 {
		t.Fatalf("initialization writes = %d, want 4", transport.writeCalls)
	}
	if err := connection.close(); err != nil {
		t.Fatal(err)
	}
	if err := connection.close(); err != nil {
		t.Fatal(err)
	}
	if transport.closeCalls != 1 {
		t.Fatalf("close calls = %d, want 1", transport.closeCalls)
	}
}

func TestOpenEnhancedCOMConnectionFailsBeforeOpenWhenCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	openCalls := 0
	_, err := openEnhancedCOMConnectionWithBackend(ctx, "COM3", enhancedCOMBackend{
		open: func(string, int, time.Duration) (enhancedCOMTransport, error) {
			openCalls++
			return nil, errors.New("unexpected open")
		},
		wait: waitContext,
	})
	if !errors.Is(err, context.Canceled) || openCalls != 0 {
		t.Fatalf("result = %v, open calls = %d", err, openCalls)
	}
}

func TestOpenEnhancedCOMConnectionClosesOnceAfterUnknownInitialization(t *testing.T) {
	transport := &fakeEnhancedCOMTransport{shortWrite: true}
	_, err := openEnhancedCOMConnectionWithBackend(context.Background(), "COM3", enhancedCOMBackend{
		open: func(string, int, time.Duration) (enhancedCOMTransport, error) { return transport, nil },
		wait: func(context.Context, time.Duration) error { return nil },
	})
	var unknown *enhancedCOMUnknownResultError
	if !errors.As(err, &unknown) || !strings.Contains(err.Error(), "initialize Enhanced COM") {
		t.Fatalf("error = %v", err)
	}
	if transport.writeCalls != 1 || transport.closeCalls != 1 {
		t.Fatalf("failed initialization writes/closes = %d/%d", transport.writeCalls, transport.closeCalls)
	}
}

func TestOpenEnhancedCOMConnectionDoesNotOwnFailedOpen(t *testing.T) {
	want := errors.New("port unavailable")
	_, err := openEnhancedCOMConnectionWithBackend(context.Background(), "COM3", enhancedCOMBackend{
		open: func(string, int, time.Duration) (enhancedCOMTransport, error) { return nil, want },
		wait: func(context.Context, time.Duration) error { return nil },
	})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
}

func TestOpenEnhancedCOMConnectionCancelsDuringPreOpenWait(t *testing.T) {
	openCalls := 0
	_, err := openEnhancedCOMConnectionWithBackend(context.Background(), "COM3", enhancedCOMBackend{
		open: func(string, int, time.Duration) (enhancedCOMTransport, error) {
			openCalls++
			return nil, errors.New("unexpected open")
		},
		wait: func(context.Context, time.Duration) error { return context.Canceled },
	})
	if !errors.Is(err, context.Canceled) || openCalls != 0 {
		t.Fatalf("result = %v, open calls = %d", err, openCalls)
	}
}

func TestEnhancedCOMConnectionSubmitsFirmwareAndStaysOpenForVerification(t *testing.T) {
	transport := &fakeEnhancedCOMTransport{reads: [][]byte{
		[]byte("00000002"), nil,
		[]byte("ad010100"), nil,
		[]byte("00000000"), nil,
		[]byte("000000c0"), nil,
		[]byte("00000003"), nil,
	}}
	connection, err := openEnhancedCOMConnectionWithBackend(context.Background(), "COM3", enhancedCOMBackend{
		open: func(string, int, time.Duration) (enhancedCOMTransport, error) { return transport, nil },
		wait: func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := connection.submitFirmware(context.Background(), fixtureSubmissionAssets(t))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State != firmwareSubmittedUnverified || transport.closeCalls != 0 {
		t.Fatalf("receipt/close calls = %+v/%d", receipt, transport.closeCalls)
	}
	if err := connection.close(); err != nil {
		t.Fatal(err)
	}
}

func TestEnhancedCOMConnectionClosesAfterSubmissionFailure(t *testing.T) {
	transport := &fakeEnhancedCOMTransport{}
	client := mustEnhancedCOMClient(t, transport)
	connection := &enhancedCOMConnection{client: client}
	assets := fixtureSubmissionAssets(t)
	assets.BSS.image.sections[0].address = 4

	_, err := connection.submitFirmware(context.Background(), assets)
	if err == nil || !strings.Contains(err.Error(), "unsupported patch entry") {
		t.Fatalf("error = %v", err)
	}
	if transport.writeCalls != 0 || transport.closeCalls != 1 {
		t.Fatalf("invalid submission writes/closes = %d/%d", transport.writeCalls, transport.closeCalls)
	}
}
