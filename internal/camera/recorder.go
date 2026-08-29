// Package camera records one required concatenated JPEG stream directly into
// a take transaction.
package camera

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	PayloadName = "camera.mjpeg"
	IndexName   = "camera.index.bin"
	// Executable is the sole camera producer supported by this package.
	Executable = "ffmpeg"
)

type Config struct {
	Device   string `json:"device"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	FPS      int    `json:"fps"`
	MaxBytes int    `json:"max_bytes"`
}

func (config Config) Validate() error {
	if config.Device == "" || config.Device != strings.TrimSpace(config.Device) || strings.IndexByte(config.Device, 0) >= 0 {
		return errors.New("camera device must be a non-empty value without NUL")
	}
	if config.Width < 1 || config.Width > 16_384 || config.Height < 1 || config.Height > 16_384 {
		return errors.New("camera width and height must be in 1..16384")
	}
	if config.FPS < 1 || config.FPS > 240 {
		return errors.New("camera fps must be in 1..240")
	}
	if config.MaxBytes < 4 || config.MaxBytes > 64<<20 {
		return errors.New("camera max_bytes must be in [4, 67108864]")
	}
	return nil
}

func (config Config) command(oneFrame bool) []string {
	argv := []string{
		Executable, "-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "dshow",
		"-video_size", strconv.Itoa(config.Width) + "x" + strconv.Itoa(config.Height),
		"-framerate", strconv.Itoa(config.FPS),
		"-i", "video=" + config.Device,
		"-an", "-c:v", "mjpeg",
	}
	if oneFrame {
		argv = append(argv, "-frames:v", "1")
	}
	return append(argv, "-f", "image2pipe", "pipe:1")
}

type Artifact struct {
	Path   string
	Bytes  uint64
	SHA256 [sha256.Size]byte
}

type Result struct {
	FrameCount uint64
	Payload    Artifact
	Index      Artifact
}

type frame struct {
	payload []byte
	err     error
}

type Recorder struct {
	baseCtx    context.Context
	cancel     context.CancelFunc
	config     Config
	hostOrigin time.Time
	stage      string
	stderr     io.Writer

	mu          sync.Mutex
	command     *exec.Cmd
	stdout      io.ReadCloser
	payload     *os.File
	frames      chan frame
	done        chan struct{}
	ready       chan error
	readyOnce   sync.Once
	started     bool
	stopping    bool
	finished    bool
	err         error
	entries     []entry
	bytes       uint64
	payloadHash hash.Hash
	result      Result
}

func New(
	ctx context.Context,
	cancel context.CancelFunc,
	config Config,
	hostOrigin time.Time,
	stage string,
	stderr io.Writer,
) (*Recorder, error) {
	if ctx == nil || cancel == nil || hostOrigin.IsZero() || stage == "" {
		return nil, errors.New("camera recorder dependencies are incomplete")
	}
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("camera recorder configuration is invalid: %w", err)
	}
	if stderr == nil {
		stderr = io.Discard
	}
	payloadPath := filepath.Join(stage, PayloadName+".part")
	payload, err := os.OpenFile(payloadPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("create camera payload: %w", err)
	}
	return &Recorder{
		baseCtx: ctx, cancel: cancel, config: config, hostOrigin: hostOrigin,
		stage: stage, stderr: stderr, payload: payload,
		frames: make(chan frame), done: make(chan struct{}), ready: make(chan error, 1),
		payloadHash: sha256.New(),
	}, nil
}

func (recorder *Recorder) Arm(ctx context.Context) error {
	recorder.mu.Lock()
	if recorder.command != nil || recorder.finished {
		recorder.mu.Unlock()
		return errors.New("camera recorder cannot be armed twice")
	}
	argv := recorder.config.command(false)
	command := exec.CommandContext(recorder.baseCtx, argv[0], argv[1:]...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		recorder.mu.Unlock()
		return fmt.Errorf("open camera stdout: %w", err)
	}
	command.Stderr = recorder.stderr
	if err := command.Start(); err != nil {
		_ = stdout.Close()
		recorder.mu.Unlock()
		return fmt.Errorf("start camera: %w", err)
	}
	recorder.command = command
	recorder.stdout = stdout
	go recorder.read(stdout)
	go recorder.write()
	recorder.mu.Unlock()
	select {
	case err := <-recorder.ready:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (recorder *Recorder) Start(context.Context) error {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.command == nil || recorder.started || recorder.finished {
		return errors.New("camera recorder is not armed or already started")
	}
	if recorder.err != nil {
		return recorder.err
	}
	recorder.started = true
	return nil
}

func (recorder *Recorder) Finish(_ context.Context, complete bool) error {
	recorder.mu.Lock()
	if recorder.finished {
		err := recorder.err
		recorder.mu.Unlock()
		return err
	}
	recorder.stopping = true
	command := recorder.command
	stdout := recorder.stdout
	recorder.mu.Unlock()

	if command != nil && command.Process != nil {
		_ = command.Process.Kill()
	}
	if stdout != nil {
		_ = stdout.Close()
	}
	if command != nil {
		_ = command.Wait()
		<-recorder.done
	}

	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.finished = true
	closeErr := recorder.closePayloadLocked()
	if !complete {
		return errors.Join(recorder.err, closeErr)
	}
	if recorder.err != nil || closeErr != nil {
		return errors.Join(recorder.err, closeErr)
	}
	index, err := encodeIndex(recorder.entries, recorder.bytes)
	if err != nil {
		return err
	}
	indexPart := filepath.Join(recorder.stage, IndexName+".part")
	if err := writeSynced(indexPart, index); err != nil {
		return err
	}
	if err := os.Rename(
		filepath.Join(recorder.stage, PayloadName+".part"),
		filepath.Join(recorder.stage, PayloadName),
	); err != nil {
		return fmt.Errorf("publish camera payload: %w", err)
	}
	if err := os.Rename(indexPart, filepath.Join(recorder.stage, IndexName)); err != nil {
		return fmt.Errorf("publish camera index: %w", err)
	}
	var payloadHash [sha256.Size]byte
	copy(payloadHash[:], recorder.payloadHash.Sum(nil))
	recorder.result = Result{
		FrameCount: uint64(len(recorder.entries)),
		Payload:    Artifact{Path: PayloadName, Bytes: recorder.bytes, SHA256: payloadHash},
		Index: Artifact{
			Path: IndexName, Bytes: uint64(len(index)), SHA256: sha256.Sum256(index),
		},
	}
	return nil
}

func (recorder *Recorder) Result() (Result, error) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if !recorder.finished || recorder.result.FrameCount == 0 || recorder.err != nil {
		return Result{}, errors.Join(errors.New("camera recorder has no complete result"), recorder.err)
	}
	return recorder.result, nil
}

func (recorder *Recorder) read(output io.Reader) {
	defer close(recorder.frames)
	reader := bufio.NewReaderSize(output, 64<<10)
	for {
		payload, err := readJPEG(reader, recorder.config.MaxBytes)
		if err != nil {
			recorder.frames <- frame{err: err}
			return
		}
		recorder.frames <- frame{payload: payload}
	}
}

func (recorder *Recorder) write() {
	defer close(recorder.done)
	for item := range recorder.frames {
		recorder.mu.Lock()
		if item.err != nil {
			if !recorder.stopping {
				err := fmt.Errorf("read camera JPEG: %w", item.err)
				recorder.signalReady(err)
				recorder.failLocked(err)
			}
			recorder.mu.Unlock()
			return
		}
		if recorder.stopping {
			recorder.mu.Unlock()
			continue
		}
		if !recorder.started {
			recorder.signalReady(nil)
			recorder.mu.Unlock()
			continue
		}
		received := time.Since(recorder.hostOrigin)
		if received < 0 {
			recorder.failLocked(errors.New("camera receive time precedes take origin"))
			recorder.mu.Unlock()
			return
		}
		written, err := recorder.payload.Write(item.payload)
		if err == nil && written != len(item.payload) {
			err = io.ErrShortWrite
		}
		if err == nil {
			_, err = recorder.payloadHash.Write(item.payload)
		}
		if err != nil {
			recorder.failLocked(fmt.Errorf("write camera payload: %w", err))
			recorder.mu.Unlock()
			return
		}
		size := uint64(len(item.payload))
		recorder.entries = append(recorder.entries, entry{
			offset: recorder.bytes, size: size, receivedNS: uint64(received),
		})
		recorder.bytes += size
		recorder.mu.Unlock()
	}
}

func (recorder *Recorder) failLocked(err error) {
	if recorder.err == nil {
		recorder.err = err
		recorder.cancel()
	}
}

func (recorder *Recorder) signalReady(err error) {
	recorder.readyOnce.Do(func() { recorder.ready <- err })
}

func (recorder *Recorder) closePayloadLocked() error {
	if recorder.payload == nil {
		return nil
	}
	err := recorder.payload.Sync()
	err = errors.Join(err, recorder.payload.Close())
	recorder.payload = nil
	return err
}

func writeSynced(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create camera index: %w", err)
	}
	written, writeErr := file.Write(data)
	if writeErr == nil && written != len(data) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	return errors.Join(writeErr, file.Close())
}
