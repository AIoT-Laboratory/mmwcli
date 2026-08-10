# IWR6843 + DCA1000 hardware smoke test

This document is one reproducible xWR68xx validation record, not the list of usable hardware.
Public xWR16xx, xWR18xx, and xWR68xx routes and their current evidence tiers are listed in the
[hardware support matrix](hardware-support.md).

Sections 1 through 5 define repeatable full-configuration validation for TI `studio_cli` firmware, IWR6843 ES2
part `0xE2`, and DCA1000 in functional/application mode. Version 0.1 has not completed hardware
validation for this path.

## Prerequisites

- IWR6843 ES2 part `0xE2`, DCA1000, a dedicated Ethernet interface, and matching power supplies/cables;
- an obtained copy of `mmwave_Studio_cli_xwr68xx.bin`;
- a built mmwcli binary;
- an operator-confirmed CLI UART;
- laboratory RF safety conditions around the antennas.

Identify jumpers and SOP settings from TI's EVM, DCA1000, and UniFlash documentation. Do not change
SOP or connect/disconnect MIPI while powered. Disconnect the connections required by the Studio CLI
flashing guide, flash the firmware, return to functional/application mode, and power-cycle. The SOP2
host-download mode cannot be used for this test.

## 1. Offline preflight

These commands do not access a serial port or DCA1000:

```text
mmwcli firmware verify PATH/mmwave_Studio_cli_xwr68xx.bin
mmwcli studio-cli check hardware/studio-cli-xwr6843-raw.cfg
```

`firmware verify` checks only the named file and does not require the complete Radar Toolbox. Flash
the file after it passes verification. Do not capture with TI's monitor profile; that profile enables
monitor UART data that mmwcli does not receive.

Record the radar model, part code, ES, firmware SHA-256, SOP setting, and CLI UART. The
`studio-cli version` command confirms only the xWR68xx platform; board or procurement records are
still required to establish IWR6843 ES2 part `0xE2`.

## 2. DCA1000 network

Default topology:

```text
host 192.168.33.30/24
DCA  192.168.33.180
UDP control/data 4096/4098
```

Verify that no other DCA tool owns host UDP port 4096. If connectivity diagnosis is needed, first
confirm that DCA1000 is powered and run this command once:

```text
mmwcli dca version
```

Stop after the first failure; do not poll repeatedly. `dca ping`/SystemAlive is not a capture
prerequisite and may not respond before StartRecord.

## 3. First run: full configuration

Confirm that the radar booted in functional/application mode and replace `PORT` with the
operator-confirmed serial port:

```text
mmwcli studio-cli capture hardware/studio-cli-xwr6843-raw.cfg capture-01 --port PORT
```

The example CFG has 100 finite frames. Each frame has 64 chirps, and each chirp has 4 Rx channels ×
256 complex16 samples. Expected output:

```text
100 × 64 × 4 × 256 × 4 = 26,214,400 bytes
```

Pass criteria:

- exit code 0;
- `capture-01/adc.bin` is exactly `26,214,400` bytes and `radar.cfg` plus `capture.json` are present;
- statistics contain no missing, discarded, malformed, overlap, or fatal asynchronous status;
- no `capture-01.part` remains.

A non-empty `adc.bin` alone is not a pass. A retained `.part` directory is failure evidence; do
not rename it to present it as complete output.

## 4. Optional second full-configuration run

Keep the firmware, SOP, CFG, serial port, and every connection unchanged when checking repeatability:

```text
mmwcli studio-cli capture hardware/studio-cli-xwr6843-raw.cfg capture-02 --port PORT
```

The second run repeats full preflight, DCA1000 setup, and the complete radar configuration. Capture
has no configuration-reuse mode. The same size and integrity criteria apply to both runs; record any
reset or power-cycle between them.

## 5. Interruptions and failures

- After Ctrl+C or `SIGTERM`, wait for `sensorStop -> drain -> StopRecord`; do not cut power immediately.
- A DCA StartRecord response timeout leaves state indeterminate. mmwcli does not resend Start and issues one StopRecord for recovery.
- A short DCA raw tail packet may be delayed by about two seconds; the default 2500 ms quiet/drain interval is expected.
- Do not run DCA commands concurrently; they use the same local UDP port 4096 by default.
- An existing `OUTDIR` or `OUTDIR.part` fails before hardware I/O and is never overwritten.

## Functional/application validation record requirements

Record the radar model/ES, firmware version and hash, SOP, serial port, baud, DCA FPGA version, CFG
hash, packet delay, output size for each run, packet count, sequence gaps, out-of-order count, exit
reason, `.part` state, and any reset or power-cycle between runs.

## Debug mode 0.1 hardware validation record

Debug mode uses the separate SOP2, Enhanced COM, and D2XX/mmWaveLink path and is not part of the
`studio_cli` full-configuration test. One finite-frame validation completed on 2026-08-05 with a command
equivalent to the following; the operator supplied `PORT`, `PATH`, `BASE`, and `OUTDIR` explicitly:

```text
mmwcli debug-cli capture hardware/debug-cli-xwr6843-raw.cfg OUTDIR --enhanced-port PORT --bss-fw PATH/xwr68xx_radarss.bin --mss-fw PATH/xwr68xx_masterss.bin --d2xx-description BASE --sop2-reset --delay-us 100
```

| Item | Validated value |
| --- | --- |
| Radar and capture card | IWR6843 QM, ES2, part `0xE2`; DCA1000 |
| Mode | SOP2 `debug-cli`, legacy frame |
| Host and native boundary | Windows/amd64, FTDI D2XX 3.2.14, `CGO_ENABLED=0`, `ftd2xx` build tag |
| Source revision | `6b3e73e` |
| Test binary SHA-256 | `9A2271DB4D440DD1FF85237392D2254727C93592E60942FCF904D791AD07A80D` |
| BSS firmware SHA-256 | `E2C69405394E35BA376EFE1A52305EE74DBD19F8BAB72BD5A9078878853CD77F` |
| MSS firmware SHA-256 | `316911D4A8DBA1762714A3A107071BD0CF06A135FAE29BFBBC92B037592DE060` |
| CFG | `hardware/debug-cli-xwr6843-raw.cfg` at source revision `6b3e73e`; SHA-256 `A14D9D2986175A03A8EE9B99911404093403211EA3575365DEA6104D2CA14FE8` |
| SOP2 and transport | `--sop2-reset`; `--delay-us 100`; DCA FPGA not reset |
| Expected/actual payload | `26,214,400` bytes / `26,214,400` bytes |
| CLI statistics | `packets=18005 payload=26214400 output=26214400 gaps=0 outOfOrder=0 missing=0` |
| Output transaction | `OUTDIR` published successfully with `adc.bin`, `radar.cfg`, and `capture.json`; no `OUTDIR.part` remained |
| ADC output SHA-256 | `418AFFD7705341CDE90D55EBBB7D01EE4C00C512DAE02DA031E99F400EE08DA1` |

The ADC output is not included in the repository or release. Its hash identifies retained laboratory
validation evidence only. This record establishes only the listed Windows/amd64, D2XX 3.2.14,
firmware, CFG, and hardware combination. It does not validate functional/application mode, Linux
D2XX, arm64, or other firmware, devices, and configurations. The current starting CFG follows the
three-TX synchronized-capture profile and is not the byte-identical CFG from this historical record;
rerunning it creates a new validation record rather than extending this one.
