package debugcapture

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestEnhancedCOMClientAccumulatesSplitHexResponseUntilQuiet(t *testing.T) {
	transport := &fakeEnhancedCOMTransport{
		reads: [][]byte{[]byte("ad010"), []byte("100\r\n"), nil},
	}
	client := mustEnhancedCOMClient(t, transport)
	client.wait = recordingEnhancedCOMWait(&transport.calls)

	value, err := client.readRegister(context.Background(), 0xffffe1dc)
	if err != nil {
		t.Fatal(err)
	}
	if value != 0xad010100 {
		t.Fatalf("value = 0x%08X, want 0xAD010100", value)
	}
	wantCalls := []string{"purge", "write:rd ffffe1dc\\r", "wait:100ms", "read:5", "read:5", "read:0"}
	if strings.Join(transport.calls, "\n") != strings.Join(wantCalls, "\n") {
		t.Fatalf("calls = %v, want %v", transport.calls, wantCalls)
	}
	if transport.writeCalls != 1 {
		t.Fatalf("read request write calls = %d, want 1", transport.writeCalls)
	}
}

func TestEnhancedCOMClientInitializesWithThreeFixedWakes(t *testing.T) {
	transport := &fakeEnhancedCOMTransport{reads: [][]byte{[]byte("00000002\r\n"), nil}}
	client := mustEnhancedCOMClient(t, transport)
	client.wait = recordingEnhancedCOMWait(&transport.calls)

	status, err := client.initialize(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status != 2 {
		t.Fatalf("status = 0x%08X, want 2", status)
	}
	wantCalls := []string{
		"write:x0 \\r\n", "wait:400ms",
		"write:x0 \\r\n", "wait:100ms",
		"purge", "write:rd ffffe2fc\\r", "wait:100ms", "read:10", "read:0",
		"write:x0 \\r\n",
	}
	if strings.Join(transport.calls, "\n") != strings.Join(wantCalls, "\n") {
		t.Fatalf("calls = %v, want %v", transport.calls, wantCalls)
	}
	if transport.writeCalls != 4 {
		t.Fatalf("write calls = %d, want 4", transport.writeCalls)
	}
}

func TestEnhancedCOMClientSubmitsWritesWithoutWaitingForACK(t *testing.T) {
	transport := &fakeEnhancedCOMTransport{}
	client := mustEnhancedCOMClient(t, transport)

	if err := client.writeRegister(context.Background(), 0xffffe108, 0xadad00ad); err != nil {
		t.Fatal(err)
	}
	if err := client.writeBlock(context.Background(), 0x40001000, []byte{1, 2, 3, 4}); err != nil {
		t.Fatal(err)
	}
	wantWrites := [][]byte{
		[]byte("wr ffffe108 adad00ad\r"),
		[]byte("wr 40001000 04030201 \r"),
	}
	if len(transport.writes) != len(wantWrites) {
		t.Fatalf("writes = %q, want %q", transport.writes, wantWrites)
	}
	for index := range wantWrites {
		if !bytes.Equal(transport.writes[index], wantWrites[index]) {
			t.Fatalf("write %d = %q, want %q", index, transport.writes[index], wantWrites[index])
		}
	}
	if transport.readCalls != 0 || transport.purgeCalls != 0 {
		t.Fatalf("write-only command waited for a response: reads=%d purges=%d", transport.readCalls, transport.purgeCalls)
	}
}

func TestEnhancedCOMClientNeverRetriesUnknownWrite(t *testing.T) {
	transport := &fakeEnhancedCOMTransport{shortWrite: true}
	client := mustEnhancedCOMClient(t, transport)

	err := client.writeRegister(context.Background(), 0xffffe108, 0xadad00ad)
	var unknown *enhancedCOMUnknownResultError
	if !errors.As(err, &unknown) || !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("first error = %v", err)
	}
	if transport.writeCalls != 1 {
		t.Fatalf("short write calls = %d, want 1", transport.writeCalls)
	}

	err = client.writeRegister(context.Background(), 0xffffe108, 0xad0000ad)
	if !errors.Is(err, errEnhancedCOMUnusable) {
		t.Fatalf("second error = %v, want unusable", err)
	}
	if transport.writeCalls != 1 {
		t.Fatalf("unknown write was retried: %d calls", transport.writeCalls)
	}
}

func TestEnhancedCOMClientTreatsPostWriteCancellationAsUnknown(t *testing.T) {
	t.Run("parent context", func(t *testing.T) {
		transport := &fakeEnhancedCOMTransport{}
		client := mustEnhancedCOMClient(t, transport)
		ctx, cancel := context.WithCancel(context.Background())
		transport.afterWrite = cancel

		err := client.writeRegister(ctx, 0xffffe108, 0xadad00ad)
		var unknown *enhancedCOMUnknownResultError
		if !errors.As(err, &unknown) || !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
		if err := client.writeRegister(context.Background(), 0xffffe108, 0); !errors.Is(err, errEnhancedCOMUnusable) {
			t.Fatalf("write after canceled result = %v", err)
		}
		if transport.writeCalls != 1 {
			t.Fatalf("canceled write was retried: %d calls", transport.writeCalls)
		}
	})

	t.Run("concurrent close", func(t *testing.T) {
		transport := &fakeEnhancedCOMTransport{}
		client := mustEnhancedCOMClient(t, transport)
		transport.afterWrite = func() { _ = client.close() }

		err := client.writeRegister(context.Background(), 0xffffe108, 0xadad00ad)
		var unknown *enhancedCOMUnknownResultError
		if !errors.As(err, &unknown) || !errors.Is(err, errEnhancedCOMClosed) {
			t.Fatalf("error = %v", err)
		}
		if transport.writeCalls != 1 || transport.closeCalls != 1 {
			t.Fatalf("write/close calls = %d/%d", transport.writeCalls, transport.closeCalls)
		}
	})
}

func TestEnhancedCOMClientFailsClosedAfterAmbiguousRead(t *testing.T) {
	transport := &fakeEnhancedCOMTransport{reads: [][]byte{[]byte("000000x1"), nil}}
	client := mustEnhancedCOMClient(t, transport)
	client.wait = func(context.Context, time.Duration) error { return nil }

	_, err := client.readRegister(context.Background(), 0xffffe1dc)
	var unknown *enhancedCOMUnknownResultError
	if !errors.As(err, &unknown) || !strings.Contains(err.Error(), "non-hex") {
		t.Fatalf("read error = %v", err)
	}
	if _, err := client.readRegister(context.Background(), 0xffffe1dc); !errors.Is(err, errEnhancedCOMUnusable) {
		t.Fatalf("second read error = %v, want unusable", err)
	}
	if transport.writeCalls != 1 {
		t.Fatalf("ambiguous read was retried: %d writes", transport.writeCalls)
	}
}

func TestEnhancedCOMClientBoundsEmptyAndOversizedResponses(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		transport := &fakeEnhancedCOMTransport{reads: [][]byte{nil}}
		client := mustEnhancedCOMClient(t, transport)
		client.wait = func(context.Context, time.Duration) error { return nil }
		_, err := client.readRegister(context.Background(), 0xffffe1dc)
		if !errors.Is(err, errEnhancedCOMReadTimeout) {
			t.Fatalf("error = %v, want timeout", err)
		}
	})

	t.Run("oversized", func(t *testing.T) {
		transport := &fakeEnhancedCOMTransport{reads: [][]byte{bytes.Repeat([]byte{'0'}, enhancedCOMMaximumResponseSize+1)}}
		client := mustEnhancedCOMClient(t, transport)
		client.wait = func(context.Context, time.Duration) error { return nil }
		_, err := client.readRegister(context.Background(), 0xffffe1dc)
		if err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("error = %v, want response limit", err)
		}
	})
}

func TestEnhancedCOMClientRejectsCanceledWorkBeforeIOAndClosesOnce(t *testing.T) {
	transport := &fakeEnhancedCOMTransport{}
	client := mustEnhancedCOMClient(t, transport)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := client.writeRegister(ctx, 0xffffe108, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled write error = %v", err)
	}
	if len(transport.calls) != 0 {
		t.Fatalf("canceled work reached transport: %v", transport.calls)
	}
	if err := client.close(); err != nil {
		t.Fatal(err)
	}
	if err := client.close(); err != nil {
		t.Fatal(err)
	}
	if transport.closeCalls != 1 {
		t.Fatalf("close calls = %d, want 1", transport.closeCalls)
	}
	if err := client.writeRegister(context.Background(), 0xffffe108, 0); !errors.Is(err, errEnhancedCOMClosed) {
		t.Fatalf("write after close error = %v", err)
	}
}

func TestEnhancedCOMClientCloseCancelsInitializationWithoutAnotherWake(t *testing.T) {
	transport := &fakeEnhancedCOMTransport{}
	client := mustEnhancedCOMClient(t, transport)
	waiting := make(chan struct{})
	client.wait = func(ctx context.Context, _ time.Duration) error {
		close(waiting)
		<-ctx.Done()
		return ctx.Err()
	}
	result := make(chan error, 1)
	go func() {
		_, err := client.initialize(context.Background())
		result <- err
	}()
	select {
	case <-waiting:
	case <-time.After(time.Second):
		t.Fatal("initialization did not reach its first bounded wait")
	}
	if err := client.close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		var unknown *enhancedCOMUnknownResultError
		if !errors.As(err, &unknown) || !errors.Is(err, context.Canceled) {
			t.Fatalf("initialization error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not cancel initialization")
	}
	if transport.writeCalls != 1 {
		t.Fatalf("wake was repeated after cancellation: %d writes", transport.writeCalls)
	}
}

func mustEnhancedCOMClient(t *testing.T, transport enhancedCOMTransport) *enhancedCOMClient {
	t.Helper()
	client, err := newEnhancedCOMClient(transport, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

type fakeEnhancedCOMTransport struct {
	reads      [][]byte
	writes     [][]byte
	calls      []string
	shortWrite bool
	writeError error
	readError  error
	afterWrite func()
	writeCalls int
	readCalls  int
	purgeCalls int
	closeCalls int
	closeError error
}

func (transport *fakeEnhancedCOMTransport) Read(buffer []byte) (int, error) {
	transport.readCalls++
	if len(transport.reads) == 0 {
		transport.calls = append(transport.calls, "read:0")
		return 0, transport.readError
	}
	data := transport.reads[0]
	transport.reads = transport.reads[1:]
	count := copy(buffer, data)
	transport.calls = append(transport.calls, "read:"+strconv.Itoa(count))
	return count, transport.readError
}

func (transport *fakeEnhancedCOMTransport) Write(buffer []byte) (int, error) {
	transport.writeCalls++
	transport.writes = append(transport.writes, append([]byte(nil), buffer...))
	transport.calls = append(transport.calls, "write:"+strings.ReplaceAll(string(buffer), "\r", "\\r"))
	if transport.afterWrite != nil {
		transport.afterWrite()
	}
	if transport.shortWrite {
		return len(buffer) - 1, transport.writeError
	}
	return len(buffer), transport.writeError
}

func (transport *fakeEnhancedCOMTransport) Close() error {
	transport.closeCalls++
	transport.calls = append(transport.calls, "close")
	return transport.closeError
}

func (transport *fakeEnhancedCOMTransport) SetReadDeadline(time.Time) error  { return nil }
func (transport *fakeEnhancedCOMTransport) SetWriteDeadline(time.Time) error { return nil }

func (transport *fakeEnhancedCOMTransport) PurgeInput() error {
	transport.purgeCalls++
	transport.calls = append(transport.calls, "purge")
	return nil
}

func recordingEnhancedCOMWait(calls *[]string) func(context.Context, time.Duration) error {
	return func(ctx context.Context, duration time.Duration) error {
		*calls = append(*calls, "wait:"+duration.String())
		return ctx.Err()
	}
}
