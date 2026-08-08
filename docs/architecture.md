# Architecture and safety guarantees

`mmwcli` separates command parsing, device protocols, capture coordination, and operating-system I/O.

```text
cmd/mmwcli
  -> internal/app
  -> internal/firmware
  -> internal/debugcapture -> internal/d2xx
  -> internal/radar -> internal/serialport
  -> internal/session
     -> internal/dca
     -> internal/capturefile
```

Go 1.26+ and the standard library define the core build. Default builds use `CGO_ENABLED=0`. The `ftd2xx` tag loads the installed DLL from pure Go on Windows; Linux is the only CGo variant and links the user-installed official FTDI library. Each native-library architecture requires separate hardware validation.

## Mode boundaries

- `studio-cli` uses the dedicated xWR68xx/IWR6843 TI firmware at 921600 baud. This route has source-backed offline validation only.
- `repl` is a `studio_cli` utility and accepts only that validated line protocol.
- `debug-capture` downloads the recorded IWR6843 ES2 RF-evaluation firmware through Enhanced COM and controls mmWaveLink through D2XX.

Each text connection validates xWR68xx before its first state write. Ports and D2XX devices are operator-selected; mmwcli does not scan or guess them. Current support must not be generalized to another device without independent implementation and validation.

Text commands require their own echo followed by explicit `Done` or numeric `Error`. Timeout, cancellation, write failure, or an incomplete response leaves device state indeterminate and closes the connection without retry.

## Capture preflight

Integrated capture constructs an immutable plan before creating output or opening hardware. Preflight requires:

- one chip, legacy software-triggered frames, and complete ordered references;
- complex16 ADC, hardware ADC LVDS, no LVDS header, and two-lane DCA raw mode;
- a profile, channel mask, frequency, and Tx selection valid for the fixed xWR68xx contract;
- enough Rx, sample, chirp, loop, and frame information to derive the exact finite byte count;
- valid ADCBuf and CBUFF sizes.

Advanced frames, continuous capture, monitor streams, loopback, software LVDS, and LVDS headers are rejected. A full `studio_cli` configuration begins with one exact `flushCfg`. `sensorStart`, when present, must be unique and last; the coordinator removes it before applying the configuration.

`--no-reconfig` still runs the complete preflight against the declared CFG but sends only `sensorStart 0`. Exact source-derived xWR68xx and buffer limits belong in the [TI reference map](ti-reference-map.md).

## Debug transport boundary

`debug-capture` is not a UART dialect. The user supplies the recorded IWR6843 MSS/BSS firmware; mmwcli does not discover TI installations or load the mmWave Studio runtime.

Enhanced COM performs bounded firmware-memory download. Cold-start negotiation validates IWR6843 part `0xE2` and may switch once from 115200 to 921600 baud. A failed or indeterminate write is never retried.

D2XX A/B then carries MPSSE SPI/IRQ and mmWaveLink control. D/C is derived from the already validated description or serial base only when `--sop2-reset` is explicit. Device selection never uses enumeration, index, raw USB, or bundled FTDI code.

The debug plan fixes BSS-before-MSS download, data-path configuration, RF power-up, profile/chirp/frame programming, and natural finite-frame completion. Queue and message sizes are bounded; an overflow poisons the client and stops later I/O.

Firmware hashes, version gates, register operations, and memory limits belong in the [TI reference map](ti-reference-map.md).

## Capture lifecycle

```text
preflight
  -> recover stopped radar/DCA state
  -> configure DCA1000
  -> apply radar configuration
  -> arm data reception
  -> StartRecord once
  -> start radar once
  -> receive and validate exact data
  -> radar stop or verified natural frame-end
  -> bounded data/control drain
  -> StopRecord
  -> sync and close
  -> publish without overwrite
```

The text and debug transports implement this lifecycle without being opened or mixed together. An indeterminate StartRecord is never resent. Cleanup permits one independent, bounded StopRecord. A finite debug capture consumes the natural frame-end event; incomplete and cancelled paths explicitly stop the radar.

`sensorStop` stops sensing only. It does not power off the radar or DCA1000.

## DCA1000 reception

Capture does not automatically run SystemAlive/`ping` or reset the FPGA. Low-level DCA commands are serialized, and reset occurs only through an explicit command or option.

Control responses must be exactly eight bytes with the expected header, trailer, and command code. For TI CLI compatibility, mmwcli accepts matching responses from any IPv4 source address; the DCA control protocol does not authenticate the sender. Data packets remain restricted to the configured DCA address.

Raw data is placed by its 48-bit byte offset. Out-of-order packets are supported, while overlaps, missing prefixes, malformed payloads, and output-limit violations fail. Coverage range tracking has a fixed bound; excessive sparsity fails before writing the rejected packet. Finite capture succeeds only after exact byte coverage and the bounded post-target quiet interval.

Payload bytes are written unchanged. mmwcli does not reorder samples, parse ADC values, or repair gaps.

Network defaults and laboratory timing checks belong in the [hardware smoke test](hardware-smoke-test.md).

## Future stream boundary

A future trusted real-time path will keep exclusive hardware ownership in `mmwcli` and expose a versioned stream contract for `mmwcore` consumers. No live stream contract or transport is implemented today; current integration ends at raw files or capture-session directories.

## Transactional output

Output exists only as `OUT.part` during capture. Existing final or partial output fails before hardware access. Publication never overwrites `OUT` and occurs only after reception, radar cleanup, DCA cleanup, status checks, synchronization, and close succeed.

Raw-file mode publishes one ADC file. With `--session-dir`, `studio-cli capture` and `debug-capture capture` stage `adc.bin`, the exact `radar.cfg`, and versioned `capture.json`, then publish the directory through the same no-overwrite transaction. Session-directory output requires a finite, mmwcore-representable xWR68xx CFG with `adcCfg 2 1`.

The output parent is a cooperative namespace. These guarantees cover runtime atomic visibility and no-overwrite publication; they do not claim power-loss durability or protection from a same-user process mutating the staging directory.

## Validation boundary

Offline tests establish parser, protocol, resource-bound, and lifecycle behavior, not hardware compatibility. Version 0.1 validated only Windows/amd64, FTDI D2XX 3.2.14, IWR6843 ES2 part `0xE2`, and DCA1000 debug mode. The complete record and all exclusions are in the [hardware smoke test](hardware-smoke-test.md).
