# Web hardware smoke test

This is the only end-to-end hardware acceptance for the local OpenMMW workstation. Unit tests and
mocked Playwright runs do not satisfy it.

## Prepare the workstation

1. Put the IWR6843 in SOP2 host-download mode, connect Enhanced COM and DCA1000, and use the
   dedicated `192.168.33.30/24` host interface.
2. Build `mmwcli/bin/mmwcli.exe` with the standard README command.
3. Copy `mmwcli/hardware/setup.example.json` to the ignored
   `mmwcli/hardware/setup.json`. The example already points to the installed xWR68xx BSS/MSS files;
   enter the real COM port, D2XX description, DCA addresses, measured height, downward pitch, and
   camera format. Choose `0`, `30`, or `90`; `0` is horizontal and `90` is vertical down.
4. Confirm that no other radar, DCA1000, camera, or OpenMMW process is running.

From `mmwcli`, validate the request without opening hardware:

```powershell
bin\mmwcli.exe check hardware\iwr6843.cfg --setup hardware\setup.json --frames 30 --radar-only
bin\mmwcli.exe probe --setup hardware\setup.json
```

Then start the actual entry point from `openmmw` and open `http://127.0.0.1:5173/capture`:

```powershell
bun run --cwd=web dev
```

The Setup card must show the measured height and pitch. Refresh cameras, explicitly select the
device under test (choose a non-first device when more than one is present), and require a real JPEG
Preview before capture.

## Three hardware gates

Use `No inference` so hardware evidence does not depend on a checkpoint.

1. **Camera:** Subject `smoke`, Scene `hardware`, Action `camera`, Take `take-001`, 30 frames,
   selected camera. Capture must end at
   `dataset/takes/smoke/hardware/camera/take-001/`.
2. **Radar only:** Action `radar`, Take `take-001`, 30 frames, no camera. Capture must end at
   `dataset/takes/smoke/hardware/radar/take-001/`.
3. **Cooperative Stop:** Action `stop`, Take `take-001`, camera selected, enough frames to press
   **Stop** while status is Capturing. Status must become Cancelled and preserve exactly
   `dataset/takes/smoke/hardware/stop/take-001.capture.part/`; neither a verified take nor a
   `.capture` directory may appear.

For each completed take, run the strict reader from the OpenMMW environment:

```powershell
.venv\Scripts\python.exe -c "from mmwcore.io import open_take; t=open_take(r'dataset/takes/smoke/hardware/camera/take-001'); t.archive.verify_all(); print(t.frame_count,t.height_m,t.pitch_deg,t.camera is not None); t.camera.read(0); t.camera.read(len(t.camera.frames)-1)"
.venv\Scripts\python.exe -c "from mmwcore.io import open_take; t=open_take(r'dataset/takes/smoke/hardware/radar/take-001'); t.archive.verify_all(); print(t.frame_count,t.height_m,t.pitch_deg,t.camera is None)"
```

Both must print 30 frames, the measured height and pitch, and `True`. The strict reader checks the
session/setup references, file sizes and hashes, radar CFG/archive agreement, and camera payload and
index. Successful conversion must leave only the verified directory; `.capture` and `.capture.part`
must be absent.

## Report five independent results

Record each as Pass, Fail, or Blocked with its command/path evidence:

- **Software:** three repository test gates pass.
- **UI:** mocked desktop/mobile Playwright flows pass; this proves UI behavior only.
- **Hardware:** Preview plus all three gates above pass on real devices.
- **Model:** a maintained checkpoint infers the new camera take and Offline opens the strictly
  validated red pose (green target is optional when labels are generated).
- **Online:** the same real setup, CFG, and maintained checkpoint produce increasing prediction
  counters; Stop returns Idle without a cleanup warning and an immediate restart succeeds.

No checkpoint means Model and Online are Blocked, not failed and not passed. A short Online smoke
does not prove long-duration stability; record a separate soak duration and peak memory before
making that claim.
