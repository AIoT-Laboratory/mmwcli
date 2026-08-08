# mmwcli

`mmwcli` controls its documented TI xWR68xx/IWR6843 acquisition routes, coordinates DCA1000 capture, and publishes raw ADC data with finite-capture integrity checks. It has no GUI, MATLAB, Lua host, mmWave Studio runtime, or signal-processing pipeline.

## Scope

- `studio-cli` and its REPL utility form a source-validated experimental xWR68xx family route for the dedicated TI firmware. They require the exact `Platform: xWR68xx` family response but do not observe or prove a model, part, ES, or board.
- `debug-cli` hardware validation is limited to IWR6843 ES2 part `0xE2`, DCA1000, Windows/amd64, and FTDI D2XX 3.2.14.
- The capture baseline is legacy frames, complex16 ADC, two LVDS lanes, and DCA1000 raw output.
- Advanced frames, cascade, LVDS headers, software LVDS, CSI-2, and implicit ADC processing are outside the contract.

Version 0.1 validated only the recorded `debug-cli` combination. See the [hardware record](docs/hardware-smoke-test.md#debug-mode-01-hardware-validation-record). The Studio route remains family-level experimental; model-specific combinations and native-library combinations require independent validation.

## Download and build

Download binaries and checksums from [GitHub Releases](https://github.com/AIoT-Laboratory/mmwcli/releases/latest).

Go 1.26 or newer is required. The repository has no third-party Go modules.

```sh
make check
go build -trimpath ./cmd/mmwcli
```

`make build VERSION=x.y.z` compiles all packages with an injected version; it does not create a command binary.

Default builds use `CGO_ENABLED=0`. Enable the D2XX backend with the `ftd2xx` build tag:

- Windows remains pure Go and loads the installed `ftd2xx.dll`.
- Linux is the only CGo variant and requires the user-installed official `ftd2xx.h` and `libftd2xx.so`.

Build the tagged command with `go build -tags ftd2xx ./cmd/mmwcli` on Windows or `CGO_ENABLED=1 go build -tags ftd2xx ./cmd/mmwcli` on Linux.

The repository and release archives do not distribute FTDI or TI assets.

## Offline validation

```text
mmwcli doctor
mmwcli firmware verify PATH/mmwave_Studio_cli_xwr68xx.bin
mmwcli studio-cli check hardware/studio-cli-xwr6843-raw.cfg
mmwcli debug-cli check --bss-fw PATH/xwr68xx_radarss.bin --mss-fw PATH/xwr68xx_masterss.bin
mmwcli debug-cli native-check
```

These commands do not open radar or DCA devices. `native-check` only loads the D2XX library.

## Capture workflows

### xWR68xx `studio_cli`

Flash `mmwave_Studio_cli_xwr68xx.bin`, boot in functional/application mode, and provide its CLI UART. The runtime accepts only the exact `Platform: xWR68xx` family response. Model, part, ES, board, and antenna geometry remain unobserved, so this is family-level experimental support rather than an IWR6843 compatibility claim. AOP-specific platform aliases and every other platform value are rejected. This route does not yet have a hardware-validation record:

```text
mmwcli studio-cli capture hardware/studio-cli-xwr6843-raw.cfg capture-session --port PORT
```

Every `studio-cli capture` performs full preflight and applies the complete configuration; capture does not reuse a prior radar configuration.

### IWR6843 ES2 debug mode

Use an `ftd2xx` build and the user-supplied RF-evaluation BSS/MSS firmware recorded for IWR6843 ES2 part `0xE2`:

```text
mmwcli debug-cli capture hardware/debug-cli-xwr6843-raw.cfg capture-session \
  --enhanced-port PORT \
  --bss-fw PATH/xwr68xx_radarss.bin \
  --mss-fw PATH/xwr68xx_masterss.bin \
  --d2xx-description AR-DevPack-EVM-012 \
  --sop2-reset
```

`AR-DevPack-EVM-012` is the D2XX description base used for the 0.1 validation. A serial-base example is `--d2xx-serial FT1234` for interfaces `FT1234A`/`FT1234B` and, with `--sop2-reset`, `FT1234C`/`FT1234D`. The example serial is not the validation board's serial.

Enhanced COM downloads firmware; D2XX A/B carries mmWaveLink control. D/C is opened only for explicit `--sop2-reset`.

### `studio_cli` REPL

```text
mmwcli repl --port PORT
```

REPL is a `studio_cli` utility, not another firmware backend. It accepts only the validated line protocol; a response without explicit `Done` or numeric `Error` terminates the session.

## Output guarantees

- CFG, mode, size, and output checks complete before hardware access.
- StartRecord is sent once; an indeterminate result is never retried.
- Finite captures require exact byte coverage. Gaps, overlaps, short data, and extra data fail.
- Both capture routes stage `OUTDIR.part` and publish a strict capture-session v1 directory as `OUTDIR` without overwrite only after capture and cleanup succeed. It contains `adc.bin`, the exact `radar.cfg`, and `capture.json`; failure retains the partial directory.
- `--stream` additionally mirrors provisional capture-stream v1 records on binary stdout while diagnostics remain on stderr. The published session directory remains authoritative.
- Low-level DCA commands are diagnostic/control operations only; ADC acquisition is available through `studio-cli capture` and `debug-cli capture`. `ping` is not a capture-readiness gate, and reset occurs only through an explicit command or option.
- `sensorStop` stops the sensor only; it does not power off the radar or DCA1000.

See the [hardware support matrix](docs/hardware-support.md), [architecture](docs/architecture.md), [multi-sensor synchronization design](docs/multisensor-sync.md), [hardware smoke test](docs/hardware-smoke-test.md), and [TI reference map](docs/ti-reference-map.md).

## License

mmwcli is licensed under the [MIT License](LICENSE). TI and FTDI assets retain their own terms.
