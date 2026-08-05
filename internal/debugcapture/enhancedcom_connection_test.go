package debugcapture

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestOpenEnhancedCOMConnectionUsesTIDebugBaudAndGatesPart(t *testing.T) {
	transport := &fakeEnhancedCOMTransport{reads: [][]byte{
		[]byte("12345\r\n"), nil,
		[]byte("03880000\r\n"), nil,
	}}
	openCalls := 0
	var waits []time.Duration
	connection, err := openEnhancedCOMConnectionWithBackend(
		context.Background(),
		"COM3",
		enhancedCOMBackend{
			open: func(name string, baud int, timeout time.Duration) (enhancedCOMTransport, error) {
				openCalls++
				if name != "COM3" || baud != 921600 || timeout != enhancedCOMOpenTimeout {
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
	if openCalls != 1 || connection.probeValue != 0x12345 || connection.partNumber != iwr68xxES2PartNumber {
		t.Fatalf("open calls/probe/part = %d/0x%08X/0x%02X", openCalls, connection.probeValue, connection.partNumber)
	}
	wantWaits := []time.Duration{
		400 * time.Millisecond,
		400 * time.Millisecond,
		100 * time.Millisecond,
		100 * time.Millisecond,
		100 * time.Millisecond,
	}
	if !slices.Equal(waits, wantWaits) {
		t.Fatalf("waits = %v, want %v", waits, wantWaits)
	}
	if transport.writeCalls != 5 {
		t.Fatalf("initialization and gate writes = %d, want 5", transport.writeCalls)
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

func TestOpenEnhancedCOMConnectionNegotiatesColdBootBaudOnce(t *testing.T) {
	requestedProbe := &fakeEnhancedCOMTransport{reads: [][]byte{[]byte("x0 ??"), nil}}
	coldBoot := &fakeEnhancedCOMTransport{reads: [][]byte{
		[]byte("2\r\n"), nil,
		[]byte("3880000\r\n"), nil,
		[]byte("1\r\n"), nil,
	}}
	requestedFinal := &fakeEnhancedCOMTransport{reads: [][]byte{
		[]byte("2\r\n"), nil,
		[]byte("3880000\r\n"), nil,
	}}
	transports := []*fakeEnhancedCOMTransport{requestedProbe, coldBoot, requestedFinal}
	var bauds []int
	var waits []time.Duration
	connection, err := openEnhancedCOMConnectionWithBackend(context.Background(), "COM3", enhancedCOMBackend{
		open: func(name string, baud int, timeout time.Duration) (enhancedCOMTransport, error) {
			if name != "COM3" || timeout != enhancedCOMOpenTimeout || len(transports) == 0 {
				t.Fatalf("open arguments/remaining = %q, %d, %s/%d", name, baud, timeout, len(transports))
			}
			bauds = append(bauds, baud)
			transport := transports[0]
			transports = transports[1:]
			return transport, nil
		},
		wait: func(ctx context.Context, duration time.Duration) error {
			waits = append(waits, duration)
			return ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{921600, 115200, 921600}; !slices.Equal(bauds, want) {
		t.Fatalf("open bauds = %v, want %v", bauds, want)
	}
	wantColdBootWrites := []string{
		"x0 \r\n",
		"rd ffffe2fc\r",
		"rd ffffe214\r",
		"rd ffffe144\r",
		"wr ffffe144 00007801\r",
		"wr ffffe264 0d902c2b\r",
	}
	var coldBootWrites []string
	for _, write := range coldBoot.writes {
		coldBootWrites = append(coldBootWrites, string(write))
	}
	if !slices.Equal(coldBootWrites, wantColdBootWrites) {
		t.Fatalf("cold-boot writes = %q, want %q", coldBootWrites, wantColdBootWrites)
	}
	wantWaits := []time.Duration{
		400 * time.Millisecond,
		400 * time.Millisecond, 100 * time.Millisecond, 100 * time.Millisecond,
		500 * time.Millisecond, 500 * time.Millisecond,
		100 * time.Millisecond, 100 * time.Millisecond,
		100 * time.Millisecond,
		300 * time.Millisecond, 100 * time.Millisecond,
		300 * time.Millisecond, 300 * time.Millisecond,
		900 * time.Millisecond,
		400 * time.Millisecond,
		400 * time.Millisecond, 100 * time.Millisecond, 100 * time.Millisecond,
		100 * time.Millisecond,
	}
	if !slices.Equal(waits, wantWaits) {
		t.Fatalf("waits = %v, want %v", waits, wantWaits)
	}
	if requestedProbe.closeCalls != 1 || coldBoot.closeCalls != 1 || requestedFinal.closeCalls != 0 {
		t.Fatalf(
			"probe/cold/final close calls = %d/%d/%d, want 1/1/0",
			requestedProbe.closeCalls,
			coldBoot.closeCalls,
			requestedFinal.closeCalls,
		)
	}
	if connection.probeValue != 2 || connection.partNumber != iwr68xxES2PartNumber {
		t.Fatalf("probe/part = 0x%08X/0x%02X", connection.probeValue, connection.partNumber)
	}
	if err := connection.close(); err != nil {
		t.Fatal(err)
	}
	if requestedFinal.closeCalls != 1 {
		t.Fatalf("final close calls = %d, want 1", requestedFinal.closeCalls)
	}
}

func TestOpenEnhancedCOMConnectionStopsAfterInvalidColdBootProbe(t *testing.T) {
	requestedProbe := &fakeEnhancedCOMTransport{reads: [][]byte{[]byte("x0 ??"), nil}}
	coldBoot := &fakeEnhancedCOMTransport{reads: [][]byte{[]byte("bad?!"), nil}}
	transports := []*fakeEnhancedCOMTransport{requestedProbe, coldBoot}
	var bauds []int
	_, err := openEnhancedCOMConnectionWithBackend(context.Background(), "COM3", enhancedCOMBackend{
		open: func(_ string, baud int, _ time.Duration) (enhancedCOMTransport, error) {
			bauds = append(bauds, baud)
			transport := transports[0]
			transports = transports[1:]
			return transport, nil
		},
		wait: func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
	})
	if !errors.Is(err, errEnhancedCOMInvalidResponse) {
		t.Fatalf("error = %v", err)
	}
	if want := []int{921600, 115200}; !slices.Equal(bauds, want) {
		t.Fatalf("open bauds = %v, want %v", bauds, want)
	}
	for _, write := range coldBoot.writes {
		if strings.HasPrefix(string(write), "wr ") {
			t.Fatalf("invalid cold-boot probe caused a register write: %q", write)
		}
	}
	if requestedProbe.closeCalls != 1 || coldBoot.closeCalls != 1 {
		t.Fatalf("probe/cold close calls = %d/%d, want 1/1", requestedProbe.closeCalls, coldBoot.closeCalls)
	}
}

func TestOpenEnhancedCOMConnectionDoesNotNegotiateAfterInitialIOFailure(t *testing.T) {
	want := errors.New("serial I/O failed")
	transport := &fakeEnhancedCOMTransport{readError: want}
	openCalls := 0
	_, err := openEnhancedCOMConnectionWithBackend(context.Background(), "COM3", enhancedCOMBackend{
		open: func(string, int, time.Duration) (enhancedCOMTransport, error) {
			openCalls++
			return transport, nil
		},
		wait: func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
	})
	if !errors.Is(err, want) || openCalls != 1 || transport.closeCalls != 1 {
		t.Fatalf("error/open/close = %v/%d/%d, want I/O error/1/1", err, openCalls, transport.closeCalls)
	}
}

func TestOpenEnhancedCOMConnectionGatesColdBootPartBeforeBaudWrites(t *testing.T) {
	requestedProbe := &fakeEnhancedCOMTransport{reads: [][]byte{[]byte("x0 ??"), nil}}
	coldBoot := &fakeEnhancedCOMTransport{reads: [][]byte{
		[]byte("2\r\n"), nil,
		[]byte("3400000\r\n"), nil,
	}}
	transports := []*fakeEnhancedCOMTransport{requestedProbe, coldBoot}
	_, err := openEnhancedCOMConnectionWithBackend(context.Background(), "COM3", enhancedCOMBackend{
		open: func(string, int, time.Duration) (enhancedCOMTransport, error) {
			transport := transports[0]
			transports = transports[1:]
			return transport, nil
		},
		wait: func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported part number 0xD0") {
		t.Fatalf("error = %v", err)
	}
	for _, write := range coldBoot.writes {
		if strings.HasPrefix(string(write), "wr ") {
			t.Fatalf("unsupported cold-boot target received a register write: %q", write)
		}
	}
	if requestedProbe.closeCalls != 1 || coldBoot.closeCalls != 1 {
		t.Fatalf("probe/cold close calls = %d/%d, want 1/1", requestedProbe.closeCalls, coldBoot.closeCalls)
	}
}

func TestOpenEnhancedCOMConnectionDoesNotNegotiateWhenInitialCloseFails(t *testing.T) {
	closeErr := errors.New("close failed")
	transport := &fakeEnhancedCOMTransport{closeError: closeErr}
	openCalls := 0
	_, err := openEnhancedCOMConnectionWithBackend(context.Background(), "COM3", enhancedCOMBackend{
		open: func(string, int, time.Duration) (enhancedCOMTransport, error) {
			openCalls++
			return transport, nil
		},
		wait: func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
	})
	if !errors.Is(err, closeErr) || !errors.Is(err, errEnhancedCOMReadTimeout) {
		t.Fatalf("error = %v", err)
	}
	if openCalls != 1 || transport.closeCalls != 1 {
		t.Fatalf("open/close calls = %d/%d, want 1/1", openCalls, transport.closeCalls)
	}
}

func TestOpenEnhancedCOMConnectionDoesNotRetryUnknownBaudSwitch(t *testing.T) {
	requestedProbe := &fakeEnhancedCOMTransport{readError: os.ErrDeadlineExceeded}
	baudSwitchErr := errors.New("baud switch result unknown")
	coldBoot := &fakeEnhancedCOMTransport{reads: [][]byte{
		[]byte("00000002\r\n"), nil,
		[]byte("03880000\r\n"), nil,
		[]byte("00000001\r\n"), nil,
	}}
	coldBoot.afterWrite = func() {
		if coldBoot.writeCalls == 6 {
			coldBoot.writeError = baudSwitchErr
		}
	}
	transports := []*fakeEnhancedCOMTransport{requestedProbe, coldBoot}
	var bauds []int
	_, err := openEnhancedCOMConnectionWithBackend(context.Background(), "COM3", enhancedCOMBackend{
		open: func(_ string, baud int, _ time.Duration) (enhancedCOMTransport, error) {
			bauds = append(bauds, baud)
			if len(transports) == 0 {
				t.Fatal("unexpected reopen after unknown baud switch")
			}
			transport := transports[0]
			transports = transports[1:]
			return transport, nil
		},
		wait: func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
	})
	if !errors.Is(err, baudSwitchErr) {
		t.Fatalf("error = %v", err)
	}
	if want := []int{921600, 115200}; !slices.Equal(bauds, want) {
		t.Fatalf("open bauds = %v, want %v", bauds, want)
	}
	baudSwitchWrites := 0
	for _, write := range coldBoot.writes {
		if string(write) == "wr ffffe264 0d902c2b\r" {
			baudSwitchWrites++
		}
	}
	if baudSwitchWrites != 1 || requestedProbe.closeCalls != 1 || coldBoot.closeCalls != 1 {
		t.Fatalf(
			"baud-switch writes/probe closes/cold closes = %d/%d/%d, want 1/1/1",
			baudSwitchWrites,
			requestedProbe.closeCalls,
			coldBoot.closeCalls,
		)
	}
}

func TestOpenEnhancedCOMConnectionDoesNotContinueAfterUnknownBaudClockWrite(t *testing.T) {
	requestedProbe := &fakeEnhancedCOMTransport{readError: os.ErrDeadlineExceeded}
	coldBoot := &fakeEnhancedCOMTransport{reads: [][]byte{
		[]byte("2\r\n"), nil,
		[]byte("3880000\r\n"), nil,
		[]byte("1\r\n"), nil,
	}}
	coldBoot.afterWrite = func() {
		if coldBoot.writeCalls == 5 {
			coldBoot.shortWrite = true
		}
	}
	transports := []*fakeEnhancedCOMTransport{requestedProbe, coldBoot}
	var bauds []int
	_, err := openEnhancedCOMConnectionWithBackend(context.Background(), "COM3", enhancedCOMBackend{
		open: func(_ string, baud int, _ time.Duration) (enhancedCOMTransport, error) {
			bauds = append(bauds, baud)
			if len(transports) == 0 {
				t.Fatal("unexpected reopen after unknown baud clock write")
			}
			transport := transports[0]
			transports = transports[1:]
			return transport, nil
		},
		wait: func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
	})
	var unknown *enhancedCOMUnknownResultError
	if !errors.As(err, &unknown) || !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("error = %v", err)
	}
	if want := []int{921600, 115200}; !slices.Equal(bauds, want) {
		t.Fatalf("open bauds = %v, want %v", bauds, want)
	}
	for _, write := range coldBoot.writes {
		if string(write) == "wr ffffe264 0d902c2b\r" {
			t.Fatalf("baud register write followed unknown clock write: %q", write)
		}
	}
	if requestedProbe.closeCalls != 1 || coldBoot.closeCalls != 1 {
		t.Fatalf("probe/cold close calls = %d/%d, want 1/1", requestedProbe.closeCalls, coldBoot.closeCalls)
	}
}

func TestOpenEnhancedCOMConnectionDoesNotReconnectAfterColdBootCloseFailure(t *testing.T) {
	requestedProbe := &fakeEnhancedCOMTransport{readError: os.ErrDeadlineExceeded}
	closeErr := errors.New("cold-boot close failed")
	coldBoot := &fakeEnhancedCOMTransport{
		reads: [][]byte{
			[]byte("2\r\n"), nil,
			[]byte("3880000\r\n"), nil,
			[]byte("1\r\n"), nil,
		},
		closeError: closeErr,
	}
	transports := []*fakeEnhancedCOMTransport{requestedProbe, coldBoot}
	var bauds []int
	_, err := openEnhancedCOMConnectionWithBackend(context.Background(), "COM3", enhancedCOMBackend{
		open: func(_ string, baud int, _ time.Duration) (enhancedCOMTransport, error) {
			bauds = append(bauds, baud)
			if len(transports) == 0 {
				t.Fatal("unexpected reconnect after cold-boot close failure")
			}
			transport := transports[0]
			transports = transports[1:]
			return transport, nil
		},
		wait: func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
	})
	if !errors.Is(err, closeErr) {
		t.Fatalf("error = %v", err)
	}
	if want := []int{921600, 115200}; !slices.Equal(bauds, want) {
		t.Fatalf("open bauds = %v, want %v", bauds, want)
	}
	if requestedProbe.closeCalls != 1 || coldBoot.closeCalls != 1 {
		t.Fatalf("probe/cold close calls = %d/%d, want 1/1", requestedProbe.closeCalls, coldBoot.closeCalls)
	}
}

func TestOpenEnhancedCOMConnectionDoesNotRenegotiateAfterFinalVerificationFailure(t *testing.T) {
	requestedProbe := &fakeEnhancedCOMTransport{readError: os.ErrDeadlineExceeded}
	coldBoot := &fakeEnhancedCOMTransport{reads: [][]byte{
		[]byte("2\r\n"), nil,
		[]byte("3880000\r\n"), nil,
		[]byte("1\r\n"), nil,
	}}
	requestedFinal := &fakeEnhancedCOMTransport{reads: [][]byte{[]byte("x0 ??"), nil}}
	transports := []*fakeEnhancedCOMTransport{requestedProbe, coldBoot, requestedFinal}
	var bauds []int
	_, err := openEnhancedCOMConnectionWithBackend(context.Background(), "COM3", enhancedCOMBackend{
		open: func(_ string, baud int, _ time.Duration) (enhancedCOMTransport, error) {
			bauds = append(bauds, baud)
			if len(transports) == 0 {
				t.Fatal("unexpected second negotiation after final verification failure")
			}
			transport := transports[0]
			transports = transports[1:]
			return transport, nil
		},
		wait: func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
	})
	if !errors.Is(err, errEnhancedCOMInvalidResponse) {
		t.Fatalf("error = %v", err)
	}
	if want := []int{921600, 115200, 921600}; !slices.Equal(bauds, want) {
		t.Fatalf("open bauds = %v, want %v", bauds, want)
	}
	if requestedProbe.closeCalls != 1 || coldBoot.closeCalls != 1 || requestedFinal.closeCalls != 1 {
		t.Fatalf(
			"probe/cold/final close calls = %d/%d/%d, want 1/1/1",
			requestedProbe.closeCalls,
			coldBoot.closeCalls,
			requestedFinal.closeCalls,
		)
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

func TestOpenEnhancedCOMConnectionRejectsUnsupportedPartBeforeRegisterWrites(t *testing.T) {
	transport := &fakeEnhancedCOMTransport{reads: [][]byte{
		[]byte("00000002"), nil,
		[]byte("03400000"), nil,
	}}
	_, err := openEnhancedCOMConnectionWithBackend(context.Background(), "COM3", enhancedCOMBackend{
		open: func(string, int, time.Duration) (enhancedCOMTransport, error) { return transport, nil },
		wait: func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported part number 0xD0") {
		t.Fatalf("error = %v", err)
	}
	if transport.closeCalls != 1 {
		t.Fatalf("close calls = %d, want 1", transport.closeCalls)
	}
	for _, write := range transport.writes {
		if strings.HasPrefix(string(write), "wr ") {
			t.Fatalf("unsupported part received a register write: %q", write)
		}
	}
}

func TestSupportedXWR6843PartNumbers(t *testing.T) {
	if !supportedXWR6843Part(iwr68xxES2PartNumber) || !supportedXWR6843Part(awr68xxPartNumber) {
		t.Fatal("supported xWR6843 part number was rejected")
	}
	for _, partNumber := range []uint8{0, 0xe0, 0xd0, 0xe3, 0x53} {
		if supportedXWR6843Part(partNumber) {
			t.Fatalf("unsupported part number 0x%02X was accepted", partNumber)
		}
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
		[]byte("03880000"), nil,
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
	connection := &enhancedCOMConnection{
		client:     client,
		partNumber: iwr68xxES2PartNumber,
		verified:   true,
	}
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

func TestEnhancedCOMConnectionRejectsUngatedFirmwareSubmission(t *testing.T) {
	transport := &fakeEnhancedCOMTransport{}
	client := mustEnhancedCOMClient(t, transport)
	connection := &enhancedCOMConnection{client: client, partNumber: iwr68xxES2PartNumber}

	_, err := connection.submitFirmware(context.Background(), fixtureSubmissionAssets(t))
	if err == nil || !strings.Contains(err.Error(), "has not passed") {
		t.Fatalf("error = %v", err)
	}
	if transport.writeCalls != 0 || transport.closeCalls != 1 {
		t.Fatalf("ungated submission writes/closes = %d/%d", transport.writeCalls, transport.closeCalls)
	}
}
