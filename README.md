# mmwcli

mmwcli captures finite IWR6843 + DCA1000 takes or streams whole ADC frames on Windows/amd64.
Finite capture writes raw ADC, an optional MJPEG camera recording, and one small manifest.
DSP, datasets, training, inference, and visualization belong downstream.

```text
finite: IWR6843 + DCA1000 [+ camera] -> mmwcli.take.v2 -> mmwcore -> OpenMMW
stream: IWR6843 + DCA1000 -> whole ADC frames on stdout -> OpenMMW
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
  "schema": "mmwcli.rig.v3",
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
    "device": "@device_pnp_...",
    "width": 1280,
    "height": 720,
    "fps": 30,
    "max_bytes": 2097152
  },
  "height_m": 1.5,
  "tilt_deg": 90
}
```

Relative firmware paths resolve beside the rig file. The camera is always a DirectShow video
device produced by the one built-in FFmpeg MJPEG command; rig files cannot execute arbitrary
camera commands. `tilt_deg` is the radar PCB plane's angle above the horizontal floor: `90` means
the PCB is vertical and its boresight is horizontal. This release accepts only `90`.

List DirectShow cameras before choosing one. This also works when the rig has no camera. The JSON
result has the rig default (`null` when absent) and each device's friendly `name` plus its
unambiguous FFmpeg `id` (the alternative name when available):

```powershell
mmwcli camera list --rig rig.json
mmwcli camera preview --rig rig.json --camera "@device_pnp_..." > preview.jpg
```

`preview` has a fixed three-second timeout and writes exactly one JPEG to stdout. Use the returned
`id`, not a device number, in `--camera`; the same effective FFmpeg/DirectShow configuration is
used by preview, `check`, and capture.

## Capture

Check every input without opening capture hardware, then run the same finite plan:

```powershell
mmwcli check hardware\iwr6843.cfg --rig rig.json --frames 600 --camera "@device_pnp_..."
mmwcli capture hardware\iwr6843.cfg TAKE --rig rig.json --frames 600 --camera "@device_pnp_..."
```

Use `--radar-only` on both commands to omit the camera; it cannot be combined with `--camera`.
`--frames` must be in `1..65535`.
A camera configured for a normal take is required to complete successfully; otherwise the take is
not published.

## Stream

Run one unbounded radar-only session for online inference:

```powershell
mmwcli stream hardware\iwr6843.cfg --rig rig.json
```

The effective in-memory CFG always uses `frameCfg numFrames=0`; the source file is unchanged. stdout
starts with one JSON line containing `frame_bytes`, `period_ns`, `height_m`, and `tilt_deg`. Every
remaining byte belongs to fixed-size complete ADC frames. Logs go to stderr. `stream` never opens a
camera or writes a take. Ctrl+C stops the radar and DCA1000 and discards any incomplete final frame.
An exact `stop` line or stdin EOF performs the same cleanup; other stdin lines are ignored.
Malformed, overlapping, or persistently incomplete DCA data terminates the stream instead of being
repaired. The first packet must be DCA sequence 1 at byte offset 0, so losing the stream origin
cannot silently shift every frame.

## Output

```text
TAKE/
  session.json
  adc.bin
  radar.cfg
  camera.mjpeg      # absent with --radar-only
  camera.index.bin  # absent with --radar-only
```

`session.json` uses `mmwcli.take.v2` and records the required 90-degree radar tilt. Radar time is a
bounded frame-start observation. Camera time is complete-JPEG delivery time, not exposure time.
Files are staged under `TAKE.part` and published
only after the requested radar frames and required camera recording complete.

See [architecture](docs/architecture.md), [timing and layout](docs/timing.md), and the
[hardware smoke test](docs/hardware-smoke-test.md).
