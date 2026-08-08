# TI assets and version map

mmwcli does not download or distribute TI assets. The functional/application text-CLI path only
requires the user-flashed `mmwave_Studio_cli_xwr68xx.bin`; debug mode uses RF evaluation MSS/BSS
firmware supplied explicitly by the user. Apart from the named firmware files, neither path
discovers a TI installation or invokes Toolbox or the mmWave Studio runtime.

This map also records source evidence for family descriptors that are experimental or planned.
A listed TI file or hash is not, by itself, a claim that mmwcli can acquire from that family. See
the separate [acquisition and decode matrix](hardware-support.md).

## `studio_cli` device firmware

The known 0.1 firmware comes from Radar Toolbox 4.00.00.05. It is the only functional/application
firmware in scope and closes a source-validated experimental xWR68xx family route. Runtime accepts
only the exact `Platform: xWR68xx` family response; it does not observe a model, part, ES, board, or
antenna geometry. The `mmwcli firmware verify FILE` command checks only this asset:

| Relative path | Bytes | SHA-256 |
| --- | ---: | --- |
| `tools/studio_cli/prebuilt_binaries/mmwave_Studio_cli_xwr68xx.bin` | 358660 | `24BDAE9662AA8E611DBEDAE65709B7589CDCFB6E3F71B8E0E7FA78C5DD4A18BF` |

The complete Toolbox, package metadata, manifests, profiles, and source are not verification or
capture prerequisites.

## `debug-capture` MSS/BSS firmware

The current executable offline asset contract comes from the mmWave Studio 2.1.1 RF evaluation
firmware. The user only needs to supply these two xWR68xx files; installing or invoking the
mmWave Studio runtime is unnecessary:

| Role and relative path | Bytes | SHA-256 |
| --- | ---: | --- |
| BSS `rf_eval_firmware/radarss/xwr68xx_radarss.bin` | 240072 | `E2C69405394E35BA376EFE1A52305EE74DBD19F8BAB72BD5A9078878853CD77F` |
| MSS `rf_eval_firmware/masterss/xwr68xx_masterss.bin` | 92992 | `316911D4A8DBA1762714A3A107071BD0CF06A135FAE29BFBBC92B037592DE060` |

`mmwcli debug-capture check --bss-fw FILE --mss-fw FILE` also parses RPRC, validates xWR68xx memory
windows, and builds the memory-write plan. It does not open the radar, DCA1000, or a USB device. A
successful command establishes only that the assets and write plan satisfy the offline contract; it
does not validate the D2XX library, radar, and DCA1000 combination.

During `debug-capture capture`, Enhanced COM only submits these files to SOP2 device memory. The host
then switches to FTDI D2XX A/B and carries mmWaveLink configuration, start, and stop over SPI/IRQ.
The host translates the capture CFG into mmWaveLink messages rather than sending it as text. No
mmWave Studio runtime is required. The two exact hashes in the table were used for the 2026-08-05
debug-mode hardware validation on IWR6843 ES2 part `0xE2`; no other firmware or device is covered.
See the complete combination and result in the
[hardware smoke test](hardware-smoke-test.md#debug-mode-01-hardware-validation-record).

### Reference-only first-generation assets

The same official mmWave Studio 2.1.1 installation contains family-specific RF-evaluation assets
for the earlier devices below. Their byte counts and SHA-256 values are recorded so community
reports can identify an exact input and future reviewed descriptors can be closed. Current mmwcli
does **not** accept, download, parse, or write these files, and their presence does not establish a
boot/download or capture route.

| Family and role | Relative path | Bytes | SHA-256 |
| --- | --- | ---: | --- |
| xWR12xx/xWR14xx BSS | `rf_eval_firmware/radarss/xwr12xx_xwr14xx_radarss.bin` | 35728 | `0B134A14D539292BB7E8E20C14676CEABAC2265D131C24F0526A21087ABCAD8C` |
| xWR12xx/xWR14xx MSS | `rf_eval_firmware/masterss/xwr12xx_xwr14xx_masterss.bin` | 92200 | `51522737ED3F0A62F2C3EC2F66B63E88E0256638A289AD4A0D8A55335EF8F1AE` |
| xWR16xx BSS | `rf_eval_firmware/radarss/xwr16xx_radarss.bin` | 35728 | `0B134A14D539292BB7E8E20C14676CEABAC2265D131C24F0526A21087ABCAD8C` |
| xWR16xx MSS | `rf_eval_firmware/masterss/xwr16xx_masterss.bin` | 52904 | `B4044513BA44C3290639AD4C416DAF37E72DEB43567FC0DE0314DAF2546130DF` |
| xWR18xx BSS | `rf_eval_firmware/radarss/xwr18xx_radarss.bin` | 35728 | `0B134A14D539292BB7E8E20C14676CEABAC2265D131C24F0526A21087ABCAD8C` |
| xWR18xx MSS | `rf_eval_firmware/masterss/xwr18xx_masterss.bin` | 52904 | `B4044513BA44C3290639AD4C416DAF37E72DEB43567FC0DE0314DAF2546130DF` |

Identical hashes in different family-named paths are recorded intentionally. They do not make the
families protocol-compatible: part/ES detection, Enhanced COM bootstrap, memory windows,
mmWaveLink configuration, lane mode, and runtime behavior must still be sourced and reviewed as a
complete descriptor.

## Development references

Protocol checks referred to these files under `tools/studio_cli`:

- `src/mss/mmw_cli.c` and `mss_main.c`: text commands and device state machine;
- `src/common/mmw_rfparser.c`: ADCBuf capacity calculations;
- `gui/mmw_cli_tool/mmw_main.c` and `serial_comm`: UART reference implementation;
- `gui/mmw_cli_tool/dca_comm/dca_control.c`: DCA1000 call order;
- `docs`: Studio CLI guide and release notes.

`tools/studio_cli/src/6843/studio_cli_xwr68xx.projectspec` declares this firmware build combination:

- mmWave SDK 3.5.0.01
- SYS/BIOS 6.73.1.01
- XDCtools 3.55.2.22_core
- ARM CGT 16.9.6.LTS

Different SDK versions are not assumed to be interchangeable build environments. The 0.1
functional/application asset contract pins the prebuilt firmware through the single-file check
above. That family-level route has offline validation only and no hardware-validation record; the
`6843` project path does not turn the family response into an observed IWR6843 model or ES claim.
AOP-specific aliases and non-`xWR68xx` platform values remain planned and are rejected. These
sources and build tools do not participate in mmwcli runtime operation.

## Multi-family TI source evidence

The following files in the official mmWave Studio 2.1.1 and mmWave SDK 3.6.2 LTS installations
were used as documentation evidence. They are not runtime dependencies and are not redistributed:

- `mmWaveStudio/Scripts/DataCaptureDemo_xWR.lua` reads the device ID from
  `0xFFFFE214[25:18]` and ES from `0xFFFFE218[3:0]`; maps xWR12 IDs `0x20`, `0x21`, `0x80`,
  xWR14 IDs `0xA0`, `0x40`, xWR16 IDs `0x60`, `0x61`, `0x04`, `0x62`, `0x67`, `0x66`, `0x01`,
  `0xC0`, `0xC1`, xWR18 IDs `0x70`, `0x71`, `0xD0`, `0x05`, and xWR68 IDs `0xE0` through `0xE4`;
  its ES1 branch additionally uses `0xFFFFE210[1:0]`.
- The same script selects two LVDS lanes and DCA1000 device mode 2 for xWR16/xWR18/xWR68, four
  lanes and device mode 1 for xWR12/xWR14, low-power ADC mode 1 for xWR1642, and a family-specific
  MSS/BSS pair. These values support closed family facts, not a generic runtime fallback. Lane
  enablement does not prove raw word interleave; audited xWR1443 sources conflict on that
  device-specific layout, so it remains planned.
- `packages/ti/control/mmwavelink/include/rl_sensor.h` documents the 77 GHz scale and 76–81 GHz
  API range for xWR1xxx, the 60 GHz scale and 57–64 GHz API range for xWR6843-class devices, and
  16 KiB ADCBuf for AWR1243/xWR1443 versus 32 KiB for xWR1642/xWR1843/xWR6843.
- `packages/ti/common/sys_common.h` fixes four RX channels. The xWR14/xWR16/xWR18/xWR68 family
  headers declare 3/2/3/3 TX antennas respectively and confirm the applicable ADCBuf sizes.
- `packages/ti/utils/sbl/platform/sbl_xwr16xx.c`, `sbl_xwr18xx.c`, and `sbl_xwr68xx.c` provide
  family-specific RPRC memory-window evidence. Similar window constants do not prove that the
  Enhanced COM bootstrap or download state machine is interchangeable.

No audited public source closed every boot/download/runtime field for xWR12/xWR14/xWR16/xWR18.
Accordingly, mmwcli acquisition for those families remains **Planned**. A future experimental
route must use a reviewed built-in descriptor, offline golden transactions, explicit user opt-in,
and fail-closed identity/ES/asset checks; it must not accept arbitrary register scripts.

## Current xWR68xx SDK references

The fixed xWR68xx capture limits and IWR6843 debug transport refer to:

- `packages/ti/common/sys_common_xwr68xx.h`: xWR68xx 32 KiB ADCBuf;
- `packages/ti/drivers/cbuff/include/cbuff_internal.h`: CBUFF constraints;
- `packages/ti/utils/sbl/include/image_parser.h`, `src/image_parser.c`, and
  `platform/sbl_xwr68xx.c`: RPRC format, padding, and xWR68xx memory windows;
- `packages/ti/control/mmwavelink`: transport-neutral mmWaveLink/RHCP protocol;
- `docs/mmwave_sdk_software_manifest.html`: version and license manifest.

## License scope

TI manifests and source files may use different licenses; one file does not establish the license
for the whole Toolbox or SDK. mmwcli reads a firmware file only when the user explicitly requests
verification. It does not read metadata, manifests, profiles, or reference source, and it does not
copy TI assets into the repository or release archives. See
[THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md).
