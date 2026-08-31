# Radar and camera timing

A finite raw capture contains one radar and, unless `--radar-only` is used, one camera.

## Layout

```text
TAKE.capture/
  session.json
  setup.json
  adc.bin
  radar.cfg
  camera.mjpeg      # camera take only
  camera.index.bin  # camera take only
```

`session.json` uses `mmwcli.take.v3` and records:

- session ID, finite frame count, and frame period;
- the bounded host-relative radar start observation;
- a bytes/SHA-256 reference to the immutable `mmwcli.snapshot.v1` hardware and mount snapshot;
- IWR6843 data/config identities and selected camera results;
- camera frame count, payload/index identity, and timing semantics when present.

`setup.json` records boresight `pitch_deg` and, when configured, level-frame ROI bounds in
`[forward, lateral, up]` metres. Pitch `90` is the primary downward-looking installation; pitch `0`
remains the horizontal control. Stream headers preserve the mount and ROI. ROI is metadata for
downstream processing and does not crop raw acquisition.

## Time meaning

The take clock starts when recording begins and runs at 1 GHz without wrapping.
`radar_start.lower_ns` and `radar_start.upper_ns` bound the observed first radar-frame start.
Subsequent frame times use the configured frame period.

Each `camera.index.bin` entry points to one complete JPEG in `camera.mjpeg` and records when that
JPEG was fully delivered to mmwcli. `delivery_observed` is not exposure time and makes no
hardware-sync claim. OpenMMW owns the downstream offline pairing policy.

## Failure boundary

The camera is required unless capture uses `--radar-only`. A malformed JPEG, camera-process failure,
partial radar frame, DCA gap, file error, or manifest error prevents publication of `TAKE.capture`.
