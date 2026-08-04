# mmwcli

`mmwcli` 是面向 TI xWR68xx 与 DCA1000 的跨平台命令行工具，用于发送雷达配置、控制
采集生命周期并保存原始 ADC 数据。它没有 GUI，不依赖 MATLAB、mmWave Studio 主机运行时、
C#/.NET、CGo、Lua host 或 TI 的 DCA1000 主机程序，也不包含 FFT、检测、可视化等数据
处理链。

## 支持范围

0.1 的硬件基线是 xWR6843 ES2、单芯片、legacy frame、16-bit complex ADC 与两路硬件
LVDS。functional/application 路线使用 TI `studio_cli` 设备固件，SDK demo CLI 作为独立
方言支持。

- Windows 与 Linux 使用同一套 Go 源码；发布目标为 amd64 和 arm64。
- PC 端只使用标准库，正式构建固定 `CGO_ENABLED=0`。
- DCA1000 数据按字节偏移写入，不重排、不解析、不修补缺失数据。
- Advanced frame、级联、LVDS header、软件 LVDS、RF monitor UART、CSI-2 和 TSW1400
  暂不支持。

这条路线唯一涉及的 Toolbox 资产是用户自行烧录的
`mmwave_Studio_cli_xwr68xx.bin`。烧录后，正常配置与采集不需要固件路径，也不需要安装
Radar Toolbox 或 mmWave Studio。固件和 TI 文档不随本仓库分发。

SOP2 主机下载与直控路线统一命名为 `debug-capture`，并与上述文本 CLI 路线分离。可用
范围以 CLI 帮助中的实际命令为准。

## 构建

需要 Go 1.26 或更高版本。仓库没有第三方 Go 模块。

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

## 离线检查

以下命令不打开串口，也不访问 DCA1000：

```text
mmwcli doctor
mmwcli firmware verify PATH/mmwave_Studio_cli_xwr68xx.bin
mmwcli studio-cli check hardware/studio-cli-xwr6843-raw.cfg
```

`firmware verify` 只读取显式给出的文件，并按已知大小与 SHA-256 严格校验；它不查找或
校验 Toolbox metadata、profile、manifest。若不需要验证固件，`doctor` 无需任何 TI 路径。

## xWR6843 + DCA1000 快速开始

1. 按 TI 板卡文档烧录 `mmwave_Studio_cli_xwr68xx.bin`。已知校验值及来源见
   [TI 资料地图](docs/ti-reference-map.md)。
2. 让雷达从 functional/application 模式启动；SOP2 主机下载模式不能用于文本 CLI 采集。
3. 将 DCA1000 接到独立网卡。默认主机地址为 `192.168.33.30/24`，DCA 地址为
   `192.168.33.180`，控制/数据端口为 UDP `4096/4098`。
4. 明确确认 CLI 串口并传给 `--port`。程序不会扫描或猜测端口。

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

仓库示例配置产生 100 帧，每轮预期原始 payload 为 `26,214,400` bytes。只有两轮文件
大小都精确匹配、无 missing/discarded 数据且没有遗留 `.part`，才算通过该硬件组合的
复用验收。完整步骤见 [硬件冒烟测试](docs/hardware-smoke-test.md)。

## CLI 概览

| 命令 | 用途 |
| --- | --- |
| `version` | 显示 mmwcli 版本与目标平台 |
| `doctor` | 离线检查平台；可选校验 `studio_cli` 固件 |
| `firmware verify FILE` | 严格校验单个 `studio_cli` 固件文件 |
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
- 清理顺序为 `sensorStop -> bounded drain -> StopRecord`，Ctrl+C 和 `SIGTERM` 也会执行
  有界清理。
- 数据先独占写入 `OUT.part`。只有采集、完整性检查和清理全部成功才无覆盖发布为 `OUT`；
  失败保留 `.part` 供诊断。
- DCA1000 raw 短尾包可能延迟约 2 秒，因此默认 quiet window 为 2500 ms。
- `sensorStop` 只停止传感器/帧，不会给雷达板或 RF 电源域断电。

设计细节见 [架构](docs/architecture.md)，TI 资产与版本依据见
[TI 资料地图](docs/ti-reference-map.md)。

## 许可证

mmwcli 采用 [MIT License](LICENSE)。TI 固件、工具、文档和商标仍受各自条款约束，详见
[第三方声明](THIRD_PARTY_NOTICES.md)。
