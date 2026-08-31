package app

import (
	"path/filepath"
	"testing"

	"mmwcli/internal/iwr6843"
)

func TestSetupSnapshotKeepsFirmwareIdentityWithoutSourcePaths(t *testing.T) {
	roi := setupROI{
		Frame: levelROIFrame,
		MinM:  [3]float64{0.5, -1.5, 0},
		MaxM:  [3]float64{5.5, 1.5, 2.2},
	}
	setup := setupConfig{
		Radar: setupRadar{Port: "COM3", D2XX: "AR-DevPack-EVM-012"},
		DCA: setupDCA{
			Host: "192.168.33.30", Device: "192.168.33.180", DelayUS: 50,
		},
		Mount: setupMount{HeightM: 1.5, PitchDeg: 90},
		ROI:   &roi,
	}
	assets := iwr6843.Assets{
		BSS: iwr6843.File{
			Path: filepath.Join(`C:\private\firmware`, "renamed-bss.bin"), Size: 10,
			SHA256: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		},
		MSS: iwr6843.File{
			Path: filepath.Join(`C:\private\firmware`, "renamed-mss.bin"), Size: 20,
			SHA256: "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
		},
	}
	snapshot := setupSnapshot(setup, assets, nil)
	if snapshot.Mount.PitchDeg != 90 {
		t.Fatalf("snapshot mount = %+v", snapshot.Mount)
	}
	if snapshot.ROI == nil || snapshot.ROI.Frame != levelROIFrame || snapshot.ROI.MaxM[0] != 5.5 {
		t.Fatalf("snapshot ROI = %+v", snapshot.ROI)
	}
	if snapshot.Radar.BSS.Name != "renamed-bss.bin" || snapshot.Radar.MSS.Name != "renamed-mss.bin" ||
		snapshot.Radar.BSS.SHA256[0] != 'a' || snapshot.Radar.MSS.SHA256[0] != 'b' {
		t.Fatalf("firmware snapshot = %+v", snapshot.Radar)
	}
}
