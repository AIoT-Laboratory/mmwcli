# mmwcli

`mmwcli` 是 TI xWR68xx 与 DCA1000 的命令行工具，用于发送雷达配置、控制采集并保存
原始 ADC 数据。它不包含 GUI、MATLAB、Lua host、mmWave Studio 主机运行时或数据处理链。

## 支持范围

- 0.1 基线：xWR6843 ES2、单芯片、legacy frame、16-bit complex ADC、两路硬件 LVDS。
- Functional/application mode：通过 SDK demo CLI 或 TI `studio_cli` 设备固件的文本串口控制。
- Debug mode：在 SOP2 下下载用户提供的 MSS/BSS 固件，再通过 FTDI D2XX 与 mmWaveLink 控制。
- DCA1000 数据按字节偏移原样写入，不重排、解析或修补。
- Advanced frame、级联、LVDS header、软件 LVDS、RF monitor UART、CSI-2 和 TSW1400 暂不支持。

`studio_cli` 路线只需用户自行烧录 `mmwave_Studio_cli_xwr68xx.bin`；运行时不需要 Radar Toolbox
或 mmWave Studio。Debug 路线也不依赖 mmWave Studio 主机组件。

0.1 仅完成 Windows/amd64、FTDI D2XX 3.2.14、IWR6843 ES2 与 DCA1000 的 debug mode
实机验收。其它组合尚未经过实机验证，记录见
[硬件冒烟测试](docs/hardware-smoke-test.md#debug-mode-01-实机验收记录)。

## 下载

预编译文件与校验值见 [GitHub Releases](https://github.com/AIoT-Laboratory/mmwcli/releases/latest)。

## 构建

需要 Go 1.26 或更高版本。仓库没有第三方 Go 模块。

```sh
make check
make build
```

核心版本固定 `CGO_ENABLED=0`。Windows D2XX backend 加载系统安装的 `ftd2xx.dll`：

```powershell
$env:CGO_ENABLED = '0'
go build -trimpath -tags ftd2xx -o bin/mmwcli.exe ./cmd/mmwcli
Remove-Item Env:CGO_ENABLED
```

Linux D2XX backend 需要匹配目标架构的官方 `ftd2xx.h` 和 `libftd2xx.so`：

```sh
CGO_ENABLED=1 go build -trimpath -tags ftd2xx -o bin/mmwcli ./cmd/mmwcli
```

仓库与发布包不分发 FTDI 库、header、驱动或 TI 固件。

## 离线检查

```text
mmwcli doctor
mmwcli firmware verify PATH/mmwave_Studio_cli_xwr68xx.bin
mmwcli studio-cli check hardware/studio-cli-xwr6843-raw.cfg
mmwcli debug-capture check --bss-fw PATH/xwr68xx_radarss.bin --mss-fw PATH/xwr68xx_masterss.bin
mmwcli debug-capture native-check
```

这些命令不打开设备；`native-check` 只加载 D2XX 库。检查通过不代表硬件组合已经验证。

## REPL

```text
mmwcli repl --port PORT
```

`repl` 只接受通过 xWR68xx `version` 门禁并实现 TI `studio_cli` 行协议的固件。未知响应会关闭
会话，不自动重试。它允许发送兼容固件扩展的单行命令，但不声明该固件是受支持的采集后端。

## xWR6843 + DCA1000 快速开始

默认主机地址为 `192.168.33.30/24`，DCA1000 地址为 `192.168.33.180`，控制/数据端口为
UDP `4096/4098`。以下两种模式使用不同固件、端口和控制协议，不能混用。

### Functional/application mode

烧录 `mmwave_Studio_cli_xwr68xx.bin`，以正常 functional/application mode 启动雷达，并
显式提供该固件的 CLI UART。首次采集下发完整配置：

```text
mmwcli studio-cli capture hardware/studio-cli-xwr6843-raw.cfg capture-01.bin --port PORT
```

保持固件、SOP、CFG、串口和 DCA1000 连接不变，可检查无重配复用：

```text
mmwcli studio-cli capture hardware/studio-cli-xwr6843-raw.cfg capture-02.bin --port PORT --no-reconfig
```

两轮都不要使用 `--reset`。示例 CFG 每轮应产生 `26,214,400` bytes；文件大小必须精确
匹配，且没有 missing/discarded 数据或遗留 `.part`。此路线尚未完成实机验收。

### Debug mode

使用 `ftd2xx` 构建，准备 xWR68xx RF evaluation firmware 的 BSS/MSS 文件，并明确提供
Enhanced COM 与同一 FTDI 的 D2XX base：

```text
mmwcli debug-capture capture hardware/debug-capture-xwr6843-raw.cfg capture-debug.bin --enhanced-port PORT --bss-fw PATH/xwr68xx_radarss.bin --mss-fw PATH/xwr68xx_masterss.bin --d2xx-description AR-DevPack-EVM-012 --sop2-reset
```

`AR-DevPack-EVM-012` 是 0.1 实机验收使用的 description base；程序由它派生接口
`AR-DevPack-EVM-012 A/B`，启用 `--sop2-reset` 时还会派生 C/D。

也可按 serial number 选择设备。例如 D2XX 显示接口 serial 为 `FT1234A`、`FT1234B`、
`FT1234C`、`FT1234D`，参数应写作 `--d2xx-serial FT1234`。这是格式示例，不是 0.1
实机验收设备的 serial。description 与 serial 选择器不能同时使用。

Enhanced COM 只下载 MSS/BSS；随后 D2XX A/B 承载 mmWaveLink 控制，DCA1000 通过以太网
传输 ADC。`--sop2-reset` 使用 D/C 接口设置 SOP2 并复位雷达目标，与 DCA FPGA 的
`--reset` 无关。每次 debug capture 都重新下载固件并配置雷达，不支持 `--no-reconfig`。

不要在 SOP2 下使用 `studio-cli capture`，也不要把 Enhanced COM 当作文本 CLI UART。
TI 资产文件名与校验值见 [TI 资料地图](docs/ti-reference-map.md)。

## 运行语义

- CFG、模式、预期字节数和输出路径在硬件 I/O 前检查。
- Start 结果未知时不重试；失败路径执行有界清理。
- 有限采集必须精确匹配 CFG 推导的字节数。
- 数据先写入 `OUT.part`；全部成功后才无覆盖发布为 `OUT`，失败时保留 `.part`。
- 低层 `dca` 命令不得并行；`dca ping` 不是采集前置检查；reset 仅由显式命令或参数触发。
- `sensorStop` 只停止传感器，不会关闭雷达板或 RF 电源。

运行 `mmwcli help` 或子命令的 `--help` 查看命令与参数。设计细节见
[架构](docs/architecture.md)。

## 许可证

mmwcli 采用 [MIT License](LICENSE)。TI 与 FTDI 资产受各自条款约束，见
[第三方声明](THIRD_PARTY_NOTICES.md)。
