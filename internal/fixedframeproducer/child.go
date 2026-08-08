package fixedframeproducer

import (
	"context"
	"io"
	"os"
	"os/exec"
	"sync"
)

type frameEvent struct {
	payload   []byte
	consumed  chan struct{}
	readBytes int
	readErr   error
}

type cameraChild struct {
	command *exec.Cmd
	output  *os.File

	stopOnce sync.Once
	stop     chan struct{}
	waitDone chan struct{}
	waitErr  error

	events   chan frameEvent
	readDone chan struct{}
}

func startCameraChild(
	ctx context.Context,
	argv []string,
	stderr io.Writer,
) (*cameraChild, error) {
	output, childOutput, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Stdout = childOutput
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		_ = output.Close()
		_ = childOutput.Close()
		return nil, err
	}
	if err := childOutput.Close(); err != nil {
		_ = output.Close()
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, err
	}
	child := &cameraChild{
		command:  command,
		output:   output,
		stop:     make(chan struct{}),
		waitDone: make(chan struct{}),
	}
	go func() {
		child.waitErr = command.Wait()
		close(child.waitDone)
	}()
	return child, nil
}

func (child *cameraChild) stream(frameBytes int) <-chan frameEvent {
	child.events = make(chan frameEvent)
	child.readDone = make(chan struct{})
	go child.readFrames(frameBytes)
	return child.events
}

func (child *cameraChild) readFrames(frameBytes int) {
	defer close(child.events)
	defer close(child.readDone)
	buffer := make([]byte, frameBytes)
	for {
		readBytes, err := io.ReadFull(child.output, buffer)
		if err != nil {
			select {
			case child.events <- frameEvent{readBytes: readBytes, readErr: err}:
			case <-child.stop:
			}
			return
		}
		consumed := make(chan struct{})
		select {
		case child.events <- frameEvent{payload: buffer, consumed: consumed}:
		case <-child.stop:
			return
		}
		select {
		case <-consumed:
		case <-child.stop:
			return
		}
	}
}

func (child *cameraChild) terminate() (bool, error) {
	alreadyExited := false
	select {
	case <-child.waitDone:
		alreadyExited = true
	default:
	}
	child.stopOnce.Do(func() {
		close(child.stop)
		_ = child.output.Close()
		if !alreadyExited && child.command.Process != nil {
			_ = child.command.Process.Kill()
		}
	})
	<-child.waitDone
	if child.readDone != nil {
		<-child.readDone
	}
	return alreadyExited, child.waitErr
}
