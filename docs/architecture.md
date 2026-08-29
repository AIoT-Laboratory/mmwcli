# Architecture

mmwcli owns IWR6843/DCA1000 acquisition: it publishes finite takes or emits whole live ADC frames.

```text
radar.cfg + rig.json + frame count
  -> offline preflight
  -> IWR6843 SOP2 firmware boot and mmWaveLink configuration
  -> DCA1000 raw ADC receive
  -> optional ffmpeg MJPEG receive
  -> validate exact files
  -> rename TAKE.part to TAKE
```

The public surface is intentionally small:

```text
mmwcli check RADAR_CFG --rig RIG --frames N [--camera DEVICE] [--radar-only]
mmwcli capture RADAR_CFG TAKE --rig RIG --frames N [--camera DEVICE] [--radar-only]
mmwcli stream RADAR_CFG --rig RIG
mmwcli camera list --rig RIG
mmwcli camera preview --rig RIG [--camera ID]
mmwcli version
```

`check` parses the radar configuration and rig, derives exact frame geometry, validates firmware
and DCA settings, loads and closes D2XX, and checks the fixed FFmpeg camera executable. It does not
open radar or DCA1000 hardware.

`capture` repeats preflight, boots the IWR6843, configures DCA1000, records exactly `N` whole radar
frames, and optionally records complete JPEGs from the rig's structured DirectShow configuration.
Raw ADC bytes are not converted, repaired, or processed.

`stream` applies the same hardware preflight with `frameCfg numFrames=0`, starts the radar once, and
emits one compact JSON geometry line followed by fixed-size complete ADC frames on stdout. It has no
camera, take directory, recording option, job layer, or network server. stderr remains human-readable.
OpenMMW must drain the pipe independently of model inference. The first DCA packet is anchored at
sequence 1 and byte offset 0; an unanchored stream fails before emitting ADC bytes. Ctrl+C, an
exact `stop` line on stdin, or stdin EOF enters the same hardware cleanup path.

## Camera identity

`rig.camera` is a structured object: `device`, `width`, `height`, `fps`, and `max_bytes`. It never
contains a shell command. mmwcli is the sole owner of the FFmpeg argv that opens `video=<device>`
through DirectShow and emits JPEGs on stdout. `--camera` replaces only the selected device and is
rejected with `--radar-only`.

`camera list` works with or without a configured camera and prints its default (`null` when absent)
plus `{name,id}` video devices; `id` prefers FFmpeg's unambiguous alternative name. `camera preview`
opens that same device and writes one complete JPEG before releasing it. Web passes the selected
`id` to both preview and capture.

## Transaction boundary

All files are created below `TAKE.part`. Publication requires:

- exact finite ADC byte count;
- no missing or overlapping capture bytes;
- a complete required camera recording when enabled;
- closed files and a valid `mmwcli.take.v2` manifest.

Existing `TAKE` or `TAKE.part` paths are never replaced. Failure leaves no completed take.

mmwcore owns archive and DSP work. OpenMMW owns datasets, models, evaluation, inference, and
display. A completed take directory is the finite handoff; the stream stdout contract is the online
handoff and is never represented as `TAKE.part`.
