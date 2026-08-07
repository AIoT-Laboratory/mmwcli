# Architecture and safety guarantees

mmwcli separates device protocols, the capture state machine, and operating-system I/O so Windows
and Linux share the same behavior.

```text
cmd/mmwcli
  -> internal/app          command parsing and exit codes
  -> internal/firmware     offline validation of one TI device-firmware file
  -> internal/debugcapture SOP2 asset checks, firmware submission, and D2XX/mmWaveLink control
  -> internal/d2xx         optional FTDI D2XX native-library boundary
  -> internal/radar        CLI dialects, CFG preflight, and response parsing
     -> internal/serialport
  -> internal/session      radar and DCA1000 capture state machine
     -> internal/dca       UDP control and raw-data reception
     -> internal/capturefile
```

The project uses Go 1.26+ and the standard library. Core builds use `CGO_ENABLED=0`. Platform code
only implements system I/O; protocols and state machines do not fork by operating system. The
optional D2XX backend is isolated by the `ftd2xx` build tag: Windows loads the system-installed DLL,
while Linux is the only CGo variant and includes the user-installed official `ftd2xx.h` and links
`libftd2xx.so`. Core release targets are Windows/Linux on amd64 and arm64. Each native-backend
architecture requires separate hardware validation.

## Radar CLI dialects

| Dialect | Command prefix | Default baud | Device firmware |
| --- | --- | ---: | --- |
| TI `studio_cli` | `studio-cli` | 921600 | `mmwave_Studio_cli_xwr68xx.bin` |
| mmWave SDK demo | `demo` | 115200 | xWR68xx SDK demo or compatible firmware |

The dialects share a transport layer, but their command sets and start semantics remain isolated.
Both `studio-cli version` and the SDK demo's common `version` extension must report an xWR68xx
platform. Each connection performs this check before its first apply, start, stop, or capture state
write. The operator must specify the serial port explicitly; mmwcli does not enumerate or probe ports.

The top-level `repl` does not add a third dialect. It always creates a `StudioCLI` client, runs the
same xWR68xx `version` validation, and then sends commands line by line using CFG lexical rules.
Compatible firmware may add commands, but it cannot bypass the terminal `Done` or numeric `Error`
contract. An indeterminate result closes the session and does not grant capture support to that
firmware.

Every command is bounded by both the serial timeout and the caller's context deadline. A write
failure, cancellation, timeout, or response without an explicit `Done` or `Error` leaves the device
state indeterminate. Before accepting a terminal response, the next command must see its own echo;
this prevents a late response from completing the wrong command.

## CFG preflight

Integrated capture builds an immutable capture plan before it creates output or opens hardware. The
0.1 contract requires:

- xWR68xx, one chip, `dfeDataOutputMode 1`, and software-triggered legacy frames;
- 16-bit complex ADC with matching ADC and DCA data formats;
- no LVDS header, hardware ADC stream enabled, and software stream disabled;
- two-lane DCA LVDS-to-Ethernet raw mode;
- complete and ordered channel/profile/chirp/frame references;
- no more than 32 KiB of 16-byte-aligned ADCBuf storage across each Rx channel;
- at least 64 bytes per headerless ADC-only CBUFF chirp and no more than `0x3fff` two-byte units per Rx transfer;
- enough Rx, sample, chirp, loop, and frame information to derive the exact output size for a finite capture.

`sensorStart` may be omitted. When present, it must be unique and last; the coordinator removes it
before applying the configuration. A full `studio_cli` configuration must start with `flushCfg` and
pass the raw-only allowlist. `--no-reconfig` still performs the complete preflight on the same CFG,
but sends only `sensorStart 0`; it is available only for `studio-cli`.

Preflight rejects advanced-frame, monitor, continuous, test, loopback, software-LVDS, and
LVDS-header configurations. TI monitor profiles are not runtime dependencies and cannot be used for
this project's raw-only capture path.

## SOP2 direct-control boundary

The SOP2 host-download and direct-control path uses the separate `debug-capture` entry point and is
not a text CLI dialect. Its controller implements `session.Radar` so it can reuse the same DCA1000
capture state machine. The user must provide the MSS/BSS firmware explicitly. mmwcli does not
discover TI installations and does not depend on the mmWave Studio runtime, Lua, or C#. Asset
preflight checks exact hashes, RPRC structure, xWR68xx memory windows, and a write plan whose blocks
are at most 4096 bytes.

When the operator explicitly passes `--sop2-reset`, the controller derives D/C from the already
validated A/B selector. D selects SOP2 through asynchronous bit-bang and holds it while C asserts and
releases NRST; the interfaces then close in C-to-D order. Any indeterminate write or close error
stops the operation before Enhanced COM is opened. Target reset is disabled by default, and mmwcli
does not enumerate C/D.

Enhanced COM opens only the operator-supplied port and never scans devices. Connection first probes
the TI monitor at 921600 baud and accepts one to eight hexadecimal digits under TI's `UInt32
HexNumber` rule. A single fixed 115200 cold-start negotiation is allowed only when that read-only
probe times out or cannot be parsed and the port closes successfully. After a valid low-speed
monitor response, mmwcli reads and validates the xWR6843 part number, preserves the original value
at `0xFFFFE144`, sets `0x7800`, writes TI's 921600 switching value, closes the port, and revalidates at
921600. It tries no other baud rates. An invalid low-speed response, indeterminate switching write,
or close failure terminates immediately without rewriting the switching register.

After the fixed three `x0` handshakes in each 921600 initialization and the TOPRCM part-number check,
firmware is submitted in xWR6843 BSS-to-MSS order. An indeterminate write is not retried, and mmwcli
does not automatically release or reset the target. It then selects D2XX A/B on the same FTDI device
through the explicit serial or description value, completes mmWaveLink startup validation over
MPSSE SPI/IRQ, and closes Enhanced COM. Device selection does not use enumeration, index, or
location, and the host does not implement raw USB.

mmWaveLink startup requires MSS firmware `2.0.0.3` and RF firmware `6.2.1.5`. A CFG that passes the
`studio-cli` contract is translated on the host into fixed RF, LVDS, profile, chirp, frame, and apply
messages; the CFG is not sent as text. RF initialization must report the complete calibration mask.
Frame start and explicit stop each send one trigger and verify the corresponding event; a finite
frame sequence that ends naturally only consumes the frame-end event. Indeterminate results are not
retried. This path does not support `--no-reconfig`.

`debug-capture native-check` only loads the D2XX library; it does not query or open USB devices. The
public capture command performs the same library-only check before firmware submission. Protocol,
failure-state, and orchestration behavior is covered by offline fakes. Version 0.1 completed one
Windows/amd64 native-D2XX hardware validation; see [validation evidence](#validation-evidence).

## Integrated capture state machine

```text
preflight CFG, assets, native boundary, and output paths
  -> open the explicit radar transport and DCA control endpoint
  -> radar stop + StopRecord to recover from a prior session
  -> configure DCA1000 (no reset by default)
  -> apply radar configuration
  -> arm data reception and send StartRecord once
  -> after StartRecord status=0, send radar start once
  -> receive and validate data
  -> successful finite debug capture: await natural frame-end; other paths: radar stop
  -> bounded data drain -> StopRecord -> control drain
  -> sync/close -> publish OUT
```

The functional/application path maps radar start/stop to device-firmware text commands; the debug
path maps them to mmWaveLink frame triggers. The paths are never opened or mixed together.

A missing StartRecord response leaves card state indeterminate. The implementation does not resend
Start and allows only one independent, bounded StopRecord for recovery. A valid finite debug capture
only consumes and verifies the natural frame-end event. Cancellation, timeout, incomplete data,
infinite frames, and text CLI firmware without that capability still stop the radar explicitly.
Cleanup uses an independent context. Even if data continues to arrive, draining has an absolute
limit and is followed by StopRecord.

## DCA1000 protocol and reception

The default network uses host `192.168.33.30`, DCA `192.168.33.180`, UDP control port `4096`, and UDP
data port `4098`. Normal configure/capture operations do not run `SystemAlive` first or reset the
FPGA automatically.

Control frames use TI's little-endian `0xA55A ... 0xEEAA` format. Synchronous responses must match
the command code exactly; asynchronous status is queued separately. For compatibility with TI's
reference CLI, control responses are not authenticated by source address, but their fixed length,
header, trailer, and command code must all be valid. The data channel accepts packets only from the
configured DCA IP.

Each raw datagram has a 10-byte header and at most 1456 bytes of payload. The header supplies a
32-bit sequence number and 48-bit byte offset. The receiver writes by offset and can fill
out-of-order packets, but it does not hide gaps, overlaps, missing prefixes, or malformed payloads.

A finite capture succeeds only after `[0, ExpectedOutputBytes)` is fully covered and the post-target
quiet period elapses. Extra payload, insufficient bytes, and timeouts fail. Unexpected silence also
fails an infinite capture. The DCA raw-mode FPGA can delay a short tail payload by about two seconds,
so the minimum quiet/drain interval is 2500 ms. When small-frame aggregation requires more time, the
coordinator raises first-packet and quiet timeouts from the CFG.

## Transactional output

Output is created exclusively as `OUT.part`; an existing `OUT` or `.part` is rejected before
hardware access. `OUT` is published without overwrite only after reception, radar stop, DCA stop,
asynchronous-status checks, and synchronization all succeed. Failure or cancellation retains
`.part`.

The default output is one raw ADC file. `studio-cli capture` and `debug-capture capture` may instead
use `--session-dir`. This mode first requires a finite, mmwcore-representable xWR68xx CFG snapshot
with `adcCfg 2 1`. It stages `adc.bin`, the exact `radar.cfg` snapshot, and the versioned
`capture.json`, then publishes the complete directory in one no-replace namespace operation. The
output parent is assumed to be cooperative. This guarantees complete runtime visibility, not
power-loss durability.

Windows uses a same-volume move that does not replace the target and supports NTFS, exFAT, and FAT32,
subject to each file system's single-file size limit. Other platforms publish without overwrite in
the same directory and fail safely when the file system cannot provide the required atomic operation.

## Validation evidence

Offline tests cover protocols, CFG handling, loopback, cancellation, and cleanup, but do not prove
hardware compatibility. The two-run finite-frame reuse validation for functional/application mode
is not complete; its requirements are in [hardware-smoke-test.md](hardware-smoke-test.md). Version
0.1 validates only the Windows/amd64, D2XX 3.2.14, IWR6843 ES2, DCA1000 debug-mode combination
recorded there. Linux D2XX, arm64, the SDK demo, and other firmware or configurations are outside
that hardware-validation claim.
