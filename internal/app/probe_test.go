package app

import (
	"context"
	"errors"
	"io"
	"net"
	"reflect"
	"testing"

	"mmwcli/internal/camera"
	"mmwcli/internal/d2xx"
	"mmwcli/internal/dca"
	"mmwcli/internal/iwr6843"
)

func TestParseProbeOptionsKeepsCameraOptional(t *testing.T) {
	options, err := parseProbeOptions([]string{"--setup", "setup.json"}, io.Discard)
	if err != nil || options.setupPath != "setup.json" || options.camera != "" {
		t.Fatalf("probe options = %+v, %v", options, err)
	}
	options, err = parseProbeOptions(
		[]string{"--setup", "setup.json", "--camera", "camera-id"},
		io.Discard,
	)
	if err != nil || options.camera != "camera-id" {
		t.Fatalf("camera probe options = %+v, %v", options, err)
	}
	if _, err := parseProbeOptions(nil, io.Discard); err == nil {
		t.Fatal("probe accepted a missing setup")
	}
}

func TestProbeHardwareChecksConfiguredInterfacesAndOptionalCamera(t *testing.T) {
	setup := setupConfig{
		Radar: setupRadar{Port: "COM47", D2XX: "AR-DevPack-EVM-012"},
		DCA:   setupDCA{Host: "192.168.33.30", Device: "192.168.33.180"},
	}
	selectedCamera := &camera.Config{Device: "camera-id", Width: 1280, Height: 720, FPS: 30, MaxBytes: 2 << 20}
	var calls []string
	backend := probeBackend{
		d2xx: func(description string) error {
			calls = append(calls, "d2xx:"+description)
			return nil
		},
		radar: func(_ context.Context, port string, selectors iwr6843.Selectors) error {
			if selectors.SPI.Description != setup.Radar.D2XX+" A" ||
				selectors.IRQ.Description != setup.Radar.D2XX+" B" {
				t.Fatalf("radar selectors = %+v", selectors)
			}
			calls = append(calls, "radar:"+port)
			return nil
		},
		dca: func(_ context.Context, options dca.Options) error {
			if !options.ControlBindAddress.Equal(net.ParseIP(setup.DCA.Host)) ||
				!options.DeviceAddress.Equal(net.ParseIP(setup.DCA.Device)) {
				t.Fatalf("DCA addresses = bind %s, device %s", options.ControlBindAddress, options.DeviceAddress)
			}
			calls = append(calls, "dca")
			return nil
		},
		camera: func(ctx context.Context, config camera.Config) ([]byte, error) {
			if _, ok := ctx.Deadline(); !ok || config.Device != selectedCamera.Device {
				t.Fatalf("camera probe context/config = %v, %+v", ctx, config)
			}
			calls = append(calls, "camera:"+config.Device)
			return []byte{0xff, 0xd8, 0xff, 0xd9}, nil
		},
	}

	if err := probeHardware(context.Background(), setup, selectedCamera, backend); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"d2xx:AR-DevPack-EVM-012",
		"radar:COM47",
		"dca",
		"camera:camera-id",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("probe calls = %v, want %v", calls, want)
	}
}

func TestProbeHardwareStopsAtFailedComponent(t *testing.T) {
	want := errors.New("missing interface D")
	backend := probeBackend{
		d2xx: func(string) error { return want },
		radar: func(context.Context, string, iwr6843.Selectors) error {
			t.Fatal("radar probe called after D2XX failure")
			return nil
		},
		dca: func(context.Context, dca.Options) error { t.Fatal("DCA called after D2XX failure"); return nil },
		camera: func(context.Context, camera.Config) ([]byte, error) {
			t.Fatal("camera called after D2XX failure")
			return nil, nil
		},
	}
	err := probeHardware(context.Background(), setupConfig{}, nil, backend)
	if !errors.Is(err, want) {
		t.Fatalf("probe error = %v, want %v", err, want)
	}
}

func TestProbeD2XXInterfacesOpensAndClosesABCD(t *testing.T) {
	var opened []string
	var devices []*probeCloser
	libraryClosed := 0
	err := probeD2XXInterfaces(
		"AR-DevPack-EVM-012",
		func(selector d2xx.Selector) (io.Closer, error) {
			opened = append(opened, selector.Description)
			device := &probeCloser{}
			devices = append(devices, device)
			return device, nil
		},
		func() error { libraryClosed++; return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"AR-DevPack-EVM-012 A",
		"AR-DevPack-EVM-012 B",
		"AR-DevPack-EVM-012 C",
		"AR-DevPack-EVM-012 D",
	}
	if !reflect.DeepEqual(opened, want) {
		t.Fatalf("opened interfaces = %v, want %v", opened, want)
	}
	for index, device := range devices {
		if device.closeCalls != 1 {
			t.Fatalf("device %d close calls = %d", index, device.closeCalls)
		}
	}
	if libraryClosed != 1 {
		t.Fatalf("library close calls = %d", libraryClosed)
	}
}

type probeCloser struct{ closeCalls int }

func (closer *probeCloser) Close() error {
	closer.closeCalls++
	return nil
}
