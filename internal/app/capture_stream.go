package app

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"mmwcli/internal/capturefile"
	"mmwcli/internal/capturestream"
	"mmwcli/internal/session"
)

const captureStreamTerminalTimeout = 3 * time.Second

type activeCaptureStream struct {
	encoder   *capturestream.StdoutEncoder
	mirror    *capturestream.Mirror
	directory *capturefile.SessionDirectory
}

func requireCaptureStreamStdout(enabled bool, stdout io.Writer) (*os.File, error) {
	if !enabled {
		return nil, nil
	}
	file, ok := stdout.(*os.File)
	if !ok || file == nil {
		return nil, usageError{message: "--stream requires stdout to be an OS file or pipe"}
	}
	return file, nil
}

func startCaptureStream(
	ctx context.Context,
	stdout *os.File,
	directory *capturefile.SessionDirectory,
	prepared preparedCaptureOutput,
	mode capturestream.CaptureMode,
	cancelCapture context.CancelFunc,
) (*activeCaptureStream, error) {
	streamID, err := capturestream.NewStreamID()
	if err != nil {
		return nil, err
	}
	encoder, err := capturestream.NewStdoutEncoder(
		ctx,
		stdout,
		capturestream.Session{
			StreamID:        streamID,
			ProducerVersion: Version,
			Mode:            mode,
		},
		prepared.plan,
		prepared.configSnapshot,
	)
	if err != nil {
		return nil, err
	}
	mirror, err := capturestream.NewMirror(
		directory,
		encoder,
		cancelCapture,
		capturestream.DefaultMirrorConfig(
			prepared.plan.BytesPerFrame,
			uint64(prepared.plan.NumberOfFrames),
		),
	)
	if err != nil {
		terminalCtx, cancel := context.WithTimeout(context.Background(), captureStreamTerminalTimeout)
		defer cancel()
		return nil, errors.Join(err, encoder.Abort(terminalCtx, capturestream.AbortIntegrityFailed))
	}
	return &activeCaptureStream{encoder: encoder, mirror: mirror, directory: directory}, nil
}

func (stream *activeCaptureStream) finish(resultErr error) error {
	terminalCtx, cancel := context.WithTimeout(context.Background(), captureStreamTerminalTimeout)
	defer cancel()
	if resultErr != nil {
		stream.mirror.Abort()
		return errors.Join(resultErr, stream.encoder.Abort(terminalCtx, streamAbortReason(resultErr)))
	}
	artifact, err := capturestream.ArtifactFromCommittedSession(stream.directory)
	if err != nil {
		return errors.Join(
			err,
			stream.encoder.Abort(terminalCtx, capturestream.AbortPublishFailed),
		)
	}
	return stream.encoder.Commit(terminalCtx, artifact)
}

func streamAbortReason(err error) capturestream.AbortReason {
	if errors.Is(err, capturestream.ErrMirrorBackpressure) {
		return capturestream.AbortBackpressure
	}
	if errors.Is(err, capturestream.ErrMirrorIntegrity) {
		return capturestream.AbortIntegrityFailed
	}
	var cleanup *session.CleanupError
	if errors.As(err, &cleanup) {
		return capturestream.AbortCleanupFailed
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return capturestream.AbortCancelled
	}
	return capturestream.AbortCaptureFailed
}
