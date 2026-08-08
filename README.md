# mmwcli

`mmwcli` captures raw ADC data from TI xWR16xx, xWR18xx, and xWR68xx devices through
DCA1000. It publishes finite radar or synchronized radar-plus-camera sessions and can mirror both
forms as live streams for inference. It has no GUI, MATLAB, Lua host, mmWave Studio runtime, or
signal-processing pipeline.

## Scope

- `debug-cli --family xwr16xx|xwr18xx|xwr68xx` exposes three closed, family-specific capture
  routes that are available now. xWR16xx and xWR18xx are source-validated experimental routes
  awaiting community hardware reports; xWR68xx also has a repository-maintained hardware record.
- `studio-cli` and its REPL utility remain a source-validated experimental xWR68xx-only route for
  the dedicated TI firmware. They require the exact `Platform: xWR68xx` family response but do not
  observe or prove a model, part, ES, or board.
- The capture baseline is legacy frames, complex16 ADC, two LVDS lanes, and DCA1000 raw output.
- Advanced frames, cascade, LVDS headers, software LVDS, CSI-2, and implicit ADC processing are outside the contract.

The support tier describes available validation evidence, not whether a route can be invoked.
Hardware owners can use all three family routes and submit successful or failed runs. See the
[support matrix](docs/hardware-support.md) for the family-wide view and the
[validation record](docs/hardware-smoke-test.md#debug-mode-01-hardware-validation-record) for one
reproducible xWR68xx combination.

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

## Validate before capture

```text
mmwcli doctor
mmwcli firmware verify PATH/mmwave_Studio_cli_xwr68xx.bin
mmwcli studio-cli check hardware/studio-cli-xwr6843-raw.cfg
mmwcli debug-cli check --family xwr16xx --bss-fw PATH/xwr16xx_radarss.bin --mss-fw PATH/xwr16xx_masterss.bin
mmwcli debug-cli check --family xwr18xx --bss-fw PATH/xwr18xx_radarss.bin --mss-fw PATH/xwr18xx_masterss.bin
mmwcli debug-cli check --family xwr68xx --bss-fw PATH/xwr68xx_radarss.bin --mss-fw PATH/xwr68xx_masterss.bin
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

### xWR16xx, xWR18xx, and xWR68xx debug mode

Use an `ftd2xx` build and select the device family explicitly. There is no default family and no
model-name alias. `--family` selects the radar contract; the existing `--device` option remains the
DCA1000 IPv4 address. Each route requires its family-named mmWave Studio 2.1.1 RF-evaluation assets:

```text
mmwcli debug-cli capture hardware/debug-cli-xwr16xx-raw.cfg capture-xwr16xx \
  --family xwr16xx --enhanced-port PORT \
  --bss-fw PATH/xwr16xx_radarss.bin --mss-fw PATH/xwr16xx_masterss.bin \
  --d2xx-description AR-DevPack-EVM-012

mmwcli debug-cli capture hardware/debug-cli-xwr18xx-raw.cfg capture-xwr18xx \
  --family xwr18xx --enhanced-port PORT \
  --bss-fw PATH/xwr18xx_radarss.bin --mss-fw PATH/xwr18xx_masterss.bin \
  --d2xx-description AR-DevPack-EVM-012

mmwcli debug-cli capture hardware/debug-cli-xwr6843-raw.cfg capture-session \
  --family xwr68xx --enhanced-port PORT \
  --bss-fw PATH/xwr68xx_radarss.bin \
  --mss-fw PATH/xwr68xx_masterss.bin \
  --d2xx-description AR-DevPack-EVM-012 \
  --sop2-reset
```

xWR16xx uses the 77 GHz profile domain, two TX channels, low-power ADC mode 1, and two LVDS lanes.
xWR18xx uses the 77 GHz profile domain, three TX channels, low-power ADC mode 0, and two LVDS
lanes. Both experimental routes require an already-responsive 921600-baud Enhanced COM monitor;
they do not run the xWR68xx cold-start baud-register sequence. xWR68xx uses its 60 GHz profile
domain and retains the validated IWR6843 ES2 cold-start path.

`AR-DevPack-EVM-012` is the D2XX description base used for the 0.1 validation. A serial-base example is `--d2xx-serial FT1234` for interfaces `FT1234A`/`FT1234B` and, with `--sop2-reset`, `FT1234C`/`FT1234D`. The example serial is not the validation board's serial.

Enhanced COM downloads firmware; D2XX A/B carries mmWaveLink control. D/C is opened only for
explicit `--sop2-reset`. The exact asset hashes, accepted device IDs, and validation tiers are in
the [TI reference map](docs/ti-reference-map.md) and [hardware support matrix](docs/hardware-support.md).

### `studio_cli` REPL

```text
mmwcli repl --port PORT
```

REPL is a `studio_cli` utility, not another firmware backend. It accepts only the validated line protocol; a response without explicit `Done` or numeric `Error` terminates the session.

### Synchronized radar and camera capture

Generate a plan for any camera command that writes fixed-size raw frames, validate it without
opening hardware, then pass it to either radar route:

```text
mmwcli multisensor init camera.json --format camera.rgb8.v1 \
  --frame-bytes 921600 --max-items 300 -- ffmpeg ... pipe:1
mmwcli multisensor check camera.json
mmwcli studio-cli capture CFG OUTDIR --port PORT --multisensor-plan camera.json
mmwcli debug-cli capture CFG OUTDIR --family xwr18xx ... --multisensor-plan camera.json
```

The generated plan uses the built-in `sensor-producer fixed-frames` adapter, so ffmpeg,
GStreamer, and vendor camera programs do not need to implement the control/data protocol.
`--stream` may be combined with `--multisensor-plan`: stdout then carries the aggregate radar and
camera stream, while `OUTDIR` remains the authoritative training capture.

Use [`mmwcore.open_multisensor_capture`](https://github.com/AIoT-Laboratory/mmwcore) for lazy
offline training data and `mmwcore.open_multisensor_stream` for a caller-owned live stream.
Radar-only directories and streams use `mmwcore.open_capture` and `mmwcore.open_capture_stream`.
mmwcli owns acquisition; mmwcore owns decoding and processing.

Ordinary cameras use `delivery_observed`: mmwcli timestamps a frame only after receiving it and
does not call that time an exposure timestamp. A producer with a real exposure clock may declare
`exposure_midpoint` and its clock mapping instead. The aggregate live stream carries a conservative
radar-start interval so radar and delivery-observed camera items share one host-relative time axis.

## Output guarantees

- CFG, mode, size, and output checks complete before hardware access.
- StartRecord is sent once; an indeterminate result is never retried.
- Finite captures require exact byte coverage. Gaps, overlaps, short data, and extra data fail.
- Both capture routes stage `OUTDIR.part` and publish a strict capture-session v1 directory as `OUTDIR` without overwrite only after capture and cleanup succeed. It contains `adc.bin`, the exact `radar.cfg`, and `capture.json`; failure retains the partial directory.
- `--stream` additionally mirrors provisional capture-stream v1 records on binary stdout while diagnostics remain on stderr. The published session directory remains authoritative.
- `--multisensor-plan` adds bounded external sources to the same transaction. With `--stream`,
  provisional radar and sensor items become trustworthy only after source outcomes, global COMMIT,
  and EOF.
- Low-level DCA commands are diagnostic/control operations only; ADC acquisition is available through `studio-cli capture` and `debug-cli capture`. `ping` is not a capture-readiness gate, and reset occurs only through an explicit command or option.
- `sensorStop` stops the sensor only; it does not power off the radar or DCA1000.

See the [hardware support matrix](docs/hardware-support.md), [architecture](docs/architecture.md),
[multi-sensor synchronization](docs/multisensor-sync.md),
[hardware validation record](docs/hardware-smoke-test.md), and
[TI reference map](docs/ti-reference-map.md).

## License

mmwcli is licensed under the [MIT License](LICENSE). TI and FTDI assets retain their own terms.
