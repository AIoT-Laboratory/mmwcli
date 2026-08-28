package iwr6843

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestFirmwareDownloaderSubmitsExactXWR6843Sequence(t *testing.T) {
	assets := fixtureSubmissionAssets(t)
	memory := &fakeEnhancedCOMMemory{reads: map[uint32][]uint32{
		0xffffe1dc: {0, 0xad010100},
		0xffffe3cc: {0xabcd1234},
		0xffffe3ac: {0x40, 0xc0},
		0xffffff6c: {1, 3},
	}}
	downloader := mustFirmwareDownloader(t, memory)
	downloader.wait = func(ctx context.Context, duration time.Duration) error {
		memory.calls = append(memory.calls, "wait:"+duration.String())
		return ctx.Err()
	}

	receipt, err := downloader.submit(context.Background(), assets)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State != firmwareSubmittedUnverified || receipt.BSSBlocks != 1 || receipt.MSSBlocks != 1 {
		t.Fatalf("receipt = %+v", receipt)
	}
	want := []string{
		"W:FFFFE108:ADAD00AD", "W:FFFFE108:AD0000AD", "W:FFFFE108:AD000000",
		"W:FFFFE1D8:AD000100", "R:FFFFE1DC", "wait:100ms", "R:FFFFE1DC",
		"W:FFFFE1D8:00000000", "W:FFFFE10C:00000000", "W:FFFFE3B8:00000067",
		"R:FFFFE3CC", "W:FFFFE3CC:10101234", "W:FFFFE140:000000AD",
		"W:FFFFE1D8:AD000000", "W:FFFFE3A8:000000C0", "R:FFFFE3AC",
		"wait:100ms", "R:FFFFE3AC", "W:FFFFE1D8:00000000", "B:40000000:8",
		"W:FFFFE26C:00000001", "W:FFFFFF5C:AD000003", "R:FFFFFF6C",
		"wait:100ms", "R:FFFFFF6C", "W:FFFFFF5C:00000000", "W:FFFFFF20:0000AD00",
		"B:00002000:8", "W:FFFFE26C:00000000",
	}
	if strings.Join(memory.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(memory.calls, "\n"), strings.Join(want, "\n"))
	}
}

func TestFirmwareDownloaderBoundsPollAndDoesNotContinue(t *testing.T) {
	memory := &fakeEnhancedCOMMemory{reads: map[uint32][]uint32{
		0xffffe1dc: make([]uint32, firmwarePollReads),
	}}
	downloader := mustFirmwareDownloader(t, memory)
	downloader.wait = func(context.Context, time.Duration) error {
		memory.waits++
		return nil
	}

	_, err := downloader.submit(context.Background(), fixtureSubmissionAssets(t))
	var submission *firmwareSubmissionError
	var notReady *firmwareNotReadyError
	if !errors.As(err, &submission) || !submission.RequiresReset || !errors.As(err, &notReady) {
		t.Fatalf("error = %v", err)
	}
	if notReady.Reads != firmwarePollReads || memory.readCounts[0xffffe1dc] != firmwarePollReads {
		t.Fatalf("poll reads = %d/%d", notReady.Reads, memory.readCounts[0xffffe1dc])
	}
	if memory.waits != firmwarePollReads-1 {
		t.Fatalf("poll waits = %d, want %d", memory.waits, firmwarePollReads-1)
	}
	for _, call := range memory.calls {
		if strings.HasPrefix(call, "B:") || strings.Contains(call, "FFFFE26C") {
			t.Fatalf("poll failure continued into payload/MSS: %v", memory.calls)
		}
	}
}

func TestFirmwareDownloaderBoundsMSSPollDespiteStudioReferenceBug(t *testing.T) {
	memory := &fakeEnhancedCOMMemory{reads: map[uint32][]uint32{
		0xffffff6c: make([]uint32, firmwarePollReads),
	}}
	downloader := mustFirmwareDownloader(t, memory)
	downloader.wait = func(context.Context, time.Duration) error {
		memory.waits++
		return nil
	}

	err := downloader.submitMSS(context.Background(), []memoryWrite{{address: 0x2000, data: make([]byte, 4)}})
	var notReady *firmwareNotReadyError
	if !errors.As(err, &notReady) || notReady.Register != 0xffffff6c {
		t.Fatalf("error = %v", err)
	}
	if memory.readCounts[0xffffff6c] != firmwarePollReads || memory.waits != firmwarePollReads-1 {
		t.Fatalf("MSS poll reads/waits = %d/%d", memory.readCounts[0xffffff6c], memory.waits)
	}
	if memory.callCount("W:FFFFFF20:0000AD00") != 0 || memory.callCount("B:00002000:4") != 0 ||
		memory.callCount("W:FFFFE26C:00000000") != 0 {
		t.Fatalf("failed MSS poll continued: %v", memory.calls)
	}
}

func TestFirmwareDownloaderNeverContinuesOrReleasesAfterBlockFailure(t *testing.T) {
	t.Run("BSS does not start MSS", func(t *testing.T) {
		memory := readyFakeEnhancedCOMMemory()
		memory.failCall = "B:40000000:8"
		downloader := mustFirmwareDownloader(t, memory)
		downloader.wait = func(context.Context, time.Duration) error { return nil }

		_, err := downloader.submit(context.Background(), fixtureSubmissionAssets(t))
		var submission *firmwareSubmissionError
		if !errors.As(err, &submission) || submission.Step != "BSS block 0" || !submission.RequiresReset {
			t.Fatalf("error = %v", err)
		}
		if memory.callCount("B:40000000:8") != 1 || memory.callCount("W:FFFFE26C:00000001") != 0 {
			t.Fatalf("failed BSS block was retried or MSS started: %v", memory.calls)
		}
	})

	t.Run("MSS remains held", func(t *testing.T) {
		memory := readyFakeEnhancedCOMMemory()
		memory.failCall = "B:00002000:8"
		downloader := mustFirmwareDownloader(t, memory)
		downloader.wait = func(context.Context, time.Duration) error { return nil }

		_, err := downloader.submit(context.Background(), fixtureSubmissionAssets(t))
		var submission *firmwareSubmissionError
		if !errors.As(err, &submission) || submission.Step != "MSS block 0" || !submission.RequiresReset {
			t.Fatalf("error = %v", err)
		}
		if memory.callCount("B:00002000:8") != 1 || memory.callCount("W:FFFFE26C:00000000") != 0 {
			t.Fatalf("failed MSS block was retried or released: %v", memory.calls)
		}
	})
}

func TestFirmwareDownloaderPreflightsBeforeTargetIO(t *testing.T) {
	assets := fixtureSubmissionAssets(t)
	assets.BSS.image.sections[0].address = 4
	memory := &fakeEnhancedCOMMemory{}
	downloader := mustFirmwareDownloader(t, memory)

	_, err := downloader.submit(context.Background(), assets)
	if err == nil || !strings.Contains(err.Error(), "unsupported patch entry") {
		t.Fatalf("error = %v", err)
	}
	if len(memory.calls) != 0 {
		t.Fatalf("invalid assets reached target: %v", memory.calls)
	}
}

func TestFirmwareDownloaderStopsCanceledPollWithoutAnotherRead(t *testing.T) {
	memory := &fakeEnhancedCOMMemory{reads: map[uint32][]uint32{0xffffe1dc: {0}}}
	downloader := mustFirmwareDownloader(t, memory)
	ctx, cancel := context.WithCancel(context.Background())
	downloader.wait = func(context.Context, time.Duration) error {
		cancel()
		return context.Canceled
	}

	_, err := downloader.submit(ctx, fixtureSubmissionAssets(t))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if memory.readCounts[0xffffe1dc] != 1 {
		t.Fatalf("canceled poll reads = %d, want 1", memory.readCounts[0xffffe1dc])
	}
}

func fixtureSubmissionAssets(t *testing.T) Assets {
	t.Helper()
	bssImage, err := parseRPRC(makeRPRCFixture(0, fixtureSection{address: 0, data: []byte("BSS data")}))
	if err != nil {
		t.Fatal(err)
	}
	mssImage, err := parseRPRC(makeRPRCFixture(0x1234, fixtureSection{address: 0x2000, data: []byte("MSS! ")[:4]}))
	if err != nil {
		t.Fatal(err)
	}
	bssWrites, err := planMemoryWrites(bssImage, rprcTargetBSS)
	if err != nil {
		t.Fatal(err)
	}
	mssWrites, err := planMemoryWrites(mssImage, rprcTargetMSS)
	if err != nil {
		t.Fatal(err)
	}
	return Assets{
		BSS: File{
			Role: "BSS", EntryPoint: bssImage.entryPoints[0], RPRCVersion: bssImage.version,
			Sections: len(bssImage.sections), Writes: len(bssWrites), image: bssImage, writePlan: bssWrites,
		},
		MSS: File{
			Role: "MSS", EntryPoint: mssImage.entryPoints[0], RPRCVersion: mssImage.version,
			Sections: len(mssImage.sections), Writes: len(mssWrites), image: mssImage, writePlan: mssWrites,
		},
	}
}

func readyFakeEnhancedCOMMemory() *fakeEnhancedCOMMemory {
	return &fakeEnhancedCOMMemory{reads: map[uint32][]uint32{
		0xffffe1dc: {0xad010100},
		0xffffe3cc: {0},
		0xffffe3ac: {0xc0},
		0xffffff6c: {3},
	}}
}

func mustFirmwareDownloader(t *testing.T, memory enhancedCOMMemory) *firmwareDownloader {
	t.Helper()
	downloader, err := newFirmwareDownloader(memory)
	if err != nil {
		t.Fatal(err)
	}
	return downloader
}

type fakeEnhancedCOMMemory struct {
	reads      map[uint32][]uint32
	readCounts map[uint32]int
	calls      []string
	waits      int
	failCall   string
}

func (memory *fakeEnhancedCOMMemory) readRegister(_ context.Context, address uint32) (uint32, error) {
	if memory.readCounts == nil {
		memory.readCounts = make(map[uint32]int)
	}
	call := fmt.Sprintf("R:%08X", address)
	memory.calls = append(memory.calls, call)
	memory.readCounts[address]++
	if call == memory.failCall {
		return 0, errors.New("injected read failure")
	}
	values := memory.reads[address]
	if len(values) == 0 {
		return 0, fmt.Errorf("no fake value for 0x%08X", address)
	}
	memory.reads[address] = values[1:]
	return values[0], nil
}

func (memory *fakeEnhancedCOMMemory) writeRegister(_ context.Context, address, value uint32) error {
	call := fmt.Sprintf("W:%08X:%08X", address, value)
	memory.calls = append(memory.calls, call)
	if call == memory.failCall {
		return errors.New("injected register write failure")
	}
	return nil
}

func (memory *fakeEnhancedCOMMemory) writeBlock(_ context.Context, address uint32, data []byte) error {
	call := fmt.Sprintf("B:%08X:%d", address, len(data))
	memory.calls = append(memory.calls, call)
	if call == memory.failCall {
		return errors.New("injected block write failure")
	}
	return nil
}

func (memory *fakeEnhancedCOMMemory) callCount(want string) int {
	count := 0
	for _, call := range memory.calls {
		if call == want {
			count++
		}
	}
	return count
}
