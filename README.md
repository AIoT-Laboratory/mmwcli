# mmwcli

mmwcli captures finite IWR6843 + DCA1000 raw captures or streams complete ADC frames on
Windows/amd64. Finite capture writes raw ADC, an optional MJPEG camera recording, and one small
manifest.
DSP, datasets, training, inference, and visualization belong downstream.

```text
finite: IWR6843 + DCA1000 [+ camera] -> mmwcli.take.v3 -> mmwcore -> openmmw.take.v3
online: IWR6843 + DCA1000 -> complete ADC frames on stdout -> OpenMMW
```

## Build

Go 1.26 or newer and the FTDI D2XX Windows library are required.

```powershell
go test ./...
go vet ./...
go build -trimpath -o bin\mmwcli.exe .\cmd\mmwcli
```

Automated checks are offline. They do not open radar, DCA1000, serial, USB, or camera hardware.

## Hardware setup

Copy `hardware/setup.example.json` to the ignored `hardware/setup.json`, then set the Enhanced COM
port, measured mount height, physical scene ROI, and camera format once. The tracked example already
points to this workstation's installed mmWave Studio 2.1.1.0 xWR68xx BSS/MSS files. Relative
firmware paths, when used, resolve beside the setup file.

`pitch_deg` is the downward boresight angle from horizontal: `0` is horizontal, `30` is the tilted
mount, and `90` is vertical down. A missing value defaults to `0`.
Inspect or update the mount with:

```powershell
mmwcli setup show hardware\setup.json
mmwcli setup mount hardware\setup.json --height 1.5 --pitch 0
mmwcli setup roi hardware\setup.json `
  --min-forward 0.5 --max-forward 5.5 `
  --min-lateral -4.8 --max-lateral 4.8 `
  --min-up 0 --max-up 2.2
```

ROI coordinates are `[forward, lateral, up]` metres in the level frame. The ROI is editable
collection metadata for downstream DSP and visualization; mmwcli never crops or changes raw ADC
bytes with it. The tracked default uses 0.1 m precision and bounds the current 5.5 m usable RD
radius with the IWR6843ISK's +/-60 degree horizontal field of view: forward `0.5..5.5 m` and
lateral `-4.8..4.8 m`. This is an axis-aligned envelope; its far corners are not claimed to be
physically reachable. A setup created before ROI was added remains valid and simply has no ROI
metadata.

List DirectShow cameras without loading radar hardware configuration, then preview any returned
device. The JSON result contains each friendly `name` plus its unambiguous FFmpeg `id`:

```powershell
mmwcli camera list
mmwcli camera preview --setup hardware\setup.json --camera "@device_pnp_..." > preview.jpg
```

`preview` has a fixed three-second timeout and writes exactly one JPEG to stdout. Use the returned
`id`, not a device number, in `--camera`. Preview and camera capture require the explicit `camera`
format in setup; there are no hidden format defaults. `camera list` remains available without setup.

Before a collection run, probe the IWR6843, D2XX A/B/C/D interfaces, and DCA1000 SystemAlive reply.
Add the selected camera to require one decodable frame as well:

```powershell
mmwcli probe --setup hardware\setup.json --camera "@device_pnp_..."
```

`probe` holds the same hardware lock as capture, performs the same SOP2 reset, and verifies the
Enhanced COM IWR6843 ES2 identity without submitting firmware.

## Capture

Check every input without opening capture hardware, then run the same finite plan:

```powershell
mmwcli check hardware\iwr6843.cfg --setup hardware\setup.json --frames 600 --camera "@device_pnp_..."
mmwcli capture hardware\iwr6843.cfg take-001.capture --setup hardware\setup.json --frames 600 --camera "@device_pnp_..." --control-stdin
```

Use `--radar-only` on both commands to omit the camera; it cannot be combined with `--camera`.
`--frames` must be in `1..65535`. With `--control-stdin`, exact `stop\n` or stdin EOF enters the
normal cancellation and hardware-cleanup path; other input is ignored. Without that flag, finite
capture does not consume stdin.
A camera configured for a normal take is required to complete successfully; otherwise the take is
not published.

## Stream

Run one unbounded radar-only session for online inference:

```powershell
mmwcli stream hardware\iwr6843.cfg --setup hardware\setup.json
```

The effective in-memory CFG always uses `frameCfg numFrames=0`; the source file is unchanged. stdout
starts with one JSON line containing `frame_bytes`, `period_ns`, `mount`, and `roi` when configured.
Every remaining byte belongs to fixed-size complete ADC frames.
Logs go to stderr. `stream` never opens a camera or writes a take. Ctrl+C stops the radar and
DCA1000 and discards any incomplete final frame.
An exact `stop` line or stdin EOF performs the same cleanup; other stdin lines are ignored.
Malformed, overlapping, or persistently incomplete DCA data terminates the stream instead of being
repaired. The first packet must be DCA sequence 1 at byte offset 0, so losing the stream origin
cannot silently shift every frame.

After the validated radar frame-start event, finite capture and stream write the exact line
`MMWCLI_EVENT {"event":"radar_started"}` to stderr. It never enters stream stdout.

After the validated radar frame-start event, finite capture and stream write the exact line
`MMWCLI_EVENT {"event":"radar_started"}` to stderr. It never enters stream stdout.

## Finite output

```text
take-001.capture/
  session.json
  setup.json
  adc.bin
  radar.cfg
  camera.mjpeg      # absent with --radar-only
  camera.index.bin  # absent with --radar-only
```

`setup.json` is the immutable normalized `mmwcli.snapshot.v1` used for the capture, including its
mount and optional physical ROI. `session.json` uses `mmwcli.take.v3` and references its bytes and
SHA-256. Radar time is a bounded frame-start
observation. Camera time is complete-JPEG delivery time, not exposure time. Files are staged under
`take-001.capture.part` and published as `take-001.capture` only after the requested radar frames
and required camera recording complete. mmwcore converts this raw capture to the
`openmmw.take.v3` verified take used by OpenMMW datasets and finite inference.

See [architecture](docs/architecture.md), [timing and layout](docs/timing.md), and the canonical
[Web hardware smoke test](docs/hardware-smoke-test.md).
