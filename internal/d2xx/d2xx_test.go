package d2xx

import (
	"errors"
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

type fakeNativeLibrary struct {
	infoValue  nativeInfo
	infoError  error
	closeCalls int
}

func (library *fakeNativeLibrary) info() (nativeInfo, error) {
	return library.infoValue, library.infoError
}

func (library *fakeNativeLibrary) close() error {
	library.closeCalls++
	return nil
}
