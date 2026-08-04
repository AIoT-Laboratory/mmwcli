# TI 资料与版本地图

mmwcli 不下载或分发 TI 资产。`--toolbox-root` 与 `MMWCLI_RADAR_TOOLBOX_ROOT` 接受用户
自己的 Radar Toolbox 安装根目录；文中路径均相对于该根目录或对应 SDK 根目录。

## Radar Toolbox 4.00.00.05

xWR6843 主线参考位于 `tools/studio_cli`：

- `prebuilt_binaries/mmwave_Studio_cli_xwr68xx.bin`：xWR6843 ES2 设备固件；
- `src/mss/mmw_cli.c`、`mss_main.c`：文本命令和设备状态机；
- `src/common/mmw_rfparser.c`：ADCBuf 容量计算；
- `gui/mmw_cli_tool/mmw_main.c` 与 `serial_comm`：UART 参考实现；
- `gui/mmw_cli_tool/dca_comm/dca_control.c`：DCA1000 调用顺序；
- `docs`：Studio CLI 指南与发布说明。

`mmwcli toolbox verify` 校验 package metadata 以及以下已知资产：

| 相对路径 | 字节数 | SHA-256 |
| --- | ---: | --- |
| `tools/studio_cli/prebuilt_binaries/mmwave_Studio_cli_xwr68xx.bin` | 358660 | `24BDAE9662AA8E611DBEDAE65709B7589CDCFB6E3F71B8E0E7FA78C5DD4A18BF` |
| `tools/studio_cli/src/profiles/profile_monitor_xwr68xx.cfg` | 1409 | `169C070C3F7E18D9E851BB272D13D6CC4E5211A2C9302BBBF91D5B3393D6A35A` |
| `toolbox_docs/RADAR_TOOLBOX_manifest.html` | 179989 | `5683D43FB3A272DA3CB16F0FC8E1795F50751401D6544F956205F101AD0091D4` |

`profile_monitor_xwr68xx.cfg` 只用于识别安装。它会产生 RF monitor UART 报告，不能作为
mmwcli 的 raw-only capture 配置。

`tools/studio_cli/src/6843/studio_cli_xwr68xx.projectspec` 指定该固件的构建组合：

- mmWave SDK 3.5.0.01
- SYS/BIOS 6.73.1.01
- XDCtools 3.55.2.22_core
- ARM CGT 16.9.6.LTS

不同版本的 SDK 不能视为可直接替换的等价构建环境。0.1 验收使用 Toolbox 提供且通过
上述校验的预编译固件。

## mmWave SDK 3.6.2 参考

SDK demo 方言的主要参考路径为：

- `packages/ti/demo/xwr68xx/mmw/mss/mmw_cli.c`：115200 baud CLI 与启动语义；
- `packages/ti/demo/xwr68xx/mmw/profiles`：官方 demo profiles；
- `packages/ti/common/sys_common_xwr68xx.h`：xWR68xx 32 KiB ADCBuf；
- `packages/ti/drivers/cbuff/include/cbuff_internal.h`：CBUFF 约束；
- `docs/mmwave_sdk_software_manifest.html`：版本和许可清单。

普通 demo profile 不一定开启硬件 LVDS。用于 DCA1000 capture 的配置必须显式满足
mmwcli 的 raw-only 预检。

## 许可边界

TI manifest 和源文件可能包含不同许可条款，不能从单个文件推断整个 Toolbox 或 SDK 的
许可证。mmwcli 只读取用户安装中的 metadata、manifest、固件和 profile，不将它们复制到
仓库或发布包。详见 [THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md)。
