# Architecture

mmwcli owns IWR6843/DCA1000 acquisition: it publishes finite raw captures or emits complete ADC
frames.

```text
radar.cfg + setup.json + frame count
  -> offline preflight
  -> IWR6843 SOP2 firmware boot and mmWaveLink configuration
  -> DCA1000 raw ADC receive
  -> optional ffmpeg MJPEG receive
  -> validate exact files
  -> rename TAKE.capture.part to TAKE.capture
```

The public surface is intentionally small:

```text
mmwcli setup show SETUP
mmwcli setup mount SETUP --height M --pitch 90
mmwcli probe --setup SETUP [--camera DEVICE]
mmwcli check RADAR_CFG --setup SETUP --frames N [--camera DEVICE | --radar-only]
mmwcli capture RADAR_CFG TAKE.capture --setup SETUP --frames N [--camera DEVICE | --radar-only] [--control-stdin]
mmwcli stream RADAR_CFG --setup SETUP
mmwcli camera list
mmwcli camera preview --setup SETUP --camera ID
mmwcli version
```

Pitch `90` is the primary downward-looking installation; pitch `0` remains the horizontal control.

`check` parses the radar configuration and setup, derives exact frame geometry, validates firmware
and DCA settings, loads and closes D2XX, and checks the fixed FFmpeg camera executable. It does not
open radar or DCA1000 hardware.

`probe` opens the configured D2XX interfaces, performs the capture path's SOP2 reset, gates the
Enhanced COM IWR6843 ES2 identity, and requires a DCA1000 SystemAlive reply through the configured
host link. Optional `--camera` reuses the one-frame preview. It does not submit firmware.

`capture` repeats preflight, boots the IWR6843, configures DCA1000, records exactly `N` complete
radar frames, and optionally records complete JPEGs using the setup format and selected DirectShow
configuration. Raw ADC bytes are not converted, repaired, or processed.

`stream` applies the same hardware preflight with `frameCfg numFrames=0`, starts the radar once, and
emits one compact JSON geometry line followed by fixed-size complete ADC frames on stdout. It has
no camera, take directory, recording option, job layer, or network server. Logs remain on stderr;
the stable `MMWCLI_EVENT {"event":"radar_started"}` stderr line marks the validated RF start event.
OpenMMW must drain the pipe independently of model inference. The first DCA packet is anchored at
sequence 1 and byte offset 0; an unanchored stream fails before emitting ADC bytes. Ctrl+C, an
exact `stop` line on stdin, or stdin EOF enters the same hardware cleanup path. Finite capture reads
the same control only when `--control-stdin` is explicit.

## Camera identity

`setup.camera` contains only `width`, `height`, `fps`, and `max_bytes`; the selected device remains
an explicit CLI input. It never contains a shell command. mmwcli owns the FFmpeg argv that opens
`video=<device>` through DirectShow and emits JPEGs on stdout. `--camera` is rejected with
`--radar-only`.

`camera list` is setup-independent and prints `{name,id}` video devices; `id` prefers FFmpeg's
unambiguous alternative name. `camera preview` requires setup camera format, opens the selected
device, and writes one complete JPEG before releasing it. Web passes the same `id` to preview and
capture.

## Transaction boundary

All files are created below `TAKE.capture.part`. Publication requires:

- exact finite ADC byte count;
- no missing or overlapping capture bytes;
- a complete required camera recording when enabled;
- closed files, an immutable `mmwcli.snapshot.v1`, and a valid `mmwcli.take.v3` manifest.

Existing `TAKE.capture` or `TAKE.capture.part` paths are never replaced. Failure retains the staged
capture for diagnosis.

mmwcore converts the `mmwcli.take.v3` raw capture into the `openmmw.take.v3` verified take and owns
DSP. OpenMMW owns datasets, models, evaluation, inference, and display. The completed raw-capture
directory is the finite handoff; stream stdout is the online handoff and is never represented as a
transaction directory.
