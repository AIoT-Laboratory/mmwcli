# Radar and camera timing

A take contains one radar and, unless `--radar-only` is used, one camera.

## Layout

```text
TAKE/
  session.json
  adc.bin
  radar.cfg
  camera.mjpeg      # camera take only
  camera.index.bin  # camera take only
```

`session.json` uses `mmwcli.take.v2` and records:

- session ID, finite frame count, and frame period;
- the bounded host-relative radar start observation;
- radar height, required 90-degree tilt, IWR6843 identity, paths, sizes, and SHA-256 digests;
- camera frame count, payload/index identity, and timing semantics when present.

`radar_tilt_deg` is the radar PCB plane's angle above the horizontal floor. Its required value
`90` means a vertical PCB and horizontal boresight; the same definition is used by stream headers.

## Time meaning

The take clock starts when recording begins and runs at 1 GHz without wrapping.
`radar_start.lower_ns` and `radar_start.upper_ns` bound the observed first radar-frame start.
Subsequent frame times use the configured frame period.

Each `camera.index.bin` entry points to one complete JPEG in `camera.mjpeg` and records when that JPEG was
fully delivered to mmwcli. `delivery_observed` is not exposure time and makes no hardware-sync claim.
OpenMMW owns the downstream offline pairing policy.

## Failure boundary

The camera is required unless capture uses `--radar-only`. A malformed JPEG, camera-process failure,
partial radar frame, DCA gap, file error, or manifest error prevents publication of `TAKE`.
