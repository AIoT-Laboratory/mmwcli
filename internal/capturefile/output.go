package capturefile

import (
	"context"
	"io"
)

// Output is the transaction boundary used by the capture session. An output
// is committed only after hardware cleanup and capture validation both succeed.
type Output interface {
	io.WriterAt
	Truncate(int64) error
	CommitContext(context.Context) error
	Close() error
	Committed() bool
	captureOutput()
}

func (*File) captureOutput() {}

// IsUsableOutput rejects nil interfaces, typed nils, and outputs whose ADC
// staging handle has already been closed. Output is sealed to this package, so
// this check covers every implementation.
func IsUsableOutput(output Output) bool {
	switch output := output.(type) {
	case *File:
		return output != nil && output.file != nil
	default:
		return false
	}
}

var _ Output = (*File)(nil)
