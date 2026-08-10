# Architecture and data contracts

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
     -> internal/capturestream
  -> internal/multisensorcapture
     -> internal/sensorproducer
     -> internal/multisensor
     -> internal/multisensorstream
  -> internal/fixedframeproducer
```

Go 1.26+ and the standard library define the core build. Default builds use `CGO_ENABLED=0`. The `ftd2xx` tag loads the installed DLL from pure Go on Windows; Linux is the only CGo variant and links the user-installed official FTDI library. Each native-library architecture requires separate hardware validation.

## Mode boundaries

- `studio-cli` uses the dedicated TI firmware at 921600 baud as a source-validated experimental
  xWR68xx family route.
- `repl` is a `studio_cli` utility and accepts only that validated line protocol.
- `debug-cli` requires `--family xwr16xx|xwr18xx|xwr68xx`, downloads that family's pinned
  RF-evaluation firmware through Enhanced COM, and controls mmWaveLink through D2XX. There is no
  default family or model alias. `--device` continues to mean the DCA1000 IPv4 address.

The `studio-cli` text connection requires the exact `Platform: xWR68xx` family response before its
first state write. That response does not observe or prove a model, part, ES, board, or antenna
geometry, and the capture descriptor leaves model and revision empty. `debug-cli` instead binds
the explicitly selected family to its device-ID set, assets, RF plan, and DCA contract. Ports and
D2XX devices are operator-selected; mmwcli does not scan or guess them.

Text commands require their own echo followed by explicit `Done` or numeric `Error`. Timeout, cancellation, write failure, or an incomplete response leaves device state indeterminate and closes the connection without retry.

## Capture preflight

Integrated capture constructs an immutable plan before creating output or opening hardware. Preflight requires:

- one chip, legacy software-triggered frames, and complete ordered references;
- complex16 ADC, hardware ADC LVDS, no LVDS header, and two-lane DCA raw mode;
- a profile, channel mask, frequency, Tx selection, low-power mode, and ADCBuf size valid for the
  explicitly selected xWR16xx, xWR18xx, or xWR68xx contract;
- enough Rx, sample, chirp, loop, and frame information to derive exact bytes per frame and, for a
  finite plan, the exact total byte count;
- valid ADCBuf and CBUFF sizes.

Advanced frames, continuous capture, monitor streams, loopback, software LVDS, and LVDS headers are rejected. A full `studio_cli` configuration begins with one exact `flushCfg`. `sensorStart`, when present, must be unique and last; the coordinator removes it before applying the configuration.

`studio-cli capture` always applies the complete preflighted configuration; capture does not reuse
an existing radar configuration. `studio-cli` is xWR68xx-only. Exact source-derived family and
buffer limits belong in the [TI reference map](ti-reference-map.md).

## Debug transport boundary

`debug-cli` is not a UART dialect. The user supplies the selected family's pinned MSS/BSS firmware;
mmwcli does not discover TI installations or load the mmWave Studio runtime.

The xWR16xx and xWR18xx source-backed experimental routes require a responsive 921600-baud
Enhanced COM monitor. They do not write the xWR68xx baud registers or attempt a 115200-baud cold
path. The xWR68xx descriptor retains the bounded cold-start negotiation that validates IWR6843 part
`0xE2` and may switch once from 115200 to 921600 baud. A failed or indeterminate write is never
retried.

D2XX A/B then carries MPSSE SPI/IRQ and mmWaveLink control. D/C is derived from the already validated description or serial base only when `--sop2-reset` is explicit. Device selection never uses enumeration, index, raw USB, or bundled FTDI code.

The family-bound debug plan fixes BSS-before-MSS download, data-path configuration, RF power-up,
profile/chirp/frame programming, and natural finite-frame completion. xWR16xx uses the 77 GHz scale,
two TX channels, and low-power ADC mode 1; xWR18xx uses the 77 GHz scale, three TX channels, and
low-power ADC mode 0; xWR68xx uses the 60 GHz scale and three TX channels. All three routes use the
closed two-lane DCA1000 type-2 raw contract. Queue and message sizes are bounded; an overflow
poisons the client and stops later I/O.

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
  -> requested radar stop or verified natural frame-end
  -> bounded data/control drain
  -> StopRecord
  -> sync and close
  -> publish without overwrite
```

The text and debug transports implement this lifecycle without being opened or mixed together. An indeterminate StartRecord is never resent. Cleanup permits one independent, bounded StopRecord. A finite debug capture consumes the natural frame-end event; incomplete and cancelled paths explicitly stop the radar. For either capture route, `--frame-count 0 --stop-on-stdin-eof` keeps acquisition open until stdin closes. That EOF is a requested stop and follows normal cleanup, validation, and publication; context cancellation remains an abort.

`sensorStop` stops sensing only. It does not power off the radar or DCA1000.

## DCA1000 reception

Capture does not automatically run SystemAlive/`ping` or reset the FPGA. Low-level DCA commands are serialized diagnostic/control operations, not an independent ADC acquisition route. Reset occurs only through an explicit command or option.

Control responses must be exactly eight bytes with the expected header, trailer, and command code. For TI CLI compatibility, mmwcli accepts matching responses from any IPv4 source address; the DCA control protocol does not authenticate the sender. Data packets remain restricted to the configured DCA address.

Raw data is placed by its 48-bit byte offset. Out-of-order packets are supported, while overlaps, missing prefixes, malformed payloads, and output-limit violations fail. Coverage range tracking has a fixed bound; excessive sparsity fails before writing the rejected packet. Finite capture succeeds only after exact byte coverage and the bounded post-target quiet interval. A requested open-ended stop succeeds only when the drained payload is non-empty and divisible by the CFG-derived bytes per frame.

Payload bytes are written unchanged. mmwcli does not reorder samples, parse ADC values, or repair gaps.

Network defaults and laboratory timing checks belong in the [hardware smoke test](hardware-smoke-test.md).

## Capture stream boundary

`internal/capturestream` implements the finite [capture-stream v1](capture-stream-v1.md) wire encoder
bound to an exact CFG-derived capture plan, a bounded `WriterAt` Mirror, and an OS-stdout encoder
whose close interrupts blocked writes.
`internal/session` can write through that Mirror and seals it before publishing the capture-session
directory. mmwcore implements the matching decoder over a caller-owned `BinaryIO`; it does not own
the process, pipe, or hardware. The decoder is exposed as `mmwcore.io.CaptureStreamReader`.

Both public capture routes always publish a capture-session v1 directory. With `--stream`, the
application also connects the exact session output to the bounded Mirror and reserves stdout for
binary capture-stream v1 records. Diagnostics remain on stderr, stream failure cancels the shared
capture context, and terminal COMMIT or ABORT is followed by EOF. The published session directory
remains the authoritative artifact; this does not move hardware ownership out of mmwcli.
Capture-stream v1 declares an exact positive frame count in SESSION and is therefore intentionally
finite. An open-ended radar-only stream is rejected rather than treating zero as an unknown count.

Multi-sensor capture is implemented as a separate aggregate contract; see
[multi-sensor synchronization](multisensor-sync.md). `--multisensor-plan` launches bounded external
producers behind the radar lifecycle, publishes one no-overwrite aggregate directory, and may use
`--stream` to emit `mmwcli.multisensor_stream.v1`. This does not extend capture-stream v1 or move
device/process ownership into mmwcore.
The aggregate SESSION source limits are finite safety maxima derived from the DCA output byte limit,
not an exact-count promise. Its bounded
Mirror can emit complete radar ITEM records during an open-ended capture; radar END records the
actual whole-frame count after graceful stop. Empty, partial, over-limit, aborted, or uncommitted
streams remain invalid.

## Multi-sensor lifecycle

```text
validate radar CFG and multi-sensor plan
  -> stage OUTDIR.part and nested radar output
  -> launch bounded external producers
  -> READY -> ARM -> START
  -> capture radar and source ITEM records
  -> STOP or CANCEL and reap every producer
  -> validate END + EOF, indices, clocks, sizes, and hashes
  -> finish radar/DCA cleanup
  -> publish the aggregate directory without overwrite
  -> emit global COMMIT + EOF, or ABORT + EOF
```

`mmwcli multisensor init` creates either a fixed-frame camera plan or an `image.jpeg.v1` plan, and
`multisensor check` validates either without opening hardware. The built-in producers wrap an
exact-size byte stream or an MJPEG `image2pipe` stream; the latter emits one complete JPEG per item
for direct offline annotation. Camera SDK and codec-process ownership stay outside mmwcli.
`delivery_observed` records when a complete camera frame reaches mmwcli and never labels it as
exposure time. RADAR_START supplies a conservative radar tick-zero interval on the same
host-relative axis. External-trigger and PTP evidence grades remain future extensions.

## Transactional output

Output exists only as `OUTDIR.part` during capture. Existing final or partial output fails before hardware access. Publication never overwrites `OUTDIR` and occurs only after reception, radar cleanup, DCA cleanup, status checks, synchronization, and close succeed.

`studio-cli capture` and `debug-cli capture` always stage `adc.bin`, the exact effective `radar.cfg`,
and versioned `capture.json`, then publish the capture-session v1 directory through the same
no-overwrite transaction. Capture requires a mmwcore-representable CFG for its declared family with
`adcCfg 2 1`; there is no bare-file output mode. A finalized `frameCfg` count of zero carries no
invented planned length: readers derive the actual positive frame count from the complete ADC file,
and aggregate publication binds that count to `index.bin`.

The output parent is a cooperative namespace. These guarantees cover runtime atomic visibility and no-overwrite publication; they do not claim power-loss durability or protection from a same-user process mutating the staging directory.

## Validation boundary

Automated tests establish parser, protocol, resource-bound, and lifecycle behavior, not hardware
compatibility. xWR16xx, xWR18xx, and xWR68xx are public family routes; their evidence tiers are
listed separately from availability. One recorded run validated Windows/amd64, FTDI D2XX 3.2.14,
IWR6843 ES2 part `0xE2`, and DCA1000 debug mode. The complete record and all exclusions are in the
[hardware validation record](hardware-smoke-test.md).
