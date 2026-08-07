# mmwcli

`mmwcli` is a command-line acquisition runtime for TI xWR68xx radars and DCA1000. It applies
radar configurations, coordinates capture lifecycles, and writes raw ADC data with finite-capture
integrity checks. It does not include a GUI, MATLAB, a Lua host, the mmWave Studio host runtime, or
a data-processing pipeline.

## Supported scope

- 0.1 baseline: xWR6843 ES2, single chip, legacy frame, 16-bit complex ADC, and two hardware LVDS lanes.
- Functional/application mode: text serial control through the SDK demo CLI or TI `studio_cli` device firmware.
- Debug mode: download user-supplied MSS/BSS firmware in SOP2, then control the radar through FTDI D2XX and mmWaveLink.
- DCA1000 data is written at its byte offset without reordering, parsing, or repair.
- Advanced frame, cascade, LVDS headers, software LVDS, RF monitor UART, CSI-2, and TSW1400 are not supported.

The `studio_cli` route only requires the user to flash `mmwave_Studio_cli_xwr68xx.bin`; neither Radar
Toolbox nor mmWave Studio is required at runtime. The debug route also has no dependency on mmWave
Studio host components.

Version 0.1 has hardware validation only for debug mode with Windows/amd64, FTDI D2XX 3.2.14,
IWR6843 ES2, and DCA1000. Other combinations remain unvalidated; see the
[hardware smoke test](docs/hardware-smoke-test.md#debug-mode-01-hardware-validation-record).

## Download

Prebuilt binaries and checksums are available from [GitHub Releases](https://github.com/AIoT-Laboratory/mmwcli/releases/latest).

## Build

Go 1.26 or newer is required. The repository has no third-party Go modules.

```sh
make check
make build
```

Core builds use `CGO_ENABLED=0`. The Windows D2XX backend loads the system-installed `ftd2xx.dll`:

```powershell
$env:CGO_ENABLED = '0'
go build -trimpath -tags ftd2xx -o bin/mmwcli.exe ./cmd/mmwcli
Remove-Item Env:CGO_ENABLED
```

The Linux D2XX backend requires the official `ftd2xx.h` and `libftd2xx.so` for the target architecture:

```sh
CGO_ENABLED=1 go build -trimpath -tags ftd2xx -o bin/mmwcli ./cmd/mmwcli
```

The repository and release archives do not distribute FTDI libraries, headers, drivers, or TI firmware.

## Offline checks

```text
mmwcli doctor
mmwcli firmware verify PATH/mmwave_Studio_cli_xwr68xx.bin
mmwcli studio-cli check hardware/studio-cli-xwr6843-raw.cfg
mmwcli debug-capture check --bss-fw PATH/xwr68xx_radarss.bin --mss-fw PATH/xwr68xx_masterss.bin
mmwcli debug-capture native-check
```

These commands do not open devices; `native-check` only loads the D2XX library. A successful check
does not validate a hardware combination.

## REPL

```text
mmwcli repl --port PORT
```

`repl` accepts only firmware that passes the xWR68xx `version` gate and implements the TI
`studio_cli` line protocol. An unknown response closes the session without an automatic retry. The
REPL can send extension commands from compatible firmware, but that does not make the firmware a
supported capture backend.

## xWR6843 + DCA1000 quick start

The default host address is `192.168.33.30/24`; the DCA1000 address is `192.168.33.180`; the UDP
control/data ports are `4096/4098`. The two modes below use different firmware, ports, and control
protocols and must not be mixed.

### Functional/application mode

Flash `mmwave_Studio_cli_xwr68xx.bin`, boot the radar in normal functional/application mode, and
explicitly provide that firmware's CLI UART. The first capture sends the complete configuration:

```text
mmwcli studio-cli capture hardware/studio-cli-xwr6843-raw.cfg capture-01.bin --port PORT
```

Without changing the firmware, SOP, CFG, serial port, or DCA1000 connection, test reuse without
reconfiguration:

```text
mmwcli studio-cli capture hardware/studio-cli-xwr6843-raw.cfg capture-02.bin --port PORT --no-reconfig
```

Do not use `--reset` for either run. The example CFG should produce exactly `26,214,400` bytes per
capture, with no missing/discarded data or leftover `.part` file. This route has not completed
hardware validation.

### Debug mode

Use a build with the `ftd2xx` tag, obtain the BSS/MSS files from the xWR68xx RF evaluation firmware,
and explicitly provide the Enhanced COM port and the D2XX base for the same FTDI device:

```text
mmwcli debug-capture capture hardware/debug-capture-xwr6843-raw.cfg capture-debug.bin --enhanced-port PORT --bss-fw PATH/xwr68xx_radarss.bin --mss-fw PATH/xwr68xx_masterss.bin --d2xx-description AR-DevPack-EVM-012 --sop2-reset
```

`AR-DevPack-EVM-012` is the description base used for the 0.1 hardware validation. The program
derives `AR-DevPack-EVM-012 A/B`, plus C/D when `--sop2-reset` is enabled.

Serial-number selection is also supported. For example, if D2XX reports interface serials
`FT1234A`, `FT1234B`, `FT1234C`, and `FT1234D`, use `--d2xx-serial FT1234`. This is a format example,
not the serial of the 0.1 validation device. The description and serial selectors are mutually
exclusive.

Enhanced COM only downloads MSS/BSS. D2XX A/B then carries mmWaveLink control, while DCA1000 sends
ADC data over Ethernet. `--sop2-reset` uses D/C to select SOP2 and reset the radar target; it is
unrelated to the DCA FPGA `--reset`. Every debug capture downloads and configures the radar again;
`--no-reconfig` is not supported.

Do not use `studio-cli capture` in SOP2 or treat Enhanced COM as a text CLI UART. TI asset names and
checksums are listed in the [TI reference map](docs/ti-reference-map.md).

## Runtime semantics

- CFG, mode, expected byte count, and output paths are checked before hardware I/O.
- An unknown Start result is not retried; failure paths use bounded cleanup.
- A finite capture must match the exact byte count derived from its CFG.
- Raw output is written to `OUT.part` and published without overwrite as `OUT` only after full success; failures retain `.part`.
- `studio-cli capture` and `debug-capture capture` accept `--session-dir`. Success publishes `OUT/adc.bin`, `OUT/radar.cfg`, and `OUT/capture.json`; failure retains `OUT.part/`. Without the flag, output remains a raw file.
- Low-level `dca` commands must not run concurrently; `dca ping` is not a capture prerequisite; reset requires an explicit command or option.
- `sensorStop` stops the sensor only; it does not power off the radar board or RF domain.

Run `mmwcli help` or a subcommand's `--help` for commands and options. See the
[architecture](docs/architecture.md) for design details.

## License

mmwcli is licensed under the [MIT License](LICENSE). TI and FTDI assets remain subject to their own
terms; see the [third-party notices](THIRD_PARTY_NOTICES.md).
