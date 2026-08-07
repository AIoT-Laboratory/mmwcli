# TI assets and version map

mmwcli does not download or distribute TI assets. The functional/application text-CLI path only
requires the user-flashed `mmwave_Studio_cli_xwr68xx.bin`; debug mode uses RF evaluation MSS/BSS
firmware supplied explicitly by the user. Apart from the named firmware files, neither path
discovers a TI installation or invokes Toolbox or the mmWave Studio runtime.

## `studio_cli` device firmware

The known 0.1 firmware comes from Radar Toolbox 4.00.00.05. `mmwcli firmware verify FILE` checks only
this asset:

| Relative path | Bytes | SHA-256 |
| --- | ---: | --- |
| `tools/studio_cli/prebuilt_binaries/mmwave_Studio_cli_xwr68xx.bin` | 358660 | `24BDAE9662AA8E611DBEDAE65709B7589CDCFB6E3F71B8E0E7FA78C5DD4A18BF` |

The complete Toolbox, package metadata, manifests, profiles, and source are not verification or
capture prerequisites.

## `debug-capture` MSS/BSS firmware

The offline asset contract comes from the mmWave Studio 2.1.1 RF evaluation firmware. The user only
needs to supply these two files; installing or invoking the mmWave Studio runtime is unnecessary:

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
debug-mode hardware validation; no other firmware version is covered. See the complete combination
and result in the
[hardware smoke test](hardware-smoke-test.md#debug-mode-01-hardware-validation-record).

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
above. That path currently has offline validation only and no hardware-validation record. These
sources and build tools do not participate in mmwcli runtime operation.

## mmWave SDK 3.6.2 references

The SDK demo dialect primarily refers to:

- `packages/ti/demo/xwr68xx/mmw/mss/mmw_cli.c`: 115200-baud CLI and start semantics;
- `packages/ti/demo/xwr68xx/mmw/profiles`: official demo profiles;
- `packages/ti/common/sys_common_xwr68xx.h`: xWR68xx 32 KiB ADCBuf;
- `packages/ti/drivers/cbuff/include/cbuff_internal.h`: CBUFF constraints;
- `packages/ti/utils/sbl/include/image_parser.h`, `src/image_parser.c`, and
  `platform/sbl_xwr68xx.c`: RPRC format, padding, and xWR68xx memory windows;
- `packages/ti/control/mmwavelink`: transport-neutral mmWaveLink/RHCP protocol;
- `docs/mmwave_sdk_software_manifest.html`: version and license manifest.

An ordinary demo profile does not necessarily enable hardware LVDS. A configuration used for
DCA1000 capture must explicitly satisfy mmwcli's raw-only preflight.

## License scope

TI manifests and source files may use different licenses; one file does not establish the license
for the whole Toolbox or SDK. mmwcli reads a firmware file only when the user explicitly requests
verification. It does not read metadata, manifests, profiles, or reference source, and it does not
copy TI assets into the repository or release archives. See
[THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md).
