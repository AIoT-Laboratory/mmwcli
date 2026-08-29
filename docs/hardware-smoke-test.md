# IWR6843 + DCA1000 smoke test

Run this only on the fixed Windows/amd64 research rig. Automated tests do not access hardware.

## Before capture

- Put the IWR6843 in SOP2 host-download mode and connect Enhanced COM.
- Connect DCA1000 to the dedicated `192.168.33.30/24` host interface; its default address is
  `192.168.33.180`.
- Install FTDI D2XX and build `mmwcli.exe` with `-tags ftd2xx`.
- Put exact Enhanced COM, D2XX description, BSS/MSS paths, DCA addresses, delay, camera settings,
  radar height, and `tilt_deg: 90` in the `mmwcli.rig.v3` `rig.json`.
- Do not run another DCA1000 or camera process concurrently.

First run offline validation:

```powershell
mmwcli check hardware\iwr6843.cfg --rig rig.json --frames 100
```

It must finish with `capture check passed (offline; no hardware accessed)`.

## Finite capture

```powershell
mmwcli capture hardware\iwr6843.cfg capture-001 --rig rig.json --frames 100
```

Pass criteria:

- exit code `0` and a final `capture complete` summary;
- `capture-001/session.json` reports `mmwcli.take.v2`, `radar_tilt_deg: 90`, and 100 frames;
- `adc.bin` size equals `frames × bytes_per_frame` reported by `check`;
- camera payload and index exist for a camera take;
- packet gaps and missing bytes are zero;
- no `capture-001.part` remains.

A non-empty ADC file or an unpublished `.part` directory is not a pass. On failure, retain the error
output and do not rename partial data.

## Continuous stream

Start the OpenMMW consumer, which invokes:

```powershell
mmwcli stream hardware\iwr6843.cfg --rig rig.json
```

The consumer must read one JSON header line and then exact `frame_bytes` blocks. Pass criteria are a
single radar start, more than 65535 complete frames without file growth, stable process memory, and
clean shutdown on `stop`, EOF, or Ctrl+C followed by an immediate successful restart. Any malformed
packet, overlap, unresolved gap, or closed consumer pipe must terminate the stream; losing the first
packet must emit no frame, and partial frames must never reach inference.
