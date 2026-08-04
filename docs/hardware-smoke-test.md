# xWR6843 + DCA1000 硬件冒烟测试

一次测试只选择一种固件/控制路径。不要把 SDK demo、Toolbox `studio_cli` 和
Studio/SOP2 的固件或命令交叉使用。首轮基线限定为 xWR6843 ES2、单芯片、两路硬件
LVDS 和 legacy frame。

## 共同准备

1. 确认雷达型号与 ES 版本，记录烧录镜像、SOP 拨码和 CLI UART；文本 CLI 固件按其
   正常 flash boot 模式启动，只有 Studio/RadarLink 路径使用 SOP2。
2. 将 DCA1000 接到独立网卡，PC 地址设为 `192.168.33.30/24`；默认 DCA 地址为
   `192.168.33.180`，配置/数据 UDP 端口为 `4096/4098`。
3. 运行 `mmwcli doctor`。用 `mmwcli dca ping` 和 `mmwcli dca version` 做连通性诊断，
   并确认 mmWave Studio 或 TI 的录制进程没有占用 DCA 端口。
4. 检查配置满足首期严格契约：`dfeDataOutputMode 1`、16-bit complex
   `adcCfg 2 1`/`adcCfg 2 2`、
   恰好一个软件触发的 legacy `frameCfg`（`triggerSelect=1`），以及
   `lvdsStreamCfg -1 0 1 0`。首轮使用 16 位 ADC 和有限帧，并确认天线周围满足实验室
   RF 安全要求。
5. DCA packet delay 默认是 `25 us`。一体化 capture 默认复用已有 FPGA 状态，不 reset；
   首轮与第二轮都不要传 `--reset`，这样才能验证复用路径。

## Toolbox studio_cli 固件：首选验证路径

将匹配 xWR6843 ES2 的 `mmwave_Studio_cli_xwr68xx.bin` 烧录到设备。Radar Toolbox
自带的 `profile_monitor_xwr68xx.cfg` 是 100 帧、16 位 ADC、硬件 LVDS 开启的已知配置，
适合首轮验证。只需一个终端：

```powershell
mmwcli studio-cli capture profile_monitor_xwr68xx.cfg capture-01.bin --port COM3
```

该入口默认串口波特率为 921600。会话会先读取 `version` 并拒绝非 xWR68xx 平台，然后
确保雷达已停，先用 StopRecord 收敛 DCA 上次状态并重新配置，再下发除 `sensorStart`
外的雷达配置、启动录制，最后才触发 `sensorStart`。有限帧结束并达到默认 `1500 ms`
静默时间后，会话依次停止雷达、接收
尾包、停止 DCA1000，并关闭输出文件。数据先写入 `capture-01.bin.part`，完整成功后才
原子改名为 `capture-01.bin`；任一名字已存在都会拒绝覆盖。

确认第一轮成功后，不 reset FPGA，直接换输出名再执行一次：

```powershell
mmwcli studio-cli capture profile_monitor_xwr68xx.cfg capture-02.bin --port COM3
```

每轮应看到一次 StartRecord，以及会话前置 ensure-stop 和结束清理各一次 StopRecord，
并得到非空文件。第二轮成功才证明当前硬件组合支持 DCA1000 复用。`--reset` 只用于
明确的故障恢复；使用它的结果不能算复用验收。

## SDK demo 固件

SDK demo 使用 `demo capture`，默认波特率为 115200：

```powershell
mmwcli demo capture profile_raw_68xx.cfg capture-sdk-01.bin --port COM3
```

标准 SDK demo 没有 `studio_cli` 的 `version` 命令，因此这个入口不会自动识别平台。
操作者必须从板卡、烧录镜像和 COM 口映射确认目标确实是支持范围内的 xWR68xx。

不要直接假设普通 demo profile 能输出 ADC。TI SDK 3.6 的
`packages/ti/demo/xwr68xx/mmw/profiles/profile_2d.cfg` 默认包含
`lvdsStreamCfg -1 0 0 0`，即关闭硬件 LVDS；它会被一体化采集的预检拒绝。应复制为测试
配置并按目标 demo 的 CLI 定义启用硬件 ADC 流，例如两路 68xx 常用的
`lvdsStreamCfg -1 0 1 0`，同时保持软件 LVDS 为 0。`adcCfg 2 1` 对应默认的 DCA
`--data-format 3`（16 位）。

`demo capture` 会重新下发完整配置，因此不接受 `--no-reconfig`。该选项只属于低层
`start`：`demo start --no-reconfig` 用于已配置的 SDK demo 在 `sensorStop` 后发送
`sensorStart 0`；`studio-cli start --no-reconfig` 也受对应固件支持。

## 有限帧、无限帧与 Ctrl+C

- 首轮必须使用有限帧。收到首包后，数据连续静默 `--idle-ms`（默认 1500）即认为采集
  结束；若该值不足以跨越 `frameCfg` 帧周期，程序会自动加上安全余量。数据还有其他
  预期停顿时，应显式把该值设得更大。若明显早于配置所表达的有限帧时间跨度就静默，
  本次采集会失败并保留 `.part`；判定只保留最多 25 ms 且小于半帧的调度容差，不会把
  丢失整帧的早断流误报为完整文件。反之，有限帧超过按帧数、周期和 idle 推导的上限后
  仍持续来包，也会失败清理，不会永久占用雷达和 DCA。
- `--start-timeout-ms`（默认 30000）只表示等待首个 ADC UDP 包的时间。超时会执行雷达
  和 DCA 清理并返回失败；它不是 DCA StartRecord 应答超时。
- 无限帧通常用 Ctrl+C 结束。只按一次并等待程序输出清理结果；正常的用户取消返回码为
  130。不要关闭终端、强杀进程或拔线，否则不能保证 `sensorStop`、DCA StopRecord 和
  文件 flush 完成。如果临时输出已经创建，取消会保留 `OUT.part`，不会把未完整采集的
  数据发布为 `OUT`；检查或归档后再由操作者明确移走该文件。
- 无限帧也会在数据静默达到 `--idle-ms` 后结束。因此慢帧配置必须增大静默阈值；如果
  本应连续的数据意外静默，应把它当作链路异常检查，而不是成功的无限采集。
- DCA StartRecord 的应答若超时，卡端状态不确定。mmwcli 不会自动重发 Start，而会发送
  一次 StopRecord 收敛状态并报错；排查网络和卡状态后再由操作者决定是否重试整次会话。

## Studio/SOP2 Lua

从可信的 68xx 脚本副本开始，先核对 COM 号、BSS/MSS 路径、有限帧数量和 ADC 输出
路径，再运行：

```powershell
mmwcli studio lua .\capture-68xx.lua
```

本机 Studio `Scripts\xwr68xx.lua` 含用户修改过的绝对路径，不直接把它当黄金脚本。
需要 RF 关断时，脚本应执行 `ar1.StopFrame()` 后再执行 `ar1.PowerOff()`；DCA 录制仍需
单独停止。这里的 RF 关断和文本 CLI 的 `sensorStop` 都不代表 EVM 板级断电。

## 独立 DCA 命令

独立命令用于诊断、恢复或高级手工编排，不再作为冒烟测试的两终端主流程：

```powershell
mmwcli dca ping
mmwcli dca version
mmwcli dca configure --no-reset --delay-us 25
mmwcli dca stop
```

`mmwcli dca capture FILE.bin` 会配置 DCA FPGA/packet、发送 StartRecord 并接收网络数据，
但不会配置或启动雷达。若高级流程需要它，操作者必须自行保证 DCA 先于雷达 start，并按
“先停雷达、后停 DCA”的顺序清理；该诊断入口不采用一体化会话的 `.part`、完整性或
fatal-async 发布门槛。

## 验收记录

每条路径记录雷达型号/ES、固件版本与哈希、SOP、COM、波特率、DCA FPGA 版本、配置
文件哈希、packet delay、输出字节数、包数、sequence gap、乱序数、退出原因、是否遗留
`.part`，以及第二次无 reset 采集是否成功。若有未修复字节空洞或丢弃的前缀包，一体化
采集应失败而不是发布最终文件。只有真实设备完成两轮有限帧采集后，才把
对应组合标记为硬件支持。
