package d2xx

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestVersionStringUsesD2XXBCDFields(t *testing.T) {
	tests := []struct {
		version Version
		want    string
	}{
		{version: 0x00021228, want: "2.12.28"},
		{version: 0x00010434, want: "1.4.34"},
		{version: 0x00030A15, want: "0x00030A15"},
		{version: 0x01030115, want: "0x01030115"},
	}
	for _, test := range tests {
		if got := test.version.String(); got != test.want {
			t.Fatalf("Version(0x%08X).String() = %q, want %q", test.version, got, test.want)
		}
	}
}

func TestStatusErrorIncludesOperationAndStatus(t *testing.T) {
	err := &StatusError{Operation: "FT_GetLibraryVersion", Status: StatusNotSupported}
	if got := err.Error(); !strings.Contains(got, "FT_GetLibraryVersion") || !strings.Contains(got, "not supported") {
		t.Fatalf("StatusError.Error() = %q", got)
	}
	if got := Status(99).String(); got != "unknown status 99" {
		t.Fatalf("Status(99).String() = %q", got)
	}
}

func TestFinishLoadRetainsInfoAndClosesOnce(t *testing.T) {
	native := &fakeNativeLibrary{infoValue: nativeInfo{
		library:      "fake-d2xx",
		version:      0x00021228,
		versionKnown: true,
	}}
	library, err := finishLoad(native)
	if err != nil {
		t.Fatal(err)
	}
	if got := library.Info(); got.Library != "fake-d2xx" || !got.VersionKnown || got.Version.String() != "2.12.28" {
		t.Fatalf("Info() = %+v", got)
	}
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

func TestFinishLoadClosesAfterProbeFailure(t *testing.T) {
	native := &fakeNativeLibrary{infoError: errors.New("probe failed")}
	_, err := finishLoad(native)
	if err == nil || err.Error() != "probe failed" {
		t.Fatalf("finishLoad() error = %v", err)
	}
	if native.closeCalls != 1 {
		t.Fatalf("close calls = %d, want 1", native.closeCalls)
	}
}

func TestLibraryRequiresExplicitSelectorAndTracksDevice(t *testing.T) {
	nativeDevice := &fakeNativeDevice{}
	native := &fakeNativeLibrary{
		infoValue:  nativeInfo{library: "fake-d2xx"},
		openDevice: nativeDevice,
	}
	library, err := finishLoad(native)
	if err != nil {
		t.Fatal(err)
	}
	selector := Selector{By: SelectBySerialNumber, Value: "FTAK3Z11A"}
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
	native := &fakeNativeLibrary{infoValue: nativeInfo{library: "fake-d2xx"}}
	library, err := finishLoad(native)
	if err != nil {
		t.Fatal(err)
	}
	defer library.Close()
	for _, selector := range []Selector{
		{},
		{By: SelectBySerialNumber},
		{By: SelectBySerialNumber, Value: "A\x00B"},
		{By: Selection(4), Value: "location"},
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
		infoValue:  nativeInfo{library: "fake-d2xx"},
		openDevice: nativeDevice,
	}
	library, err := finishLoad(nativeLibrary)
	if err != nil {
		t.Fatal(err)
	}
	device, err := library.Open(Selector{By: SelectBySerialNumber, Value: "A"})
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
	infoValue  nativeInfo
	infoError  error
	closeCalls int
	openDevice nativeDevice
	openError  error
	selectors  []Selector
}

func (library *fakeNativeLibrary) open(selector Selector) (nativeDevice, error) {
	library.selectors = append(library.selectors, selector)
	return library.openDevice, library.openError
}

func (library *fakeNativeLibrary) info() (nativeInfo, error) {
	return library.infoValue, library.infoError
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
