# TI 资料与版本地图

mmwcli 不下载或分发 TI 资产。文本 CLI 路线唯一需要的是用户自行烧录的
`mmwave_Studio_cli_xwr68xx.bin`；运行时不读取 Toolbox 安装。

## `studio_cli` 设备固件

0.1 已知固件来自 Radar Toolbox 4.00.00.05。`mmwcli firmware verify FILE` 只校验以下
资产：

| 相对路径 | 字节数 | SHA-256 |
| --- | ---: | --- |
| `tools/studio_cli/prebuilt_binaries/mmwave_Studio_cli_xwr68xx.bin` | 358660 | `24BDAE9662AA8E611DBEDAE65709B7589CDCFB6E3F71B8E0E7FA78C5DD4A18BF` |

完整 Toolbox、package metadata、manifest、profile 和源码都不是校验或采集前置条件。

## `debug-capture` MSS/BSS 固件

离线资产合同来自 mmWave Studio 2.1.1 的 RF evaluation firmware。只需用户显式提供下列
两个文件，不需要安装或调用 mmWave Studio runtime：

| 角色与相对路径 | 字节数 | SHA-256 |
| --- | ---: | --- |
| BSS `rf_eval_firmware/radarss/xwr68xx_radarss.bin` | 240072 | `E2C69405394E35BA376EFE1A52305EE74DBD19F8BAB72BD5A9078878853CD77F` |
| MSS `rf_eval_firmware/masterss/xwr68xx_masterss.bin` | 92992 | `316911D4A8DBA1762714A3A107071BD0CF06A135FAE29BFBBC92B037592DE060` |

`mmwcli debug-capture check --bss-fw FILE --mss-fw FILE` 还会解析 RPRC、检查 xWR68xx
内存窗口并生成内存写计划；它不会打开雷达、DCA1000 或 USB 设备。命令通过不代表
直控采集链已经实现。

## 开发参考

协议核对曾参考 `tools/studio_cli` 中的下列资料：

- `src/mss/mmw_cli.c`、`mss_main.c`：文本命令和设备状态机；
- `src/common/mmw_rfparser.c`：ADCBuf 容量计算；
- `gui/mmw_cli_tool/mmw_main.c` 与 `serial_comm`：UART 参考实现；
- `gui/mmw_cli_tool/dca_comm/dca_control.c`：DCA1000 调用顺序；
- `docs`：Studio CLI 指南与发布说明。

`tools/studio_cli/src/6843/studio_cli_xwr68xx.projectspec` 指定该固件的构建组合：

- mmWave SDK 3.5.0.01
- SYS/BIOS 6.73.1.01
- XDCtools 3.55.2.22_core
- ARM CGT 16.9.6.LTS

不同版本的 SDK 不能视为可直接替换的等价构建环境。0.1 验收使用通过上述单文件校验的
预编译固件；这些源码与构建工具不参与 mmwcli 运行。

## mmWave SDK 3.6.2 参考

SDK demo 方言的主要参考路径为：

- `packages/ti/demo/xwr68xx/mmw/mss/mmw_cli.c`：115200 baud CLI 与启动语义；
- `packages/ti/demo/xwr68xx/mmw/profiles`：官方 demo profiles；
- `packages/ti/common/sys_common_xwr68xx.h`：xWR68xx 32 KiB ADCBuf；
- `packages/ti/drivers/cbuff/include/cbuff_internal.h`：CBUFF 约束；
- `packages/ti/utils/sbl/include/image_parser.h`、`src/image_parser.c` 与
  `platform/sbl_xwr68xx.c`：RPRC 格式、补齐和 xWR68xx 内存窗口；
- `packages/ti/control/mmwavelink`：transport-neutral mmWaveLink/RHCP 协议；
- `docs/mmwave_sdk_software_manifest.html`：版本和许可清单。

普通 demo profile 不一定开启硬件 LVDS。用于 DCA1000 capture 的配置必须显式满足
mmwcli 的 raw-only 预检。

## 许可边界

TI manifest 和源文件可能包含不同许可条款，不能从单个文件推断整个 Toolbox 或 SDK 的
许可证。mmwcli 只在用户显式要求校验时读取固件文件，不读取 metadata、manifest、profile
或参考源码，也不将 TI 资产复制到仓库或发布包。详见
[THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md)。
