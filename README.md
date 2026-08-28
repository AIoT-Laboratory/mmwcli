# mmwcli

mmwcli captures finite IWR6843 + DCA1000 takes for OpenMMW on Windows/amd64.
It writes raw ADC, an optional MJPEG camera recording, and one small manifest.
DSP, datasets, training, inference, and visualization belong downstream.

```text
IWR6843 + DCA1000 [+ camera] -> mmwcli.take.v1 -> mmwcore -> OpenMMW
```

## Build

Go 1.26 or newer and the FTDI D2XX Windows library are required.

```powershell
go test ./...
go vet ./...
go build -trimpath -tags ftd2xx -o bin\mmwcli.exe .\cmd\mmwcli
```

Automated checks are offline. They do not open radar, DCA1000, serial, USB, or camera hardware.

## Rig

Keep workstation-specific paths and device selections in one JSON file:

```json
{
  "schema": "mmwcli.rig.v1",
  "port": "COM3",
  "bss": "firmware/xwr68xx_radarss.bin",
  "mss": "firmware/xwr68xx_masterss.bin",
  "d2xx": "AR-DevPack-EVM-012",
  "dca": {
    "host": "192.168.33.30",
    "device": "192.168.33.180",
    "delay_us": 50
  },
  "camera": {
    "command": [
      "ffmpeg", "-hide_banner", "-loglevel", "error", "-nostdin",
      "-f", "dshow", "-i", "video=CAMERA NAME",
      "-an", "-c:v", "mjpeg", "-f", "image2pipe", "pipe:1"
    ],
    "max_bytes": 2097152
  },
  "height_m": 1.5
}
```

Relative firmware paths resolve beside the rig file. Device discovery is never implicit.

## Capture

Check every input without opening capture hardware, then run the same finite plan:

```powershell
mmwcli check hardware\iwr6843.cfg --rig rig.json --frames 600
mmwcli capture hardware\iwr6843.cfg TAKE --rig rig.json --frames 600
```

Use `--radar-only` on both commands to omit the camera. `--frames` must be in `1..65535`.
A camera configured for a normal take is required to complete successfully; otherwise the take is
not published.

## Output

```text
TAKE/
  session.json
  adc.bin
  radar.cfg
  camera.mjpeg      # absent with --radar-only
  camera.index.bin  # absent with --radar-only
```

`session.json` uses `mmwcli.take.v1`. Radar time is a bounded frame-start observation. Camera time
is complete-JPEG delivery time, not exposure time. Files are staged under `TAKE.part` and published
only after the requested radar frames and required camera recording complete.

See [architecture](docs/architecture.md), [timing and layout](docs/timing.md), and the
[hardware smoke test](docs/hardware-smoke-test.md).
