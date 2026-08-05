package debugcapture

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"mmwcli/internal/d2xx"
)

const (
	sop2BitBangBaud        = uint32(115200)
	sop2GPIOPinMask        = byte(0x1c)
	sop2GPIODevelopment    = byte(0x18)
	sop2GPIOOutputMask     = byte(0xff)
	sop2ResetOutputMask    = byte(0xf9)
	sop2ResetPinMask       = byte(0x40)
	sop2LatchWait          = 300 * time.Millisecond
	sop2ResetOpenWait      = 500 * time.Millisecond
	sop2ResetDeviceWait    = 2 * time.Millisecond
	sop2ResetLowWait       = 500 * time.Millisecond
	sop2ResetReleaseWait   = 500 * time.Millisecond
	sop2BitBangReadWaitMS  = uint32(500)
	sop2BitBangWriteWaitMS = uint32(100)
)

type sop2ResetStateError struct{ err error }

func (failure *sop2ResetStateError) Error() string {
	return "SOP2 reset target state is unknown: " + failure.err.Error()
}

func (failure *sop2ResetStateError) Unwrap() error { return failure.err }

type boardControlSelectors struct {
	reset d2xx.Selector
	gpio  d2xx.Selector
}

type bitBangDevice interface {
	io.WriteCloser
	SetTimeouts(uint32, uint32) error
	SetLatencyTimer(byte) error
	SetBitMode(byte, byte) error
	GetBitMode() (byte, error)
	SetBaudRate(uint32) error
	SetUSBParameters(uint32, uint32) error
}

type boardControlBackend struct {
	open  func(d2xx.Selector) (bitBangDevice, error)
	close func() error
	wait  func(context.Context, time.Duration) error
}

func prepareSOP2Target(ctx context.Context, selectors D2XXSelectors) error {
	derived, err := deriveBoardControlSelectors(selectors)
	if err != nil {
		return err
	}
	library, err := d2xx.Load()
	if err != nil {
		return err
	}
	return prepareSOP2TargetWithBackend(ctx, derived, boardControlBackend{
		open: func(selector d2xx.Selector) (bitBangDevice, error) {
			return library.Open(selector)
		},
		close: library.Close,
		wait:  waitContext,
	})
}

func deriveBoardControlSelectors(selectors D2XXSelectors) (boardControlSelectors, error) {
	if err := selectors.validate(); err != nil {
		return boardControlSelectors{}, err
	}
	var base string
	switch selectors.SPI.By {
	case d2xx.SelectBySerialNumber:
		base = selectors.SPI.Value[:len(selectors.SPI.Value)-1]
		return boardControlSelectors{
			reset: d2xx.Selector{By: selectors.SPI.By, Value: base + "C"},
			gpio:  d2xx.Selector{By: selectors.SPI.By, Value: base + "D"},
		}, nil
	case d2xx.SelectByDescription:
		base = selectors.SPI.Value[:len(selectors.SPI.Value)-2]
		return boardControlSelectors{
			reset: d2xx.Selector{By: selectors.SPI.By, Value: base + " C"},
			gpio:  d2xx.Selector{By: selectors.SPI.By, Value: base + " D"},
		}, nil
	default:
		return boardControlSelectors{}, fmt.Errorf("unsupported D2XX selection method %d", selectors.SPI.By)
	}
}

func prepareSOP2TargetWithBackend(
	ctx context.Context,
	selectors boardControlSelectors,
	backend boardControlBackend,
) (resultErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	operationContext, cancel := withNativeDeadline(ctx)
	defer cancel()
	ctx = operationContext
	if backend.open == nil || backend.close == nil || backend.wait == nil {
		return errors.New("SOP2 board-control backend is incomplete")
	}
	if err := selectors.gpio.Validate(); err != nil {
		return errors.Join(fmt.Errorf("SOP2 GPIO selector: %w", err), backend.close())
	}
	if err := selectors.reset.Validate(); err != nil {
		return errors.Join(fmt.Errorf("SOP2 reset selector: %w", err), backend.close())
	}
	if err := checkContext(ctx); err != nil {
		return errors.Join(err, backend.close())
	}

	gpio, err := backend.open(selectors.gpio)
	if err != nil {
		resultErr := fmt.Errorf("open D2XX GPIO interface D: %w", err)
		if gpio != nil {
			resultErr = errors.Join(resultErr, closeBitBangDevice("GPIO interface D", gpio), backend.close())
			return &sop2ResetStateError{err: resultErr}
		}
		return errors.Join(resultErr, backend.close())
	}
	if gpio == nil {
		return errors.Join(errors.New("D2XX GPIO opener returned a nil device"), backend.close())
	}
	var reset bitBangDevice
	// Cleanup resets the bit-bang mode, so any path after a successful open may
	// have driven the target-facing pins even if cancellation wins immediately.
	stateMayHaveChanged := true
	defer func() {
		var cleanupErr error
		if reset != nil {
			cleanupErr = errors.Join(cleanupErr, closeBitBangDevice("reset interface C", reset))
		}
		cleanupErr = errors.Join(cleanupErr, closeBitBangDevice("GPIO interface D", gpio))
		cleanupErr = errors.Join(cleanupErr, backend.close())
		resultErr = errors.Join(resultErr, cleanupErr)
		if resultErr != nil && stateMayHaveChanged {
			resultErr = &sop2ResetStateError{err: resultErr}
		}
	}()

	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := initializeBitBangDevice(ctx, gpio, sop2GPIOOutputMask); err != nil {
		return fmt.Errorf("initialize D2XX GPIO interface D: %w", err)
	}
	if err := updateBitBangPins(ctx, gpio, sop2GPIOPinMask, sop2GPIODevelopment, "write SOP2 on interface D"); err != nil {
		return err
	}
	if err := backend.wait(ctx, sop2LatchWait); err != nil {
		return err
	}

	reset, err = backend.open(selectors.reset)
	if err != nil {
		return fmt.Errorf("open D2XX reset interface C: %w", err)
	}
	if reset == nil {
		return errors.New("D2XX reset opener returned a nil device")
	}
	if err := initializeBitBangDevice(ctx, reset, sop2ResetOutputMask); err != nil {
		return fmt.Errorf("initialize D2XX reset interface C: %w", err)
	}
	if err := backend.wait(ctx, sop2ResetOpenWait); err != nil {
		return err
	}
	if err := updateBitBangPins(ctx, reset, sop2ResetPinMask, 0, "assert NRST on interface C"); err != nil {
		return err
	}
	if err := backend.wait(ctx, sop2ResetDeviceWait); err != nil {
		return err
	}
	if err := backend.wait(ctx, sop2ResetLowWait); err != nil {
		return err
	}
	if err := updateBitBangPins(ctx, reset, 0, sop2ResetPinMask, "release NRST on interface C"); err != nil {
		return err
	}
	if err := backend.wait(ctx, sop2ResetDeviceWait); err != nil {
		return err
	}
	return backend.wait(ctx, sop2ResetReleaseWait)
}

func initializeBitBangDevice(ctx context.Context, device bitBangDevice, outputMask byte) error {
	return runNativeSteps(ctx,
		func() error { return device.SetBitMode(0, d2xx.BitModeReset) },
		func() error { return device.SetTimeouts(sop2BitBangReadWaitMS, sop2BitBangWriteWaitMS) },
		func() error { return device.SetUSBParameters(4096, 4096) },
		func() error { return device.SetLatencyTimer(1) },
		func() error { return device.SetBitMode(outputMask, d2xx.BitModeAsyncBitBang) },
		func() error { return device.SetBaudRate(sop2BitBangBaud) },
	)
}

func updateBitBangPins(
	ctx context.Context,
	device bitBangDevice,
	clearMask, setMask byte,
	operation string,
) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	current, err := device.GetBitMode()
	if err != nil {
		return fmt.Errorf("%s: read current pins: %w", operation, err)
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	value := current&^clearMask | setMask
	written, writeErr := device.Write([]byte{value})
	if written == 1 && writeErr == nil {
		return nil
	}
	if written < 0 || written > 1 {
		writeErr = errors.Join(fmt.Errorf("invalid D2XX write count %d", written), writeErr)
	} else if writeErr == nil {
		writeErr = io.ErrShortWrite
	}
	return fmt.Errorf("%s result is unknown after writing %d of 1 byte: %w", operation, written, writeErr)
}

func closeBitBangDevice(name string, device bitBangDevice) error {
	if device == nil {
		return nil
	}
	return errors.Join(
		wrapOptionalError("reset bit mode on "+name, device.SetBitMode(0, d2xx.BitModeReset)),
		wrapOptionalError("close "+name, device.Close()),
	)
}

func wrapOptionalError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
