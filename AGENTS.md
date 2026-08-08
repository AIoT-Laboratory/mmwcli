# mmwcli Agent Guide

This file governs automated development in this repository. User-facing behavior and usage are
defined by `README.md` and `docs/`.

## Project boundaries

- `mmwcli` is a cross-platform command-line controller and DCA1000 raw-ADC acquisition tool for
  explicitly documented TI routes. Device-family support is tiered and must not be inferred from a
  shared source tree, RF band, lane count, or similar part number.
- The baseline is IWR6843 ES2 part `0xE2`, legacy frame, 16-bit complex ADC, and two LVDS lanes.
- The functional/application workflow supports only the dedicated xWR68xx/IWR6843 TI `studio_cli`
  device firmware and currently has source-backed offline validation only. It requires the user to
  flash `mmwave_Studio_cli_xwr68xx.bin`; a complete Radar Toolbox installation is unnecessary.
- `debug-capture` hardware validation covers only IWR6843 ES2 part `0xE2` with the exact firmware,
  host, FTDI, and DCA1000 combination recorded in the hardware smoke test. Other devices may enter
  a source-validated experimental tier before hardware is available only under the requirements
  below; they must not be described as supported.
- The top-level `repl` is a `studio_cli` utility fixed to its line protocol and xWR68xx `version`
  validation. It is not a separate firmware or capture backend.
  It must not provide a selectable custom dialect or a way to bypass validation. It may send
  single-line extension commands from compatible firmware, but that does not make the firmware a
  supported configuration or capture backend. A response without an explicit `Done` or numeric
  `Error <code>` must terminate the session immediately.
- The public command for SOP2 host download and direct control is always `debug-capture`; do not add
  architecture or device-family aliases. This workflow may load user-supplied MSS/BSS firmware, but
  it must remain layered separately from the text CLI workflows and must not depend on the mmWave
  Studio host runtime.
- The TI reference workflow uses Enhanced COM only for firmware-memory writes and then switches to
  FTDI MPSSE SPI/IRQ. Do not model `debug-capture` as a UART-only workflow. Its host USB transport is
  fixed to FTDI D2XX; do not implement custom raw USB or bind/copy mmWave Studio DLLs or FTDILib.
- Host code uses Go 1.26+ and the standard library. Default builds use `CGO_ENABLED=0` and support
  Windows/Linux on amd64 and arm64. The D2XX backend is enabled only by the `ftd2xx` build tag:
  Windows remains pure Go and loads the system-installed DLL; Linux is the only permitted CGo
  exception, includes the user-installed official `ftd2xx.h`, and links `libftd2xx.so`. Native
  backend architecture support depends on a matching FTDI library and must be validated separately.
- Do not add a GUI, MATLAB, a processing pipeline, C#/.NET, the mmWave Studio runtime, TI's DCA CLI,
  a Lua host/interpreter, or third-party Go modules. CGo is forbidden except for the Linux D2XX
  adapter described above.
- The repository and release archives must not distribute FTDI headers, libraries, drivers, or
  installers. Bind only the minimum D2XX ABI needed for current operations; do not port TI's complete
  FTDILib.
- Users supply TI firmware, configurations, and tools from their own installations. Do not copy
  these assets into the repository or release archives.
- `sensorStop` stops the sensor only; it does not power off the radar or capture card.

## Hardware support tiers

- **Supported** means a public fail-closed implementation, relevant automated tests, and a
  reproducible validation record. A hardware-I/O route requires a hardware record for the exact
  device, ES, firmware, transport, host, and capture-card combination claimed.
- **Source-validated experimental** may be implemented without repository-owner hardware only
  when official TI evidence closes every field required by that route. Acquisition descriptors
  include device identity, ES handling, bootstrap/download/runtime sequence, memory map, RF limits,
  lane/data layout, and required assets; decoder descriptors include the exact byte layout,
  geometry, and provenance contract they claim. Both require a closed built-in descriptor, offline
  golden transactions or data, explicit user opt-in, fail-closed validation, honest public
  labeling, and a community hardware-validation path.
- **Planned** means no public route or named descriptor exists. Evidence may be documented, but
  users must not be told that the hardware works.
- Never fill a missing family field with xWR68xx/IWR6843 values, infer board geometry from a part
  ID, or add an arbitrary register-script/custom hardware-JSON escape hatch. Leave the route
  planned until every safety-relevant field is sourced and reviewed.
- Community reports can promote an experimental combination only after the record is reproducible
  and repository coverage pins the proven identity, assets, stages, and failure behavior. A report
  for one board, ES, or host library does not promote adjacent combinations.

## Mandatory small batches

Large tasks must be split before implementation. Do not combine implementation, whole-repository
audits, documentation rewrites, and releases into one batch.

Before editing, define multiple independently verifiable batches when any of these conditions apply:

- more than two packages or eight repository files are affected;
- more than one public behavior changes;
- implementation is combined with repository-wide documentation, a release, or migration work;
- continuous work is expected to exceed 30 minutes.

Each batch must follow these rules:

1. Give the batch one primary objective and a clear exit condition.
2. Modify only the files required for that objective; add the narrowest relevant tests for behavior changes.
3. Run narrow validation and inspect the diff before reporting the checkpoint. When commits are authorized, commit each batch separately.
4. If a new issue crosses another package, exceeds the file limit, or changes the agreed design, stop expanding the batch and split again.
5. Sub-agents may perform bounded, non-overlapping read-only audits or small implementations. They must not recombine split work into one large change.

Exceptions to these limits require explicit user approval before editing. Mechanically generated files
must not be used to evade the limits.

## Starting work

1. Read this file, `README.md`, and the directly relevant files under `docs/`.
2. Use read-only Git commands to inspect the branch, worktree, and upstream. Treat existing changes as user-owned.
3. State the current batch, its exit condition, and whether it touches hardware.
4. Prefer local official TI material and included reference source; do not guess protocol details from memory.

## Hardware safety

- Run offline tests and loopback fakes by default.
- Without explicit user authorization in the current task, do not open a serial port, send radar
  commands, or run any `dca ping/version/configure/start/stop/reset-*` operation.
- When the user authorizes a probe, attempt each target once. Stop after the first timeout, bind
  error, or no response. The only exception is TI's fixed baud negotiation within one
  `debug-capture` Enhanced COM connection. A new connection attempt requires a power, cable, or
  ownership change followed by a user request.
- Do not scan serial ports or guess the CLI port, firmware, or baud. Enhanced COM permits only the
  fixed 921600-to-115200-to-921600 negotiation. It may enter that sequence only after the initial
  read-only probe times out or violates TI's one-to-eight-digit hexadecimal rule and the port closes
  successfully. After the 115200 probe, IWR6843 part `0xE2` must pass validation before any state
  write. An invalid low-speed probe, indeterminate state write, or close failure must stop without
  continuation or retry.
- Do not run two DCA control commands concurrently; they contend for host UDP port 4096 by default.
- `SystemAlive` belongs only to an explicit `dca ping`; it is not an automatic configure/capture prerequisite.
- An indeterminate StartRecord result must not be retried. Only one bounded StopRecord recovery is allowed.
- Reset requires an explicit option or user request. Normal capture must permit DCA1000 reuse.

## Implementation constraints

- Preserve the responsibility boundaries of `radar`, `dca`, `session`, `capturefile`, and
  `serialport`. Keep platform differences in small OS transport files.
- Complete all CFG and capture-combination preflight before creating output or performing hardware I/O.
- Integrated capture order is: configure the radar, start DCA recording, then start the radar. A
  successful finite debug capture consumes the firmware's natural frame-end event. Other paths stop
  the radar explicitly, perform bounded draining, stop DCA, and drain control status within a bound.
- Never retry an indeterminate start. Cleanup uses an independent, bounded context.
- Create output exclusively as `OUT.part`. Publish without overwrite as `OUT` only after capture and
  cleanup both succeed. Retain `.part` on failure.
- A finite frame sequence must match the exact byte count derived from its CFG. Gaps, overlaps,
  missing prefixes, short streams, and long streams all fail.
- Validate DCA response structure and command codes. Preserve TI CLI-compatible handling of
  responses from an unknown source address.
- Preserve raw output exactly. Do not reorder, parse, run FFT/detection, or repair data implicitly.
- Offline tests do not promote a hardware-I/O route to **Supported**. Supported compatibility
  claims require a reproducible hardware-validation record; source-only claims must remain labeled
  **Source-validated experimental** and satisfy the tier requirements above.

See `docs/architecture.md` for protocol and state-machine details.

## Validation

Start with the narrowest relevant command. Expand to the full repository for protocol, lifecycle,
concurrency, or release-path changes:

```text
CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 go build -trimpath -o bin/mmwcli ./cmd/mmwcli
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/mmwcli-linux-amd64 ./cmd/mmwcli
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o bin/mmwcli-linux-arm64 ./cmd/mmwcli
```

Report the commands actually run and distinguish offline validation, environment checks, and hardware
validation. Default validation must not access hardware, install dependencies, or download toolchains.

## Git and public documentation

- The default development branch is `dev`. Push ordinary commits to `origin/dev`; never force-push.
- Do not commit, push, switch branches, rewrite history, create tags, or publish a release without explicit user authorization.
- Commit messages use `<type>(<scope>): <description>`, with one logical change per commit.
- Public documentation describes only reproducible installation, capabilities, limitations, and
  validation. Do not include local absolute paths, agent work history, past mistakes, temporary state,
  or unvalidated compatibility claims.
- A behavior change must update its tests and directly related documentation. Avoid repeating the same
  details across multiple documents.
