# Hardware support

Hardware acquisition and offline decoding are separate capabilities. A device can have a
source-backed `mmwcore` byte layout without having a safe `mmwcli` boot, download, or capture
route. Conversely, a successful acquisition does not prove the antenna geometry selected for
processing. The tables below keep those claims separate.

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
add offline golden tests, require explicit opt-in, and fail closed at that claimed identity scope.
If a safety parameter depends on an unobserved part, ES, board, asset, or response, the route remains
planned. Arbitrary register scripts and user-authored hardware-protocol JSON are not accepted as a
substitute for a reviewed descriptor.

## Acquisition and decode matrix

| Device or board | `mmwcli studio-cli` acquisition | `mmwcli debug-capture` acquisition | `mmwcore` raw layout | `mmwcore` antenna geometry |
| --- | --- | --- | --- | --- |
| IWR6843 ES2 part `0xE2` with the recorded DCA1000/host combination | Source-validated experimental at xWR68xx family level only; model, part, ES, and board are not observed | **Supported**; exact firmware and environment in the hardware smoke test only | **Supported** `group2_i_then_q` for the documented complex16 capture contract | Source-validated experimental IWR6843ISK preset when that is the actual board; the capture record does not prove geometry |
| IWR6843ISK combinations outside that record | Source-validated experimental at xWR68xx family level only; no ISK identity is observed | Planned; no implicit relaxation of part/ES/asset checks | Source-validated experimental `group2_i_then_q` | Source-validated experimental IWR6843ISK preset |
| IWR6843 AOP | Planned; an AOP-specific platform alias and capture routing have not been established | Planned | Source-validated experimental `group2_i_then_q` only when the producer proves that layout | Source-validated experimental IWR6843 AOP preset |
| Other non-AOP xWR68xx parts or boards reporting exact `Platform: xWR68xx` | Source-validated experimental at family level only with the dedicated asset and closed two-lane contract; model, part, ES, board, and geometry are not observed | Planned | Source-validated experimental `group2_i_then_q` | Planned; caller must provide reviewed board geometry rather than select an ISK/AOP preset by family |
| xWR1843 standard EVM | Planned | Planned | Source-validated experimental `group2_i_then_q` | Source-validated experimental standard XWR1843 EVM preset |
| AWR1843 AOP | Planned | Planned | Source-validated experimental `group2_i_then_q` only when the producer proves that layout | Source-validated experimental AWR1843 AOP preset |
| xWR1642 | Planned | Planned | Source-validated experimental `group2_i_then_q` | Source-validated experimental XWR1642 preset |
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
| xWR1642 | 77 GHz scale; 76–81 GHz API range | 2 / 4 | 32 KiB | 2 | `group2_i_then_q` | xWR16xx MSS+BSS | reference script uses low-power ADC mode 1; mmwcli descriptor is not closed |
| xWR1843 | 77 GHz scale; 76–81 GHz API range | 3 / 4 | 32 KiB | 2 | `group2_i_then_q` | xWR18xx MSS+BSS | mmwcli boot/download and runtime descriptors are not closed |
| xWR6843 | 60 GHz scale; 57–64 GHz API range | 3 / 4 | 32 KiB | 2 | `group2_i_then_q` | xWR68xx MSS+BSS; xWR68xx `studio_cli` | Studio is family-level experimental with only `Platform: xWR68xx` observed; supported debug is restricted to IWR6843 ES2 part `0xE2` and the pinned assets |

Exact asset sizes and hashes, device-ID evidence, and the source paths used for this table are in
the [TI reference map](ti-reference-map.md). mmwcli does not ship those TI assets.

The named layouts cover legacy-frame, 16-bit complex ADC captures without LVDS headers. Real-only
ADC, complex 2x, CP/CQ or mixed payloads, LVDS headers, advanced frames, CSI-2, and cascade assembly
are not implied by the family row. A different data path needs a separately sourced descriptor and
golden fixture.

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
