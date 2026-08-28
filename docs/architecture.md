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
mmwcli check RADAR_CFG --rig RIG --frames N [--radar-only]
mmwcli capture RADAR_CFG TAKE --rig RIG --frames N [--radar-only]
mmwcli version
```

`check` parses the radar configuration and rig, derives exact frame geometry, validates firmware
and DCA settings, loads and closes D2XX, and checks the camera executable. It does not open radar or
DCA1000 hardware.

`capture` repeats preflight, boots the IWR6843, configures DCA1000, records exactly `N` whole radar
frames, and optionally records complete JPEGs from the rig's ffmpeg command. Raw ADC bytes are not
converted, repaired, or processed.

## Transaction boundary

All files are created below `TAKE.part`. Publication requires:

- exact finite ADC byte count;
- no missing or overlapping capture bytes;
- a complete required camera recording when enabled;
- closed files and a valid `mmwcli.take.v1` manifest.

Existing `TAKE` or `TAKE.part` paths are never replaced. Failure leaves no completed take.

mmwcore owns archive and DSP work. OpenMMW owns datasets, models, evaluation, inference, and
display. The completed take directory is the only handoff.
