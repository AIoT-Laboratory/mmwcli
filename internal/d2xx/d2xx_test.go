package d2xx

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestStatusErrorIncludesOperationAndStatus(t *testing.T) {
	err := &StatusError{Operation: "FT_OpenEx", Status: StatusNotSupported}
	if got := err.Error(); !strings.Contains(got, "FT_OpenEx") || !strings.Contains(got, "not supported") {
		t.Fatalf("StatusError.Error() = %q", got)
	}
	if got := Status(99).String(); got != "unknown status 99" {
		t.Fatalf("Status(99).String() = %q", got)
	}
}

func TestLibraryClosesOnce(t *testing.T) {
	native := &fakeNativeLibrary{}
	library := &Library{native: native}
	if err := library.Close(); err != nil {
		t.Fatal(err)
	}
	if err := library.Close(); err != nil {
		t.Fatal(err)
	}
	if native.closeCalls != 1 {
		t.Fatalf("close calls = %d, want 1", native.closeCalls)
	}
}

func TestLibraryRequiresExplicitSelectorAndTracksDevice(t *testing.T) {
	nativeDevice := &fakeNativeDevice{}
	native := &fakeNativeLibrary{
		openDevice: nativeDevice,
	}
	library := &Library{native: native}
	selector := Selector{Description: "AR-DevPack-EVM-012 A"}
	device, err := library.Open(selector)
	if err != nil {
		t.Fatal(err)
	}
	if len(native.selectors) != 1 || native.selectors[0] != selector {
		t.Fatalf("selectors = %+v", native.selectors)
	}
	if err := library.Close(); !errors.Is(err, ErrDevicesOpen) {
		t.Fatalf("close with open device = %v", err)
	}
	if err := device.Close(); err != nil {
		t.Fatal(err)
	}
	if err := device.Close(); err != nil {
		t.Fatal(err)
	}
	if nativeDevice.closeCalls != 1 {
		t.Fatalf("device close calls = %d, want 1", nativeDevice.closeCalls)
	}
	if err := library.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLibraryRejectsInvalidSelectorsBeforeNativeOpen(t *testing.T) {
	native := &fakeNativeLibrary{}
	library := &Library{native: native}
	defer library.Close()
	for _, selector := range []Selector{
		{},
		{Description: "A\x00B"},
	} {
		if _, err := library.Open(selector); err == nil {
			t.Fatalf("selector %+v was accepted", selector)
		}
	}
	if len(native.selectors) != 0 {
		t.Fatalf("native open called for invalid selector: %+v", native.selectors)
	}
}

func TestDeviceIOAndConfiguration(t *testing.T) {
	native := &fakeNativeDevice{readData: []byte{0xFA, 0xAB}, bitModeValue: 0xA5}
	device := &Device{native: native, release: func() {}}

	buffer := make([]byte, 2)
	if count, err := device.Read(buffer); err != nil || count != 2 || string(buffer) != string(native.readData) {
		t.Fatalf("Read() = %d, %v, % X", count, err, buffer)
	}
	if count, err := device.Write([]byte{1, 2, 3}); err != nil || count != 3 {
		t.Fatalf("Write() = %d, %v", count, err)
	}
	native.queued = 7
	if count, err := device.QueueStatus(); err != nil || count != 7 {
		t.Fatalf("QueueStatus() = %d, %v", count, err)
	}
	if err := device.SetTimeouts(500, 100); err != nil {
		t.Fatal(err)
	}
	if err := device.SetTimeouts(500, 0); err != nil {
		t.Fatal(err)
	}
	if err := device.SetChars(0, false, 0, false); err != nil {
		t.Fatal(err)
	}
	if err := device.SetLatencyTimer(1); err != nil {
		t.Fatal(err)
	}
	if err := device.SetBitMode(0x5B, BitModeMPSSE); err != nil {
		t.Fatal(err)
	}
	if mode, err := device.GetBitMode(); err != nil || mode != 0xA5 {
		t.Fatalf("GetBitMode() = 0x%02X, %v", mode, err)
	}
	if err := device.SetBaudRate(115200); err != nil {
		t.Fatal(err)
	}
	if err := device.SetUSBParameters(4096, 4096); err != nil {
		t.Fatal(err)
	}
	if err := device.SetUSBParameters(4096, 0); err != nil {
		t.Fatal(err)
	}
	if native.timeouts != [2]uint32{500, 0} || native.chars != [4]byte{} ||
		native.latency != 1 || native.bitMode != [2]byte{0x5B, BitModeMPSSE} || native.baud != 115200 ||
		native.usb != [2]uint32{4096, 0} {
		t.Fatalf("configuration = %+v", native)
	}
}

func TestDeviceCloseFailureIsNotRetried(t *testing.T) {
	nativeDevice := &fakeNativeDevice{closeStatus: StatusIOError}
	nativeLibrary := &fakeNativeLibrary{
		openDevice: nativeDevice,
	}
	library := &Library{native: nativeLibrary}
	device, err := library.Open(Selector{Description: "AR-DevPack-EVM-012 A"})
	if err != nil {
		t.Fatal(err)
	}
	if err := device.Close(); err == nil {
		t.Fatal("close failure was ignored")
	}
	if err := device.Close(); err != nil {
		t.Fatalf("second close = %v", err)
	}
	if nativeDevice.closeCalls != 1 {
		t.Fatalf("close calls = %d", nativeDevice.closeCalls)
	}
	if _, err := device.Read(nil); !errors.Is(err, ErrDeviceClosed) {
		t.Fatalf("zero-length read after close = %v", err)
	}
	if err := library.Close(); err != nil {
		t.Fatalf("library close after device close failure = %v", err)
	}
}

func TestNilDeviceMethodsReturnClosed(t *testing.T) {
	var device *Device
	if _, err := device.Read(nil); !errors.Is(err, ErrDeviceClosed) {
		t.Fatalf("Read() = %v", err)
	}
	if _, err := device.Write(nil); !errors.Is(err, ErrDeviceClosed) {
		t.Fatalf("Write() = %v", err)
	}
	if _, err := device.QueueStatus(); !errors.Is(err, ErrDeviceClosed) {
		t.Fatalf("QueueStatus() = %v", err)
	}
	if _, err := device.GetBitMode(); !errors.Is(err, ErrDeviceClosed) {
		t.Fatalf("GetBitMode() = %v", err)
	}
}

func TestDeviceReportsShortWriteAndStatus(t *testing.T) {
	native := &fakeNativeDevice{writeCount: 1}
	device := &Device{native: native, release: func() {}}
	if count, err := device.Write([]byte{1, 2}); count != 1 || !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short Write() = %d, %v", count, err)
	}
	native.writeStatus = StatusIOError
	if count, err := device.Write([]byte{1, 2}); count != 1 || err == nil || !strings.Contains(err.Error(), "I/O error") {
		t.Fatalf("failed Write() = %d, %v", count, err)
	}
}

func TestDeviceBitBangConfigurationReportsNativeStatus(t *testing.T) {
	native := &fakeNativeDevice{bitModeStatus: StatusIOError, baudStatus: StatusInvalidBaudRate}
	device := &Device{native: native, release: func() {}}
	if _, err := device.GetBitMode(); err == nil || !strings.Contains(err.Error(), "FT_GetBitMode") {
		t.Fatalf("GetBitMode() error = %v", err)
	}
	if err := device.SetBaudRate(115200); err == nil || !strings.Contains(err.Error(), "FT_SetBaudRate") {
		t.Fatalf("SetBaudRate() error = %v", err)
	}
}

type fakeNativeLibrary struct {
	closeCalls int
	openDevice nativeDevice
	openError  error
	selectors  []Selector
}

func (library *fakeNativeLibrary) open(selector Selector) (nativeDevice, error) {
	library.selectors = append(library.selectors, selector)
	return library.openDevice, library.openError
}

func (library *fakeNativeLibrary) close() error {
	library.closeCalls++
	return nil
}

type fakeNativeDevice struct {
	closeCalls       int
	closeStatus      Status
	readData         []byte
	readStatus       Status
	writeCount       uint32
	writeStatus      Status
	queued           uint32
	queueStatusValue Status
	timeouts         [2]uint32
	chars            [4]byte
	latency          byte
	bitMode          [2]byte
	bitModeValue     byte
	bitModeStatus    Status
	baud             uint32
	baudStatus       Status
	usb              [2]uint32
}

func (device *fakeNativeDevice) close() Status {
	device.closeCalls++
	return device.closeStatus
}

func (device *fakeNativeDevice) read(buffer []byte) (uint32, Status) {
	return uint32(copy(buffer, device.readData)), device.readStatus
}

func (device *fakeNativeDevice) write(buffer []byte) (uint32, Status) {
	if device.writeCount == 0 {
		return uint32(len(buffer)), device.writeStatus
	}
	return device.writeCount, device.writeStatus
}

func (device *fakeNativeDevice) queueStatus() (uint32, Status) {
	return device.queued, device.queueStatusValue
}

func (device *fakeNativeDevice) setTimeouts(read, write uint32) Status {
	device.timeouts = [2]uint32{read, write}
	return StatusOK
}

func (device *fakeNativeDevice) setChars(eventChar, eventEnabled, errorChar, errorEnabled byte) Status {
	device.chars = [4]byte{eventChar, eventEnabled, errorChar, errorEnabled}
	return StatusOK
}

func (device *fakeNativeDevice) setLatencyTimer(milliseconds byte) Status {
	device.latency = milliseconds
	return StatusOK
}

func (device *fakeNativeDevice) setBitMode(mask, mode byte) Status {
	device.bitMode = [2]byte{mask, mode}
	return StatusOK
}

func (device *fakeNativeDevice) getBitMode() (byte, Status) {
	return device.bitModeValue, device.bitModeStatus
}

func (device *fakeNativeDevice) setBaudRate(baud uint32) Status {
	device.baud = baud
	return device.baudStatus
}

func (device *fakeNativeDevice) setUSBParameters(inputSize, outputSize uint32) Status {
	device.usb = [2]uint32{inputSize, outputSize}
	return StatusOK
}
