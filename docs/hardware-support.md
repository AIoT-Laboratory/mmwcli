# Hardware support

Hardware acquisition and decoding are separate capabilities. A device can have a source-backed
`mmwcore` byte layout without having an implemented `mmwcli` boot, download, or capture
route. Conversely, a successful acquisition does not prove the antenna geometry selected for
processing. The tables below keep those claims separate.

## Public family routes

These routes are callable today. The evidence tier records how far each route has been reproduced;
it is not a hidden feature gate.

| Family selection | Public acquisition route | Starting configuration | Current evidence |
| --- | --- | --- | --- |
| `--family xwr16xx` | `mmwcli debug-cli check/capture` | `hardware/debug-cli-xwr16xx-raw.cfg` | Source-validated experimental; community hardware reports requested |
| `--family xwr18xx` | `mmwcli debug-cli check/capture` | `hardware/debug-cli-xwr18xx-raw.cfg` | Source-validated experimental; community hardware reports requested |
| `--family xwr68xx` | `mmwcli debug-cli check/capture` | `hardware/debug-cli-xwr6843-raw.cfg` | Public family route with one supported exact hardware record |
| xWR68xx dedicated firmware | `mmwcli studio-cli check/capture` | `hardware/studio-cli-xwr6843-raw.cfg` | Source-validated experimental family route |

There is no default family or experimental unlock flag. Select the family explicitly, use the
matching assets and CFG domain, and report the result. A failed run is useful evidence too.

## Support tiers

- **Supported**: a public, fail-closed implementation has relevant automated coverage and a
  reproducible validation record. Hardware-I/O routes additionally require a recorded hardware
  run for the exact device/ES/firmware/host combination in scope.
- **Source-validated experimental**: a public implementation is derived from named TI sources,
  uses a closed descriptor, and has offline golden coverage, but the listed hardware combination
  has not completed the supported-tier validation. The user must select the route or descriptor
  explicitly; no other family's parameters are used as a fallback. A descriptor may claim only
  the identity scope it actually observes. Unobserved model and revision values remain empty, and
  fields outside the schema remain absent; neither can support a model-level compatibility claim.
- **Planned**: evidence or a desired use case is known, but there is no public route or named
  descriptor to invoke. This is not usable hardware support.
- **Not supported**: the current acquisition or data contract cannot represent the workflow.

An experimental contribution must identify official source evidence, close every field it claims,
add golden tests, require explicit family or descriptor selection, and reject mismatched inputs.
If a safety parameter depends on an unobserved part, ES, board, asset, or response, the route remains
planned. Arbitrary register scripts and user-authored hardware-protocol JSON are not accepted as a
substitute for a reviewed descriptor.

## Acquisition and decode matrix

| Device or board | `mmwcli studio-cli` acquisition | `mmwcli debug-cli` acquisition | `mmwcore` raw layout | `mmwcore` antenna geometry |
| --- | --- | --- | --- | --- |
| IWR6843 ES2 part `0xE2` with the recorded DCA1000/host combination | Source-validated experimental at xWR68xx family level only; model, part, ES, and board are not observed | **Supported** with `--family xwr68xx`; exact firmware and environment in the hardware smoke test only | **Supported** `group2_i_then_q` for the documented complex16 capture contract | Source-validated experimental IWR6843ISK preset when that is the actual board; the capture record does not prove geometry |
| IWR6843ISK combinations outside that record, with part `0xE2` | Source-validated experimental at xWR68xx family level only; no ISK identity is observed | Source-validated experimental with `--family xwr68xx`; the route observes the accepted part, not the board | Source-validated experimental `group2_i_then_q` | Source-validated experimental IWR6843ISK preset |
| IWR6843 AOP | Planned; an AOP-specific platform alias and capture routing have not been established | Planned | Source-validated experimental `group2_i_then_q` only when the producer proves that layout | Source-validated experimental IWR6843 AOP preset |
| Other non-AOP xWR68xx parts or boards reporting exact `Platform: xWR68xx` | Source-validated experimental at family level only with the dedicated asset and closed two-lane contract; model, part, ES, board, and geometry are not observed | Planned | Source-validated experimental `group2_i_then_q` | Planned; caller must provide reviewed board geometry rather than select an ISK/AOP preset by family |
| xWR16xx devices with accepted IDs `0x60`, `0x61`, `0x04`, `0x62`, `0x67`, `0x66`, `0x01`, `0xC0`, or `0xC1` | Planned; `studio-cli` is xWR68xx-only | Source-validated experimental with `--family xwr16xx`; requires the pinned assets and a responsive 921600-baud monitor | Source-validated experimental `group2_i_then_q` | Source-validated experimental XWR1642 preset only when it matches the actual board |
| xWR18xx devices with accepted IDs `0x70`, `0x71`, `0xD0`, or `0x05` | Planned; `studio-cli` is xWR68xx-only | Source-validated experimental with `--family xwr18xx`; requires the pinned assets and a responsive 921600-baud monitor | Source-validated experimental `group2_i_then_q` | Source-validated experimental standard XWR1843 EVM or AWR1843 AOP preset only when it matches the actual board |
| xWR1443 | Planned | Planned | Planned; audited TI sources conflict on device-specific raw interleave | Planned; no named board geometry preset |
| AWR1243 | Planned | Planned | Source-validated experimental `group4_i_then_q` | Planned; no named board geometry preset |
| xWRL64xx / IWRL6432 (sometimes shortened to “xWR64”) | Planned; no descriptor is derived from xWR68xx | Planned | Planned; no named raw layout | Planned; no named board geometry |
| AOP boards not named above | Planned | Planned | Planned until the physical data path and byte layout are sourced | Planned until the exact board geometry is sourced |
| Cascaded or multi-chip radar | Not supported end to end | Not supported end to end | Not supported for cross-device assembly, ordering, timing, or phase calibration | Not supported as a cascade geometry |
| Any unlisted TI device, ES, board, or capture interface | Planned | Planned | Planned | Planned |

“Source-validated experimental” for a decoder describes only the named byte layout or geometry. It
does not certify the producing board, cable, DCA firmware, LVDS electrical path, or radar profile.
The caller must select both the actual capture layout and the actual board geometry.

## Family facts behind the descriptors

These are maxima and reference-path properties found in the audited TI sources, not a promise that
every board exposes every channel or lane. “Layout” is the narrow complex16 raw contract currently
implemented by `mmwcore`.

| Family | RF API domain | TX / RX | ADCBuf | DCA1000 LVDS lanes | `mmwcore` layout | Audited RF-evaluation assets | Current limitation |
| --- | --- | ---: | ---: | ---: | --- | --- | --- |
| AWR1243 | 77 GHz scale; 76–81 GHz API range | 3 / 4 | 16 KiB | 4 | `group4_i_then_q` | shared xWR12xx/xWR14xx MSS+BSS | mmwcli boot/download and runtime descriptors are not closed |
| xWR1443 | 77 GHz scale; 76–81 GHz API range | 3 / 4 | 16 KiB | 4 | candidate `group4_i_then_q`; evidence conflict | shared xWR12xx/xWR14xx MSS+BSS | device-specific raw interleave and mmwcli runtime descriptors are not closed |
| xWR16xx | 77 GHz scale; 76–81 GHz API range | 2 / 4 | 32 KiB | 2, DCA type 2 | `group2_i_then_q` | pinned xWR16xx MSS+BSS | public source-validated experimental debug route; low-power ADC mode 1; warm 921600-baud monitor only; no repository-owner hardware record |
| xWR18xx | 77 GHz scale; 76–81 GHz API range | 3 / 4 | 32 KiB | 2, DCA type 2 | `group2_i_then_q` | pinned xWR18xx MSS+BSS | public source-validated experimental debug route; low-power ADC mode 0; warm 921600-baud monitor only; no repository-owner hardware record |
| xWR6843 | 60 GHz scale; 57–64 GHz API range | 3 / 4 | 32 KiB | 2 | `group2_i_then_q` | xWR68xx MSS+BSS; xWR68xx `studio_cli` | Studio is family-level experimental with only `Platform: xWR68xx` observed; supported debug is restricted to IWR6843 ES2 part `0xE2` and the pinned assets |

Exact asset sizes and hashes, device-ID evidence, and the source paths used for this table are in
the [TI reference map](ti-reference-map.md). mmwcli does not ship those TI assets.

The named layouts cover legacy-frame, 16-bit complex ADC captures without LVDS headers. Real-only
ADC, complex 2x, CP/CQ or mixed payloads, LVDS headers, advanced frames, CSI-2, and cascade assembly
are not implied by the family row. A different data path needs a separately sourced descriptor and
golden fixture.

The three public debug routes are selected only by the exact values `xwr16xx`, `xwr18xx`, and
`xwr68xx`. The repository provides matching starting configurations:

| Selection | Starting CFG |
| --- | --- |
| `--family xwr16xx` | `hardware/debug-cli-xwr16xx-raw.cfg` |
| `--family xwr18xx` | `hardware/debug-cli-xwr18xx-raw.cfg` |
| `--family xwr68xx` | `hardware/debug-cli-xwr6843-raw.cfg` |

There is no default family. A successful xWR16xx or xWR18xx run is valuable validation evidence;
users do not need to wait for the repository owner to possess the same board before using the
public route.

## Help validate another combination

Hardware owners are invited to submit the
[hardware validation issue form](https://github.com/AIoT-Laboratory/mmwcli/issues/new?template=hardware-validation.yml). A useful
report identifies the route, exact device/board/part/ES, firmware SHA-256 values, runtime and host
versions, D2XX and DCA1000 versions, CFG hash, last completed stage, result, and a redacted log.
Reports of failures are as useful as successes.

Do not upload TI firmware, SDK/Toolbox files, FTDI binaries, private network details, credentials,
or other licensed assets. Provide filenames, versions, byte counts, hashes, commands, and redacted
logs only. A community success report is evidence for review; promotion to **Supported** still
requires a reproducible record and repository-maintained fail-closed coverage.
