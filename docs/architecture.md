# Architecture

mmwcli has one job: publish a finite IWR6843 take that mmwcore and OpenMMW can open directly.

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
- closed files and a valid `mmwcli.take.v1` manifest.

Existing `TAKE` or `TAKE.part` paths are never replaced. Failure leaves no completed take.

mmwcore owns archive and DSP work. OpenMMW owns datasets, models, evaluation, inference, and
display. The completed take directory is the only handoff.
