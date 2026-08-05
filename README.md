# mmwcli

`mmwcli` 是面向 TI xWR68xx 与 DCA1000 的跨平台命令行工具，用于发送雷达配置、控制
采集生命周期并保存原始 ADC 数据。它没有 GUI，不依赖 MATLAB、mmWave Studio 主机运行时、
C#/.NET、Lua host 或 TI 的 DCA1000 主机程序，也不包含 FFT、检测、可视化等数据处理链。
默认构建不使用 CGo；只有可选的 Linux D2XX backend 使用一个受 build tag 隔离的 CGo
链接层。

## 支持范围

0.1 的硬件基线是 xWR6843 ES2、单芯片、legacy frame、16-bit complex ADC 与两路硬件
LVDS。functional/application 路线使用 TI `studio_cli` 设备固件，SDK demo CLI 作为独立
方言支持。

- 核心版本使用同一套 Go 源码，发布目标为 Windows/Linux amd64 和 arm64，固定
  `CGO_ENABLED=0`。
- `debug-capture` 的可选原生 backend 使用用户安装的 FTDI D2XX；具体架构只有经过对应
  原生库与实机验证后才视为支持。
- DCA1000 数据按字节偏移写入，不重排、不解析、不修补缺失数据。
- Advanced frame、级联、LVDS header、软件 LVDS、RF monitor UART、CSI-2 和 TSW1400
  暂不支持。

文本 CLI 路线唯一需要的 TI 资产是用户自行烧录的
`mmwave_Studio_cli_xwr68xx.bin`。烧录后，正常配置与采集不需要固件路径，也不需要安装
Radar Toolbox 或 mmWave Studio。固件和 TI 文档不随本仓库分发。

SOP2 主机下载与直控路线统一命名为 `debug-capture`，并与上述文本 CLI 路线分离。可用
范围以 CLI 帮助中的实际命令为准。该路线使用用户显式提供的 MSS/BSS 固件和 FTDI D2XX，
不需要安装 mmWave Studio 主机运行时。

## 构建

需要 Go 1.26 或更高版本。仓库没有第三方 Go 模块。

在提供 POSIX `make` 的环境中，核心构建可直接运行：

```sh
make check
make build
```

这些目标固定使用 `CGO_ENABLED=0`；可选的原生 D2XX 变体仍按下面的平台命令构建。

Windows PowerShell：

```powershell
$env:CGO_ENABLED = '0'
go test ./...
go vet ./...
go build -trimpath -o bin/mmwcli.exe ./cmd/mmwcli
Remove-Item Env:CGO_ENABLED
```

Linux：

```sh
CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 go build -trimpath -o bin/mmwcli ./cmd/mmwcli
```

要启用 `debug-capture` 的 D2XX 原生边界，Windows 使用用户已安装到系统目录的
`ftd2xx.dll`，仍不需要 CGo：

```powershell
$env:CGO_ENABLED = '0'
go build -trimpath -tags ftd2xx -o bin/mmwcli.exe ./cmd/mmwcli
Remove-Item Env:CGO_ENABLED
```

Linux 需要用户先安装与目标架构匹配的官方 `ftd2xx.h` 和 `libftd2xx.so`，再构建唯一的
CGo 变体：

```sh
CGO_ENABLED=1 go build -trimpath -tags ftd2xx -o bin/mmwcli ./cmd/mmwcli
```

仓库不下载或分发 FTDI 库、header、驱动与安装程序，也不引入第三方 Go 模块。

## 离线检查

以下命令不打开串口，也不枚举或打开雷达、DCA1000、FTDI 设备：

```text
mmwcli doctor
mmwcli firmware verify PATH/mmwave_Studio_cli_xwr68xx.bin
mmwcli debug-capture check --bss-fw PATH/xwr68xx_radarss.bin --mss-fw PATH/xwr68xx_masterss.bin
mmwcli debug-capture native-check
mmwcli studio-cli check hardware/studio-cli-xwr6843-raw.cfg
```

`firmware verify` 只读取显式给出的文件，并按已知大小与 SHA-256 严格校验；它不查找或
校验 Toolbox metadata、profile、manifest。若不需要验证固件，`doctor` 无需任何 TI 路径。
`debug-capture check` 核对用户显式提供的 MSS/BSS 固件，解析 RPRC 并生成 xWR68xx
内存写计划，全程不访问硬件；通过只表示资产与下载计划有效，不代表 SOP2 下载、
mmWaveLink 控制或 ADC 采集已经通过实机验收。`debug-capture native-check` 只确认当前构建
的 D2XX 库边界可用；Windows 读取
库版本，Linux 当前不报告版本。该命令不查询或打开 USB 设备；未使用 `ftd2xx` build tag
的核心版本会明确报告 backend 不可用。

## 固件命令 REPL

`repl` 用于向实现 TI `studio_cli` 行协议的 xWR68xx 固件发送扩展命令：

```text
mmwcli repl --port PORT
```

连接后会先发送 `version`，只有收到包含 `Platform: xWR68xx` 且以 `Done` 结束的响应才进入
会话。输入按 CFG 的单行与注释规则处理；明确的数字 `Error <code>` 会报告并继续，超时、
取消或缺少 `Done`/`Error <code>` 的响应会立即关闭串口并终止，且不会发送下一条命令或
自动重试。该入口没有自定义方言或跳过平台门禁选项；通过门禁的固件可以扩展命令，但不会
因此成为受支持的配置或采集 backend。

## xWR6843 + DCA1000 快速开始

两条路线都通过 DCA1000 的以太网数据口接收原始 ADC。默认主机地址为
`192.168.33.30/24`，DCA 地址为 `192.168.33.180`，控制/数据端口为 UDP `4096/4098`。
根据雷达启动模式只选择下面一条流程；functional/application 与 debug 的端口、固件和
控制协议不能混用。

### Functional/application mode：设备内文本 CLI

1. 按 TI 板卡文档烧录 `mmwave_Studio_cli_xwr68xx.bin`，并让雷达从正常的
   functional/application 模式启动。已知校验值及来源见
   [TI 资料地图](docs/ti-reference-map.md)。
2. 明确确认设备固件的 CLI UART，并传给 `--port`。程序不会扫描或猜测串口。
3. 将 DCA1000 接到已配置上述静态地址的独立网卡。

首次采集下发完整配置：

```text
mmwcli studio-cli capture hardware/studio-cli-xwr6843-raw.cfg capture-01.bin --port PORT
```

保持固件、SOP、CFG、串口和 DCA1000 连接不变，再验证无重配复用：

```text
mmwcli studio-cli capture hardware/studio-cli-xwr6843-raw.cfg capture-02.bin --port PORT --no-reconfig
```

两轮都不要使用 `--reset`。第二轮仍解析 CFG 以建立完整性门槛，但不重发雷达配置，只使用
`sensorStart 0`；DCA1000 会重新 configure/start/stop，而不会 reset FPGA。

仓库示例配置产生 100 帧，每轮预期原始 payload 为 `26,214,400` bytes。只有两轮文件大小
都精确匹配、无 missing/discarded 数据且没有遗留 `.part`，才算通过该 functional/application
组合的复用验收。完整步骤见 [硬件冒烟测试](docs/hardware-smoke-test.md)。

### Debug mode：SOP2 主机下载与直控

1. 使用带 `ftd2xx` build tag 的构建，并按[构建](#构建)一节安装与目标架构匹配的 FTDI
   D2XX 驱动和原生库。
2. 显式确认 Enhanced COM 端口，以及同一 FTDI 的 D2XX A/B 接口所共有的 serial base 或
   description base。若板卡把 SOP/NRST 接到该 FTDI 的 C/D 接口，使用下面的
   `--sop2-reset` 让程序设置 SOP2 并脉冲一次目标复位；否则按板卡文档手工进入 SOP2，省略
   该参数。程序不会枚举或猜测设备。
3. 从自己的 TI 安装中取得 xWR68xx RF evaluation firmware 的 BSS 与 MSS 文件；已知文件名、
   大小和校验值见 [TI 资料地图](docs/ti-reference-map.md)。
4. 将 DCA1000 接到已配置上述静态地址的独立网卡，然后执行：

```text
mmwcli debug-capture capture hardware/debug-capture-xwr6843-raw.cfg capture-debug.bin --enhanced-port PORT --bss-fw PATH/xwr68xx_radarss.bin --mss-fw PATH/xwr68xx_masterss.bin --d2xx-serial BASE --sop2-reset
```

若设备使用 description 标识，以 `--d2xx-description BASE` 替代 `--d2xx-serial BASE`；两者
不能同时使用。`--sop2-reset` 是显式的雷达目标复位，只在 Enhanced COM 之前使用同一
base 派生的 D/C 接口各执行一次 SOP2/NRST 流程；它与控制 DCA FPGA 的 `--reset` 无关。
Enhanced COM 只负责将 BSS/MSS 固件提交到内存，随后 D2XX A/B 承载
mmWaveLink 配置、启动和停止，DCA1000 仍通过以太网传输 ADC 数据。CFG 在主机端严格预检
并翻译成 mmWaveLink 消息，不会作为文本发送给固件。

`debug-capture capture` 不支持 `--no-reconfig`；每次采集都执行完整的固件下载和雷达配置。
不要在 SOP2 下使用 `studio-cli capture`，也不要把 Enhanced COM 当作设备固件的 CLI UART。
0.1 已完成 Windows/amd64、FTDI D2XX 3.2.14、IWR6843 ES2 与 DCA1000 的这一条
debug mode 实机验收。functional/application、SDK demo、Linux D2XX、arm64 及其它固件或
配置尚未经过实机验证；完整组合与结果见[硬件冒烟测试](docs/hardware-smoke-test.md#debug-mode-01-实机验收记录)。

## CLI 概览

| 命令 | 用途 |
| --- | --- |
| `version` | 显示 mmwcli 版本与目标平台 |
| `doctor` | 离线检查平台；可选校验 `studio_cli` 固件 |
| `firmware verify FILE` | 严格校验单个 `studio_cli` 固件文件 |
| `debug-capture check` | 离线校验直控路线所需的 MSS/BSS 固件 |
| `debug-capture native-check` | 只检查可选 D2XX 动态库，不访问设备 |
| `debug-capture capture` | SOP2 下载 MSS/BSS，并通过 D2XX/mmWaveLink 配置与采集 |
| `repl --port PORT` | 发送符合 `studio_cli` 行协议的单行固件命令 |
| `studio-cli check` | 离线预检 `studio_cli` CFG |
| `studio-cli version\|apply\|start\|stop\|capture` | 控制 `studio_cli` 固件 |
| `demo check\|apply\|start\|stop\|capture` | 控制 SDK demo 固件 |
| `dca version\|ping\|configure\|start\|stop\|capture` | 独立 DCA1000 诊断与编排 |
| `dca reset-fpga\|reset-radar` | 显式恢复操作 |

运行 `mmwcli help` 或相应命令的 `--help` 查看参数。`studio-cli` 默认 921600 baud，SDK
demo 默认 115200 baud；两种固件的命令与启动语义不能混用。

低层 DCA 命令具有硬件副作用。不要并行执行它们，也不要把 `dca ping` 当作正常采集的
readiness 门槛；非录制状态下 `SystemAlive` 可能无响应，而 `dca version` 仍可工作。

## 数据与失败语义

- CFG、模式组合、预期字节数和输出路径均在硬件 I/O 前检查。
- 一体化采集只在 DCA StartRecord 成功后发送一次 `sensorStart`；未知结果不会自动重试。
- 正常完成的有限 debug capture 会验证固件自然产生的 frame-end 事件，不再重复发送 stop；
  functional/application 路线以及取消、超时和失败路径仍按
  `radar stop -> bounded drain -> StopRecord` 执行有界清理。
- 数据先独占写入 `OUT.part`。只有采集、完整性检查和清理全部成功才无覆盖发布为 `OUT`；
  失败保留 `.part` 供诊断。
- DCA1000 raw 短尾包可能延迟约 2 秒，因此默认 quiet window 为 2500 ms。
- `sensorStop` 只停止传感器/帧，不会给雷达板或 RF 电源域断电。

设计细节见 [架构](docs/architecture.md)，TI 资产与版本依据见
[TI 资料地图](docs/ti-reference-map.md)。

## 许可证

mmwcli 采用 [MIT License](LICENSE)。TI 固件、工具、文档和商标仍受各自条款约束，详见
[第三方声明](THIRD_PARTY_NOTICES.md)。
