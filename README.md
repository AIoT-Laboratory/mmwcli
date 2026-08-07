# mmwcli

`mmwcli` configures TI xWR radars, coordinates DCA1000 capture, and publishes raw ADC data with finite-capture integrity checks. It has no GUI, MATLAB, Lua host, mmWave Studio runtime, or signal-processing pipeline.

## Scope

- SDK demo mode supports ordinary xWR16xx, xWR18xx, xWR64xx, and xWR68xx firmware through `--radar-family`; xWR68xx is the default. AOP aliases are not supported.
- TI `studio_cli`, REPL, `debug-capture`, and capture-session v1 remain xWR68xx-specific.
- The debug baseline is IWR6843 ES2, legacy frames, complex16 ADC, two LVDS lanes, and DCA1000 raw capture.
- Advanced frames, cascade, LVDS headers, software LVDS, CSI-2, and implicit ADC processing are outside the contract.

Version 0.1 validated only `debug-capture` on Windows/amd64 with IWR6843 ES2, DCA1000, and FTDI D2XX 3.2.14. See the [hardware record](docs/hardware-smoke-test.md#debug-mode-01-hardware-validation-record). The SDK demo families and `studio-cli` path currently have source-backed offline validation only.

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
mmwcli demo check PATH/profile.cfg --radar-family xwr18xx
mmwcli studio-cli check hardware/studio-cli-xwr6843-raw.cfg
mmwcli debug-capture check --bss-fw PATH/xwr68xx_radarss.bin --mss-fw PATH/xwr68xx_masterss.bin
mmwcli debug-capture native-check
```

These commands do not open radar or DCA devices. `native-check` only loads the D2XX library.

## Capture workflows

### SDK demo firmware

```text
mmwcli demo capture PATH/profile.cfg capture.bin --port PORT --radar-family xwr18xx
```

The profile must explicitly enable compatible hardware ADC LVDS output.

### xWR68xx `studio_cli`

Flash `mmwave_Studio_cli_xwr68xx.bin`, boot in functional/application mode, and provide its CLI UART:

```text
mmwcli studio-cli capture hardware/studio-cli-xwr6843-raw.cfg capture.bin --port PORT
```

The complete two-run `--no-reconfig` procedure is in the [hardware smoke test](docs/hardware-smoke-test.md).

### xWR68xx debug mode

Use an `ftd2xx` build and user-supplied xWR68xx RF-evaluation BSS/MSS firmware:

```text
mmwcli debug-capture capture hardware/debug-capture-xwr6843-raw.cfg capture.bin \
  --enhanced-port PORT \
  --bss-fw PATH/xwr68xx_radarss.bin \
  --mss-fw PATH/xwr68xx_masterss.bin \
  --d2xx-description AR-DevPack-EVM-012 \
  --sop2-reset
```

`AR-DevPack-EVM-012` is the D2XX description base used for the 0.1 validation. A serial-base example is `--d2xx-serial FT1234` for interfaces `FT1234A`/`FT1234B` and, with `--sop2-reset`, `FT1234C`/`FT1234D`. The example serial is not the validation board's serial.

Enhanced COM downloads firmware; D2XX A/B carries mmWaveLink control. D/C is opened only for explicit `--sop2-reset`.

### REPL

```text
mmwcli repl --port PORT
```

REPL accepts only the xWR68xx `studio_cli` line protocol. A response without explicit `Done` or numeric `Error` terminates the session.

## Output guarantees

- CFG, mode, size, and output checks complete before hardware access.
- StartRecord is sent once; an indeterminate result is never retried.
- Finite captures require exact byte coverage. Gaps, overlaps, short data, and extra data fail.
- Output is staged as `OUT.part` and published as `OUT` without overwrite only after capture and cleanup succeed. Failure retains `.part`.
- `studio-cli capture` and `debug-capture capture` accept `--session-dir`, publishing `adc.bin`, `radar.cfg`, and `capture.json` as one no-overwrite directory transaction.
- Low-level DCA commands are serialized; `ping` is not a capture-readiness gate, and reset occurs only through an explicit command or option.
- `sensorStop` stops the sensor only; it does not power off the radar or DCA1000.

See the [architecture](docs/architecture.md), [hardware smoke test](docs/hardware-smoke-test.md), and [TI reference map](docs/ti-reference-map.md).

## License

mmwcli is licensed under the [MIT License](LICENSE). TI and FTDI assets retain their own terms.
