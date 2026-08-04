# 第三方声明

mmwcli 自身采用 MIT License，见 [LICENSE](LICENSE)。`go.mod` 不包含第三方模块。核心版本
只使用 Go 标准库；可选原生 backend 在 Windows 运行时加载用户安装的 FTDI D2XX，在
Linux 构建、链接和运行时使用用户安装的 D2XX。

项目实现参考了 Texas Instruments 提供的 xWR68xx、Radar Toolbox Studio CLI 与 DCA1000
协议资料。TI 固件、主机工具、参考源码、profiles、manifest 和文档不属于本项目的 MIT
授权范围，也不随仓库或发布包分发。使用者应从自己的合法 TI 安装中取得这些资产，并遵守
每个文件及对应 manifest 的许可条款。

`studio_cli` 与 `mmwave_Studio_cli_xwr68xx.bin` 是 TI Radar Toolbox 中的原始资产名。
functional 路线只要求用户自行烧录这个设备固件；完整 Toolbox、参考源码、profiles、
manifest 和主机工具都不是构建或运行依赖。仓库中的
`hardware/studio-cli-xwr6843-raw.cfg` 是本项目的 raw capture 验收配置，不是 TI 官方
monitor profile。`debug-capture` 校验的 MSS/BSS 固件也必须由用户自行提供。

## FTDI D2XX

`debug-capture` 的可选 `ftd2xx` build tag 只绑定显式打开 A/B 接口、MPSSE 初始化与
SPI/IRQ transport 所需的最小 D2XX ABI。仓库与发布包不包含 FTDI library、header、driver
或 installer；Linux 构建时使用的官方 `ftd2xx.h` 与 `libftd2xx.so` 也必须由用户提供。
这些文件不属于本项目的 MIT 授权范围。使用者应从
[FTDI D2XX 官方下载页](https://ftdichip.com/drivers/d2xx-drivers/) 取得与操作系统及架构匹配
的版本，并遵守其中随附的许可条款。API 定义见
[FTDI D2XX Programmer's Guide](https://ftdichip.com/wp-content/uploads/2023/09/D2XX_Programmers_Guide.pdf)。

Texas Instruments、TI、mmWave 及相关产品名称和商标归其各自权利人所有。本项目与
Texas Instruments 无隶属或背书关系。
