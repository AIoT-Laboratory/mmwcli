package debugcapture

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	firmwarePollReads    = 11
	firmwarePollInterval = 100 * time.Millisecond
)

type enhancedCOMMemory interface {
	readRegister(context.Context, uint32) (uint32, error)
	writeRegister(context.Context, uint32, uint32) error
	writeBlock(context.Context, uint32, []byte) error
}

type firmwareSubmissionState string

const firmwareSubmittedUnverified firmwareSubmissionState = "submitted-unverified"

type firmwareSubmissionReceipt struct {
	State     firmwareSubmissionState
	BSSBlocks int
	MSSBlocks int
}

// firmwareSubmissionError means the target submission sequence was entered.
// It conservatively requires an explicit reset because the abstract memory
// boundary cannot always prove whether a failed first call emitted bytes. The
// downloader performs no automatic release or reset because either action
// could compound an unknown device state.
type firmwareSubmissionError struct {
	Step          string
	RequiresReset bool
	Cause         error
}

func (err *firmwareSubmissionError) Error() string {
	reset := ""
	if err.RequiresReset {
		reset = "; target state requires an explicit reset"
	}
	return fmt.Sprintf("firmware submission stopped at %s%s: %v", err.Step, reset, err.Cause)
}

func (err *firmwareSubmissionError) Unwrap() error { return err.Cause }

type firmwareNotReadyError struct {
	Register uint32
	Reads    int
	Last     uint32
}

func (err *firmwareNotReadyError) Error() string {
	return fmt.Sprintf(
		"register 0x%08X was not ready after %d reads; last=0x%08X",
		err.Register,
		err.Reads,
		err.Last,
	)
}

type firmwareDownloader struct {
	memory enhancedCOMMemory
	wait   func(context.Context, time.Duration) error
}

func newFirmwareDownloader(memory enhancedCOMMemory) (*firmwareDownloader, error) {
	if memory == nil {
		return nil, errors.New("Enhanced COM memory interface is nil")
	}
	return &firmwareDownloader{memory: memory, wait: waitContext}, nil
}

func (downloader *firmwareDownloader) submit(
	ctx context.Context,
	assets Assets,
) (firmwareSubmissionReceipt, error) {
	plans, err := preflightFirmwareSubmission(assets)
	if err != nil {
		return firmwareSubmissionReceipt{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return firmwareSubmissionReceipt{}, err
	}

	if err := downloader.submitBSS(ctx, plans.bss); err != nil {
		return firmwareSubmissionReceipt{}, err
	}
	if err := downloader.submitMSS(ctx, plans.mss); err != nil {
		return firmwareSubmissionReceipt{}, err
	}
	return firmwareSubmissionReceipt{
		State:     firmwareSubmittedUnverified,
		BSSBlocks: len(plans.bss),
		MSSBlocks: len(plans.mss),
	}, nil
}

func (downloader *firmwareDownloader) submitBSS(ctx context.Context, writes []memoryWrite) error {
	registerWrites := []struct {
		step    string
		address uint32
		value   uint32
	}{
		{step: "BSS unlock 1", address: 0xffffe108, value: 0xadad00ad},
		{step: "BSS unlock 2", address: 0xffffe108, value: 0xad0000ad},
		{step: "BSS unlock 3", address: 0xffffe108, value: 0xad000000},
		{step: "BSS boot request", address: 0xffffe1d8, value: 0xad000100},
	}
	for _, write := range registerWrites {
		if err := downloader.writeRegister(ctx, write.step, write.address, write.value); err != nil {
			return err
		}
	}
	if err := downloader.poll(
		ctx,
		"BSS boot readiness",
		0xffffe1dc,
		func(value uint32) bool { return value&0xad010100 != 0 },
	); err != nil {
		return err
	}

	registerWrites = []struct {
		step    string
		address uint32
		value   uint32
	}{
		{step: "BSS clear boot request", address: 0xffffe1d8, value: 0},
		{step: "BSS ROM select", address: 0xffffe10c, value: 0},
		{step: "BSS clock setup", address: 0xffffe3b8, value: 0x67},
	}
	for _, write := range registerWrites {
		if err := downloader.writeRegister(ctx, write.step, write.address, write.value); err != nil {
			return err
		}
	}
	clockStatus, err := downloader.memory.readRegister(ctx, 0xffffe3cc)
	if err != nil {
		return submissionFailure("BSS clock status read", err)
	}
	if err := downloader.writeRegister(
		ctx,
		"BSS clock status update",
		0xffffe3cc,
		(clockStatus&0xffff)|0x10100000,
	); err != nil {
		return err
	}

	registerWrites = []struct {
		step    string
		address uint32
		value   uint32
	}{
		{step: "BSS ROM mode", address: 0xffffe140, value: 0xad},
		{step: "BSS power request", address: 0xffffe1d8, value: 0xad000000},
		{step: "BSS power setup", address: 0xffffe3a8, value: 0xc0},
	}
	for _, write := range registerWrites {
		if err := downloader.writeRegister(ctx, write.step, write.address, write.value); err != nil {
			return err
		}
	}
	if err := downloader.poll(
		ctx,
		"BSS power readiness",
		0xffffe3ac,
		func(value uint32) bool { return value&0xc0 == 0xc0 },
	); err != nil {
		return err
	}
	if err := downloader.writeRegister(ctx, "BSS clear power request", 0xffffe1d8, 0); err != nil {
		return err
	}
	return downloader.writePlan(ctx, "BSS", writes)
}

func (downloader *firmwareDownloader) submitMSS(ctx context.Context, writes []memoryWrite) error {
	if err := downloader.writeRegister(ctx, "MSS hold", 0xffffe26c, 1); err != nil {
		return err
	}
	if err := downloader.writeRegister(ctx, "MSS boot request", 0xffffff5c, 0xad000003); err != nil {
		return err
	}
	if err := downloader.poll(
		ctx,
		"MSS boot readiness",
		0xffffff6c,
		func(value uint32) bool { return value&3 == 3 },
	); err != nil {
		return err
	}
	if err := downloader.writeRegister(ctx, "MSS clear boot request", 0xffffff5c, 0); err != nil {
		return err
	}
	if err := downloader.writeRegister(ctx, "MSS memory mapping", 0xffffff20, 0xad00); err != nil {
		return err
	}
	if err := downloader.writePlan(ctx, "MSS", writes); err != nil {
		return err
	}
	return downloader.writeRegister(ctx, "MSS release", 0xffffe26c, 0)
}

func (downloader *firmwareDownloader) writeRegister(
	ctx context.Context,
	step string,
	address, value uint32,
) error {
	if err := downloader.memory.writeRegister(ctx, address, value); err != nil {
		return submissionFailure(step, err)
	}
	return nil
}

func (downloader *firmwareDownloader) writePlan(ctx context.Context, role string, writes []memoryWrite) error {
	for index, write := range writes {
		if err := downloader.memory.writeBlock(ctx, write.address, write.data); err != nil {
			return submissionFailure(fmt.Sprintf("%s block %d", role, index), err)
		}
	}
	return nil
}

func (downloader *firmwareDownloader) poll(
	ctx context.Context,
	step string,
	address uint32,
	ready func(uint32) bool,
) error {
	var last uint32
	for read := range firmwarePollReads {
		value, err := downloader.memory.readRegister(ctx, address)
		if err != nil {
			return submissionFailure(step, err)
		}
		last = value
		if ready(value) {
			return nil
		}
		if read+1 == firmwarePollReads {
			break
		}
		if err := downloader.wait(ctx, firmwarePollInterval); err != nil {
			return submissionFailure(step, err)
		}
	}
	return submissionFailure(step, &firmwareNotReadyError{
		Register: address,
		Reads:    firmwarePollReads,
		Last:     last,
	})
}

func submissionFailure(step string, cause error) error {
	return &firmwareSubmissionError{Step: step, RequiresReset: true, Cause: cause}
}

type firmwareSubmissionPlans struct {
	bss []memoryWrite
	mss []memoryWrite
}

func preflightFirmwareSubmission(assets Assets) (firmwareSubmissionPlans, error) {
	if len(assets.BSS.image.sections) == 0 {
		return firmwareSubmissionPlans{}, errors.New("debug-capture BSS firmware has no RPRC sections")
	}
	if assets.BSS.image.sections[0].address != 0 {
		return firmwareSubmissionPlans{}, fmt.Errorf(
			"debug-capture BSS firmware uses unsupported patch entry 0x%X; xWR6843 ROM image entry must be zero",
			assets.BSS.image.sections[0].address,
		)
	}
	bss, err := preflightFirmwareFile(assets.BSS, "BSS", rprcTargetBSS)
	if err != nil {
		return firmwareSubmissionPlans{}, err
	}
	mss, err := preflightFirmwareFile(assets.MSS, "MSS", rprcTargetMSS)
	if err != nil {
		return firmwareSubmissionPlans{}, err
	}
	return firmwareSubmissionPlans{bss: bss, mss: mss}, nil
}

func preflightFirmwareFile(file File, role string, target rprcTarget) ([]memoryWrite, error) {
	if file.Role != role {
		return nil, fmt.Errorf("debug-capture %s firmware role is %q", role, file.Role)
	}
	if file.Sections != len(file.image.sections) || file.RPRCVersion != file.image.version ||
		file.EntryPoint != file.image.entryPoints[0] {
		return nil, fmt.Errorf("debug-capture %s firmware metadata does not match its RPRC image", role)
	}
	planned, err := planMemoryWrites(file.image, target)
	if err != nil {
		return nil, fmt.Errorf("plan debug-capture %s firmware submission: %w", role, err)
	}
	if len(planned) == 0 {
		return nil, fmt.Errorf("debug-capture %s firmware has no memory writes", role)
	}
	if file.Writes != len(planned) || !equalMemoryWrites(file.writePlan, planned) {
		return nil, fmt.Errorf("debug-capture %s firmware write plan does not match its RPRC image", role)
	}
	for index, write := range planned {
		if _, err := encodeEnhancedCOMBlockWrite(write.address, write.data); err != nil {
			return nil, fmt.Errorf("debug-capture %s firmware block %d: %w", role, index, err)
		}
	}
	return planned, nil
}

func equalMemoryWrites(first, second []memoryWrite) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index].section != second[index].section || first[index].address != second[index].address ||
			!bytes.Equal(first[index].data, second[index].data) {
			return false
		}
	}
	return true
}
