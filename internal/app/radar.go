package app

import (
	"fmt"
	"io"
	"os"

	"mmwcli/internal/radar"
)

const captureConfigMaxBytes = 4 << 20

type loadedPlan struct {
	plan   radar.Plan
	source []byte
}

func readRadarConfig(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open radar configuration %s: %w", path, err)
	}
	defer file.Close()
	snapshot, err := io.ReadAll(io.LimitReader(file, captureConfigMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read radar configuration %s: %w", path, err)
	}
	if len(snapshot) > captureConfigMaxBytes {
		return nil, fmt.Errorf("radar configuration exceeds %d bytes", captureConfigMaxBytes)
	}
	return snapshot, nil
}

func printFiniteCapture(writer io.Writer, plan radar.Plan) {
	fmt.Fprintf(
		writer,
		"radar: IWR6843 frames=%d period=%s bytes/frame=%d total=%d\n",
		plan.NumberOfFrames,
		plan.FramePeriod,
		plan.BytesPerFrame,
		plan.ExpectedBytes,
	)
}
