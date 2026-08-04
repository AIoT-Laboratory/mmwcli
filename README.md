# mmwcli

`mmwcli` 是一个面向 TI xWR68xx 的无界面控制工具。首期目标不是复刻 mmWave Studio 的界面或后处理，而是把设备控制面拆成可测试、可复用的组件：

- 向已烧录 mmWave SDK 应用的 CLI 串口发送 `.cfg`；
- 启动、停止 68xx 传感器；
- 在不启动 mmWave Studio GUI 的情况下运行常见 TI Lua 脚本；
- 独立控制并重复使用 DCA1000，保存原始 ADC 字节流，不做 MATLAB/FFT/检测处理。

当前实现是 Windows/x86 的第一个可运行里程碑。TI 的 `AR1xController`、`RadarLinkDLL`、固件和 Lua 运行库不会被复制进仓库；运行 Studio 兼容脚本时从本机 mmWave Studio 2.1.1 安装目录动态加载。DCA1000 命令与录制由本项目直接实现，不依赖 Studio GUI。

## 构建

在 Windows PowerShell 中运行：

```powershell
.\build.ps1
```

产物位于 `bin\Debug\mmwcli.exe`。构建只使用 Windows 自带的 .NET Framework 编译器，不下载 NuGet 包。

先运行环境检查与离线测试：

```powershell
.\bin\Debug\mmwcli.exe doctor
.\bin\Debug\mmwcli.exe self-test
```

## 常用命令

推荐用一条命令完成雷达配置和 DCA1000 原始数据采集。烧录 Radar Toolbox 的
`mmwave_Studio_cli_xwr68xx.bin` 后，使用 `studio-cli` 入口（默认 921600）：

```powershell
mmwcli studio-cli capture profile_monitor_xwr68xx.cfg capture.bin --port COM3
```

设备运行 mmWave SDK demo 或兼容应用时，使用 `demo` 入口（默认 115200）：

```powershell
mmwcli demo capture profile_raw_68xx.cfg capture.bin --port COM3
```

两条 `capture` 命令使用相同的生命周期：幂等停止传感器，确认 DCA1000 已停止并重新
配置，再下发雷达配置；收到 DCA1000 的 StartRecord 应答后才发送 `sensorStart`。
`studio-cli capture` 会先用
`version` 响应确认 xWR68xx；标准 SDK demo 没有这条命令，因此 `demo capture` 无法自动
识别器件，调用者必须保证端口连接的是首期范围内的 68xx 固件。有限帧数据静默后，或用户
按下 Ctrl+C 后，会话会依次停止雷达、接收尾包、停止 DCA1000，并刷新和关闭输出文件。
不要用任务管理器或直接拔线代替 Ctrl+C，否则无法保证清理命令送达。

一体化采集先写入同目录的 `OUT.part`，只有主流程及雷达/DCA 停止都成功后才原子改名为
`OUT`。为避免覆盖证据，启动时只要 `OUT` 或 `OUT.part` 已存在就会在访问硬件前拒绝；
失败和 Ctrl+C 取消时，已经创建的 `.part` 会保留供检查，重新运行前应先明确归档或移走。
一体化采集还会按 DCA 字节偏移核对覆盖区间：迟到包补齐空洞后可以发布，未修复空洞或
被丢弃的前缀包则只保留 `.part`。
独立的 `dca capture` 保持其原有文件语义，不使用这一发布协议。

一体化采集的 DCA 默认值面向 68xx：两路 LVDS、16 位数据、`25 us` packet delay，网络为
`192.168.33.30 -> 192.168.33.180`，配置/数据端口为 `4096/4098`。默认不会 reset FPGA，
因此同一块 DCA1000 可连续采集；只有明确需要恢复卡状态时才传 `--reset`。常用调整项：

```powershell
mmwcli studio-cli capture profile.cfg capture.bin --port COM3 `
  --delay-us 25 --start-timeout-ms 30000 --idle-ms 1500
```

`--start-timeout-ms` 是等待首个 ADC UDP 包的时间，`--idle-ms` 是收到数据后判定流结束的
静默时间。若该值不足以跨越 `frameCfg` 帧周期，程序会自动提高到“帧周期 + 安全余量”；
仍可显式设得更大。有限帧若明显早于 `(帧数 - 1) × 帧周期` 就静默（判定保留最多
25 ms 且小于半帧的调度容差），会被报告为不完整采集并保留 `.part`。无限帧配置需要
Ctrl+C 结束；若意外数据静默，程序会按链路异常失败并保留 `.part`。有限帧若超过按帧数、
周期和 idle 推导的绝对上限仍持续来包，也会失败清理而不是无限等待。`capture` 为串口和 DCA 分别使用
`--serial-timeout-ms` 与 `--dca-timeout-ms`，不接受含义不清的 `--timeout-ms`。

一体化采集的首期 CFG 契约是 `dfeDataOutputMode 1`、16-bit complex
`adcCfg 2 1`/`adcCfg 2 2`、恰好一个 `frameCfg` 且 `triggerSelect=1`，以及硬件 ADC LVDS 配置
`lvdsStreamCfg -1 0 1 0`；Advanced frame、LVDS header 和软件 LVDS 均不支持。
`adcCfg` 位宽还必须与 DCA `--data-format` 一致。配置末尾已有精确小写的 `sensorStart`
时，会话会先将它分离，等 DCA1000 armed 后再发送；`studio-cli capture` 也接受
`sensorStart 0`，但重新下发完整配置的 `demo capture` 不接受。没有 start 时会自动补发
`sensorStart`。重复的 start，或它之后仍有有效命令，都会在接触硬件前被拒绝。

以下命令保留用于只控制文本 CLI，不负责 DCA1000 采集：

```powershell
mmwcli demo apply profile.cfg --port COM3
mmwcli demo start --port COM3
mmwcli demo stop --port COM3
mmwcli demo start --no-reconfig --port COM3
mmwcli studio-cli apply profile_monitor_xwr68xx.cfg --port COM3
mmwcli studio-cli start --port COM3
mmwcli studio-cli start --no-reconfig --port COM3
mmwcli studio-cli stop --port COM3
```

普通 mmWave SDK demo 通常使用 `115200`。Radar Toolbox `studio_cli` 68xx 固件使用
独立入口，默认 `921600`；波特率最终应以目标固件为准。对 SDK 3.6 的 xWR68xx demo，
`sensorStop` 后沿用已加载配置重启时必须使用 `demo start --no-reconfig`（发送
`sensorStart 0`）；完整配置刚重新下发后才使用无该选项的 `demo start`。

无 GUI 运行 TI Lua：

```powershell
mmwcli studio lua D:\path\to\xwr68xx.lua
mmwcli studio eval "return ar1.IsConnected()"
```

若未自动定位安装目录，可传入 `--studio-root`，其值可以是 `mmwave_studio_*` 安装目录或其中的 `mmWaveStudio` 目录：

```powershell
mmwcli studio lua script.lua --studio-root D:\Apps\ti\mmwave_studio_02_01_01_00
```

DCA1000 独立命令保留作连通性诊断、恢复和高级手工编排；日常采集优先使用上面的
`demo|studio-cli capture`：

```powershell
mmwcli dca ping
mmwcli dca version
mmwcli dca configure --no-reset --delay-us 25
mmwcli dca capture adc_data.bin --idle-ms 1500
mmwcli dca stop
```

独立的 `dca capture` 会配置 DCA FPGA/packet、发送 StartRecord 并接收网络数据，但不会
配置或启动雷达；它不再是硬件冒烟测试的主流程。数据按 DCA1000 的 48 位字节偏移写入，
丢包形成的空洞会保持为零，迟到包仍可
回填。该诊断入口不采用一体化会话的 `.part`、完整性及 fatal-async 发布门槛。所有
capture 路径都不进行 ADC 重排、FFT 或任何信号处理。

## 支持边界

首期收敛到 xWR68xx，硬件验收基线是 xWR6843 ES2 单芯片、两路硬件 LVDS 和 legacy
frame。Advanced frame、软件 LVDS、级联雷达、TSW1400、CSI-2、GUI API、MATLAB
后处理和监控分析 API 不在首期支持范围。TI Lua 路径继续兼容常见 `ar1` 配置、固件
加载、帧启动/停止和 DCA1000 API，但仍需按具体 Studio/固件版本做实机验证。

架构和兼容策略见 [docs/architecture.md](docs/architecture.md)，本机已核对的 TI 资料和
版本边界见 [docs/ti-reference-map.md](docs/ti-reference-map.md)，接上设备后的验证顺序见
[docs/hardware-smoke-test.md](docs/hardware-smoke-test.md)。
