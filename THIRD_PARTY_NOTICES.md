# 第三方声明

mmwcli 自身采用 MIT License，见 [LICENSE](LICENSE)。主机端只使用 Go 标准库，`go.mod`
不包含第三方模块。

项目实现参考了 Texas Instruments 提供的 xWR68xx、Radar Toolbox Studio CLI 与 DCA1000
协议资料。TI 固件、主机工具、参考源码、profiles、manifest 和文档不属于本项目的 MIT
授权范围，也不随仓库或发布包分发。使用者应从自己的合法 TI 安装中取得这些资产，并遵守
每个文件及对应 manifest 的许可条款。

`studio_cli` 与 `mmwave_Studio_cli_xwr68xx.bin` 是 TI Radar Toolbox 中的原始资产名；
它们不表示 mmwcli 依赖桌面版 mmWave Studio。仓库中的
`hardware/toolbox-xwr6843-raw.cfg` 是本项目的 raw capture 验收配置，不是 TI 官方
monitor profile。

Texas Instruments、TI、mmWave 及相关产品名称和商标归其各自权利人所有。本项目与
Texas Instruments 无隶属或背书关系。
