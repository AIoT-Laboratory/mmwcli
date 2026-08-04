# mmwcli Agent Guide

`mmwcli` 是 TI xWR68xx 的无 GUI 控制与 DCA1000 原始采集工具。当前阶段只实现控制面，
不包含 MATLAB、FFT、检测、可视化或其他数据处理链。

## 新会话开始

按顺序阅读：

1. 本文件（`AGENTS.md`）
2. `README.md`
3. `docs/architecture.md`
4. `docs/ti-reference-map.md`
5. 与硬件操作有关时再读 `docs/hardware-smoke-test.md`

随后用只读 Git 命令确认分支和工作树。不要假设未提交文件可删除、覆盖或回退；它们属于
用户，除非上下文明确说明是本任务生成的文件。

---

## 项目边界

- 初期只支持 xWR68xx；首轮硬件基线进一步限定为 xWR6843 ES2、单芯片、两路硬件
  LVDS、16-bit complex ADC 和 legacy frame。
- 三条控制路径必须分开：SDK demo 文本 CLI、Radar Toolbox `studio_cli` 文本 CLI、
  Studio/SOP2 RadarLink + TI Lua。不得混用固件、波特率、命令语义或启动流程。
- 文本 CLI 的 `sensorStop` 只是停止传感器/帧，不得描述为 EVM 断电。Studio Lua 的
  `PowerOff()` 是 RF 关断，也不得描述为板级电源切断。
- DCA1000 输出是原始 ADC 字节，不添加重排、解析、FFT、检测或隐式后处理。
- 不把离线测试等同于硬件支持。只有用户认可的实机两轮无 reset 采集才能证明某个
  板卡/固件组合可用。

---

## 硬件与探测安全

- 默认只做离线开发和 fake-loopback 测试。`build`、`self-test` 和读取本地 TI 资料不需要
  连接真实硬件。
- 未经用户明确要求，不运行 `dca ping/version/configure/start/stop/reset-*`，不打开
  COM 口，不执行会访问硬件的 Lua，也不向雷达发送任何命令。
- 用户明确要求探测时，每个目标只尝试一次。首次 timeout、bind error 或无响应后立即
  停止并报告；除非用户说明电源、线缆或占用状态已经改变并要求重试，否则不得循环探测。
- DCA1000 单独供电可能关闭；无响应是正常外部状态，不是需要持续重试的软件故障。
- 绝不并行运行两个 DCA 控制命令。它们都需要绑定主机 UDP `4096`，并行调用会制造
  本地端口冲突和误导性错误。
- 不根据枚举出的 COM 名称猜测 CLI 端口。发送串口命令前必须知道用户指定的端口、固件
  路径和预期波特率。
- `configure`、`sensorStart`、`sensorStop`、DCA Start/StopRecord、reset 和 Lua RF API
  都是硬件状态操作；只有用户明确把对应设备和操作放入当前任务范围后才执行。
- DCA StartRecord 应答超时表示卡端状态未知：不得重发 Start，只允许一次 best-effort
  StopRecord 收敛状态。清理顺序保持 `sensorStop -> bounded drain -> StopRecord`。
- 常规一体化采集默认不 reset DCA FPGA，以支持复用；reset 只能由显式 `--reset` 或用户
  明确请求触发。

### 已记录的代理失误

| 失误 | 次数 |
| --- | ---: |
| DCA 已知断电/首次无响应后仍继续探测 | 1 |
| 并行运行需要绑定同一 UDP 4096 端口的 DCA 命令 | 1 |

这些是流程错误，不是可接受的试错方式。后续代理必须在首次失败处停止硬件探测。

---

## 本机 TI 资料基线

- 实际 TI 根目录：`D:\Apps\ti`（不是 `D:\App\ti`）
- mmWave Studio：`D:\Apps\ti\mmwave_studio_02_01_01_00`
- mmWave SDK：`D:\Apps\ti\mmwave_sdk_03_06_02_00-LTS`
- Radar Toolbox：`C:\ti\radar_toolbox_4_00_00_05`
- 首选无 GUI 参考：`C:\ti\radar_toolbox_4_00_00_05\tools\studio_cli`

优先核对本机官方源码、release notes、developer guide 和 profile，不凭记忆猜协议。不要把
SDK 3.6.2 与 `studio_cli` 所需的 SDK 3.5.0.01 依赖组合描述为可互换。不得把 TI DLL、
固件或 Lua 运行库复制进仓库或发布包；它们只从用户自己的安装目录动态加载。

---

## 架构与实现规则

- 保持 `DemoCli`、`TextCliCaptureSession`、`Dca1000Client`、`Dca1000Capture`、
  `StudioLuaHost` 的边界清晰，不添加 GUI 或数据处理依赖。
- 一体化 capture 必须先从 CFG 剥离 `sensorStart`，确认 DCA StartRecord status=0 后才发送
  start。启动结果未知时绝不自动重试。
- CFG 必须在硬件 I/O 前预检。首期契约是 `dfeDataOutputMode 1`、16-bit complex
  `adcCfg`、恰好一个软件触发 legacy `frameCfg`，以及 `lvdsStreamCfg -1 0 1 0`。
- 68xx CLI 命令名区分大小写；显式启动只接受精确 `sensorStart` 或（仅适用的固件路径）
  `sensorStart 0`。
- 一体化输出先以 `OUT.part` 和 `CreateNew` 打开。只有主流程、雷达停止和 DCA 停止全部
  成功后才改名为 `OUT`；错误或取消保留 `.part`，既有 `OUT`/`.part` 不得覆盖。
- 清理等待必须有绝对上限。即使雷达仍在持续发流，也必须最终执行 DCA StopRecord，不能
  在等待 idle 时永久阻塞。
- 有限帧既要检查过早静默，也要有按帧计划推导的最长持续时间；无限帧意外静默属于失败。
- StopRecord 后必须在有绝对上限的 quiet window 内排空 DCA 控制端口，再判定异步状态。
- DCA 致命异步状态、接收线程异常、未修复字节空洞、丢弃的前缀包、过早/过长数据流和
  清理错误都不得被降级为成功。
- 行为变化必须同步更新离线测试和文档。优先使用小型 loopback fake，不用真实硬件验证
  普通代码改动。

---

## 构建与验证

项目是 Windows x86/.NET Framework 工具。构建不得下载依赖或安装软件：

```powershell
.\build.ps1
.\bin\Debug\mmwcli.exe self-test
.\build.ps1 -Configuration Release
.\bin\Release\mmwcli.exe self-test
```

- 对普通改动运行最窄相关测试；涉及协议、采集生命周期或并发清理时，再做多轮 self-test。
- `doctor` 只用于用户要求的环境诊断；它不能证明 DCA 或雷达可用。
- 不把 `dca ping` 或真实串口命令列入默认验证步骤。
- 报告实际运行的命令和结果，并明确区分离线验证、环境发现与真实硬件验收。
- 不运行依赖安装、升级、大型下载或长时间任务；需要时把准确命令交给用户决定。

---

## Git 与文档

- 保持改动聚焦，不清理或提交无关用户文件。
- 未经用户要求，不 commit、push、切换分支、改写历史或生成发布。
- 提交格式以 `cliff.toml` 为准：`<type>(<scope>): <description>`。
- 不手工声称尚未实机验证的兼容性。硬件组合、固件版本、SOP、COM、DCA FPGA 版本和
  两轮复用结果应按 `docs/hardware-smoke-test.md` 留证。
