# 架构与兼容边界

## 三种 68xx 控制模式

TI 生态中存在三条不同的控制路径，不能混为一种串口协议：

1. **SDK 应用 CLI 模式**：68xx 已烧录 mmWave SDK demo 或兼容固件，PC 通过用户串口
   发送 `profileCfg`、`frameCfg`、`sensorStart`、`sensorStop` 等文本命令，常见波特率为
   115200。`mmwcli demo ...` 负责这条路径。
2. **Radar Toolbox `studio_cli` 固件模式**：TI 在 `tools/studio_cli` 中提供 xWR68xx
   预编译固件、设备端源码和 C 主机参考实现。它也使用文本配置，但 68xx 默认波特率为
   921600，并专门面向 Studio 风格配置和 DCA1000 采集。`mmwcli studio-cli ...` 负责
   这条路径，也是下一阶段摆脱闭源 Studio 运行库的首选 68xx 参考后端。
3. **Studio/SOP2 开发模式**：设备处于 SOP2，由主机下载 BSS/MSS 固件并通过 RadarLink
   配置 RF。TI Lua 中的 `ar1.Connect`、`DownloadBSSFw`、`RfInit`、`ProfileConfig`、
   `StartFrame` 属于这条路径。`mmwcli studio ...` 当前负责这条路径。

## 当前分层

```text
CLI
 |
 +-- demo / studio-cli
 |     +-- DemoCli -------------------- 115200 / 921600 文本串口
 |     +-- TextCliCaptureSession ------ 雷达 + DCA 一体化生命周期
 |             +-- Dca1000Client ------ UDP 控制命令
 |             +-- Dca1000Capture ----- UDP 原始数据与文件
 |
 +-- studio
 |     +-- StudioLuaHost -------------- 无 GUI Lua 5.1 + ar1 兼容层
 |             +-- TI AR1xController/RadarLinkDLL（运行时动态加载）
 |
 +-- dca ------------------------------ 独立诊断、恢复和高级手工编排
```

Studio 适配器刻意隔离在边界内。首期复用本机 TI 2.1.1 的 32 位 RadarLink 实现，以降低
固件下载和异步事件协议的硬件风险；下一阶段优先移植 Radar Toolbox 的 `studio_cli` 68xx
主机/设备参考代码。更底层的替代方案是移植 mmWave SDK 中的
`packages/ti/control/mmwavelink` 与 Studio 的 FTDI 参考传输层，但不能与现有 Studio
固件随意混搭：本机 Studio 2.1.1 与 SDK 3.6.2 的 mmWaveLink 版本不同，源码、BSS/MSS
固件和器件版本必须作为一个经过硬件验证的组合升级。

本机 Radar Toolbox 的 `studio_cli` xWR68xx 工程精确依赖 mmWave SDK 3.5.0.01、
SYS/BIOS 6.73.1.01、XDCtools 3.55.2.22 和 ARM CGT 16.9.6.LTS；当前机器并不具备这套
完全匹配的构建组合。首轮硬件验证应使用 Toolbox 自带的
`prebuilt_binaries/mmwave_Studio_cli_xwr68xx.bin`，不宣称能够用 SDK 3.6.2 原样重编。

## 文本 CLI 一体化采集

`demo capture CFG OUT --port COMx` 与 `studio-cli capture CFG OUT --port COMx` 共享同一个
协调器。主流程是：

```text
解析并预检 CFG
  -> 打开串口（studio-cli 用 version 确认 xWR68xx；demo 由调用者保证平台）
  -> sensorStop（幂等）
  -> DCA alive / StopRecord（幂等收敛上次异常会话）
  -> configure FPGA / configure packet（默认不 reset）
  -> 下发不含 sensorStart 的雷达配置
  -> 绑定数据端口并发送一次 StartRecord
  -> 收到 StartRecord 应答后发送一次 sensorStart
  -> 等待有限帧静默、首包超时或 Ctrl+C
  -> sensorStop -> 接收尾包 -> StopRecord -> flush/close
```

配置中的 `sensorStart` 是会话边界，不会和普通配置命令一起提前下发。显式 start 只接受
精确小写的 `sensorStart` 或 `sensorStart 0`，必须唯一且为最后一条有效命令；其中
`demo capture` 因为会重新下发完整配置而只接受 `sensorStart`，`sensorStart 0` 仅可用于
`studio-cli capture`。若不存在，会话合成 `sensorStart`。重复或后置命令会在任何硬件
I/O 前被拒绝。

CFG 预检把首期能力边界编码为契约：必须包含 `dfeDataOutputMode 1` 和至少一个
16-bit complex `adcCfg 2 1`/`adcCfg 2 2`；必须恰好包含一个 legacy `frameCfg`，且 `triggerSelect=1`；每条
LVDS 配置都必须符合无 header、硬件 ADC data format 1、软件流关闭的
`lvdsStreamCfg -1 0 1 0`。预检据 `adcCfg` 校验 DCA 数据位宽，并记录帧数和周期供会话
判断有限/无限帧。Advanced frame、LVDS header 和软件 LVDS 在此边界外。

`studio-cli` 固件提供 `version` 命令，因此该入口会验证响应包含 xWR68xx。标准 SDK demo
没有相同命令，`demo capture` 只能依赖调用者选择正确的板卡、固件和 COM 口；这不会扩大
“初期仅支持 68xx”的承诺。

协调器在配置前先发送一次 `sensorStop`，并跳过 CFG 内重复的 `sensorStop`，从而建立稳定
起点。正常有限帧结束也会再次幂等停止雷达。这里的停止只终止传感器/帧，不表示 RF 电源
域或 EVM 板级断电。

Ctrl+C 只设置线程安全的取消信号，实际 I/O 和清理由主流程执行。取消发生在 DCA armed
之后、雷达 start 之前时，不会再启动雷达；取消发生在采集中时，仍按先雷达后 DCA 的顺序
停止，完成文件 flush 后以 130 退出。有限帧通常以数据静默结束；无限帧通常依靠 Ctrl+C，
无限帧若在未取消时达到 `--idle-ms`，会按意外断流失败而不是发布输出。协调器会根据
`frameCfg` 周期自动提高过小的静默阈值，调用方仍可为其他预期间隔显式设置更大的值。
有限帧早断流门槛为 `(N - 1) × P - T`，其中
`T = min(P/2, max(1 ms, min(25 ms, 0.1P)))`；早于该门槛静默不能发布最终文件。
有限帧首包后的绝对上限为 `(N - 1) × P + idle + max(1000 ms, 2P)`；超过该上限仍持续
来包会进入失败清理，防止异常流让会话永久等待。无限帧只由 Ctrl+C 或异常静默结束。

串口响应与 DCA 命令使用独立的 `--serial-timeout-ms` 和 `--dca-timeout-ms`。
`--start-timeout-ms` 专指等待首个 ADC 数据包，不代表 StartRecord 命令应答时间。

一体化输出采用事务式发布：接收器以 CreateNew 打开同目录的 `OUT.part`，若目标 `OUT`
或临时文件已存在则在硬件 I/O 前拒绝；只有主流程、雷达停止和 DCA 停止全部成功，才把
临时文件原子改名为 `OUT`。硬件错误和 Ctrl+C 会保留已经创建的 `.part` 作为诊断证据，
不会把不完整数据伪装成成功输出。提交瞬间若出现同名 `OUT` 竞态，也拒绝覆盖并保留
`.part`。接收器合并每个 DCA 字节偏移区间；迟到包可修复空洞，但停止时仍有未覆盖字节
或收到低于首个基准偏移的包时，一体化会话拒绝发布。独立 `dca capture` 不使用这套发布协议。

## TI Lua 兼容策略

运行时创建 Lua 5.1 VM，并把 TI `AR1xxxWrapper` 注册为 `ar1`。本项目补充最小 `RSTD`
兼容函数（例如 `Sleep`、`GetRstdPath`、日志），并覆盖以下 DCA1000 函数，使录制生命周期
归本项目管理：

- `ar1.SelectCaptureDevice`
- `ar1.CaptureCardConfig_EthInit`
- `ar1.CaptureCardConfig_Mode`
- `ar1.CaptureCardConfig_PacketDelay`
- `ar1.CaptureCardConfig_StartRecord`
- `ar1.CaptureCardConfig_StopRecord`

普通 68xx 配置脚本无需启动 `mmWaveStudio.exe`。依赖 `RSTD.Build`、
`RSTD.RegisterDllEx`、GUI 控件、MATLAB 或级联设备的 Studio 启动脚本不属于兼容目标。
Legacy Lua 路径需要 RF 关断时，应在 `StopFrame()` 后调用 `PowerOff()`；它仍不表示 EVM
板级断电。

## DCA1000 生命周期与协议

一体化 capture 的 68xx 默认配置是两路 LVDS、16 位数据、`25 us` packet delay。正常
会话不 reset FPGA，以便同一块 DCA1000 连续承担多次采集；只有显式 `--reset` 或独立
`dca reset-fpga` 才改变这条复用约束：

```text
alive -> configure mode -> configure packet -> start record -> stop record
                                                     ^             |
                                                     +-------------+
```

DCA StartRecord 的响应超时意味着“命令是否生效未知”。实现不会重发 Start，而会发送一次
StopRecord 尝试把卡收敛到停止状态，再将本次会话报告为失败。这样避免一次高层命令产生
两次底层 start。独立的 `dca` 子命令仍然存在；`dca capture` 会配置并启动 DCA，但不控制雷达，只适合诊断
和调用方自行维护顺序的高级流程；它不承诺一体化会话的事务发布、完整性和 fatal-async
门槛。

命令包采用 TI 公布的 `0xA55A ... 0xEEAA` 小端协议；同步响应按命令码匹配，异步状态包
不会误当作同步响应。原始数据包前 10 字节是 32 位序号和 48 位字节偏移。录制器使用字节
偏移定位有效载荷，从而保留丢包空洞并允许乱序包回填。输出仍是 ADC 原始字节流，不包含
网络头，也不进行 ADC 重排、FFT 或检测处理。

## 68xx 初期边界与硬件门槛

初期只支持 xWR68xx。首轮承诺进一步收敛到 xWR6843 ES2 单芯片、两路硬件 LVDS、16 位
ADC 和 legacy frame。Advanced frame、软件 LVDS、级联、TSW1400、CSI-2 和板级电源
切断在真实硬件证明前均不作支持承诺。

真实硬件验收至少需要：

- 一块 xWR6843 ES2 EVM，以及与所选控制路径匹配的固件和 SOP 启动模式；
- 正确的 CLI UART；只有 Studio/RadarLink 固件下载路径要求 SOP2；
- DCA1000 网卡配置为 `192.168.33.30/24`，或在命令行显式覆盖地址和端口；
- 硬件 LVDS 已启用、软件 LVDS 已关闭且 ADC 位宽与 DCA 配置一致的有限帧 CFG；
- 用同一固件/配置连续完成两次不 reset 的启动、采集和停止回归。

离线测试覆盖命令编解码、CFG 计划、DCA 回环、取消与清理顺序，但不能替代上述实机
门槛。具体操作见 [hardware-smoke-test.md](hardware-smoke-test.md)。
