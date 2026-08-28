# mmwcli contributor guide

## Scope

mmwcli acquires raw mmWave data and publishes completed radar or radar-plus-camera takes. Keep DSP,
datasets, inference, and visualization in mmwcore or OpenMMW.

The active research path is:

```text
mmwcli capture -> flat take -> mmwcore -> OpenMMW
```

The only hardware route is IWR6843 ES2 + DCA1000 on Windows/amd64.

## Code map

- `cmd/mmwcli`: executable
- `internal/app`: command parsing and orchestration
- `internal/iwr6843`, `internal/d2xx`: IWR6843 firmware and mmWaveLink control
- `internal/dca`, `internal/session`, `internal/capturefile`: raw ADC capture and publication
- `internal/camera`, `internal/take`: optional camera recording and flat take publication
- `internal/radar`: CFG parsing and frame geometry

## Rules

- Validate capture inputs before opening hardware or creating output.
- Never auto-select a COM or D2XX device.
- Preserve ADC bytes exactly; do not process or repair them.
- Publish only complete whole-frame takes through the existing `.part` transaction.
- Keep public capture stdout human-readable. Completed directories are the data handoff.
- Keep changes narrow and update the closest tests and documentation.
- Automated validation must not access radar, DCA1000, serial, USB, camera, or network hardware.

## Validation

Run the narrowest test first, then the full offline gates for capture or lifecycle changes:

```text
go test ./...
go vet ./...
go build -trimpath ./cmd/mmwcli
```

The `ftd2xx` build and physical capture require the user-installed FTDI library and explicit
hardware authorization. Report offline tests separately from hardware validation.

Do not commit, push, switch branches, tag, or publish without explicit user authorization.
