# 架构与安全保证

mmwcli 将设备协议、采集状态机和操作系统 I/O 分开，使 Windows 与 Linux 共用同一套行为。

```text
cmd/mmwcli
  -> internal/app          命令解析与退出码
  -> internal/firmware     单个 TI 设备固件的离线校验
  -> internal/debugcapture SOP2 资产校验、固件提交、D2XX/mmWaveLink 控制器
  -> internal/d2xx         可选 FTDI D2XX 原生库边界
  -> internal/radar        CLI 方言、CFG 预检、应答解析
     -> internal/serialport
  -> internal/session      雷达与 DCA1000 采集状态机
     -> internal/dca       UDP 控制与原始数据接收
     -> internal/capturefile
```

项目以 Go 1.26+ 标准库实现，核心构建使用 `CGO_ENABLED=0`。平台代码仅负责系统 I/O，
协议和状态机不得按操作系统分叉。可选 D2XX backend 由 `ftd2xx` build tag 隔离：Windows
加载系统安装的 DLL，Linux 是唯一允许的 CGo 变体，包含用户安装的官方 `ftd2xx.h` 并
链接 `libftd2xx.so`。
核心发布架构为 Windows/Linux amd64 与 arm64；原生 backend 的架构支持需要分别实机验证。

## 雷达 CLI 方言

| 方言 | 命令前缀 | 默认波特率 | 设备固件 |
| --- | --- | ---: | --- |
| TI `studio_cli` | `studio-cli` | 921600 | `mmwave_Studio_cli_xwr68xx.bin` |
| mmWave SDK demo | `demo` | 115200 | xWR68xx SDK demo 或兼容固件 |

两种方言共享传输层，但命令集合和启动语义隔离。`studio-cli version` 必须返回 xWR68xx
平台；SDK demo 没有统一的同类门禁。串口只能由操作者显式指定，程序不枚举或试探端口。

顶层 `repl` 不增加第三种方言。它固定创建 `StudioCLI` 客户端，先执行同一 xWR68xx
`version` 门禁，再逐行发送符合 CFG 词法规则的命令。兼容固件可以增加命令，但不能绕过
`Done`/数字 `Error` 终态合同；未知结果立即关闭会话，也不会授予该固件 capture 能力。

每条命令同时受串口 timeout 和调用者 context deadline 限制。若写入、取消、超时或应答
没有明确 `Done`/`Error`，设备状态记为未知。下一条命令必须先看到自身 echo，才能接受
终态响应，避免迟到应答完成错误的命令。

## CFG 预检

一体化 capture 在创建输出和打开硬件前构建不可变采集计划。0.1 的合同包括：

- xWR68xx、单芯片、`dfeDataOutputMode 1` 和软件触发 legacy frame；
- 16-bit complex ADC，ADC 与 DCA data format 一致；
- 无 LVDS header、硬件 ADC stream 开启、软件 stream 关闭；
- 两路 DCA LVDS-to-Ethernet raw 模式；
- channel/profile/chirp/frame 的引用和顺序完整；
- 每 RX ADCBuf 16-byte 对齐后的总量不超过 32 KiB；
- 无 header ADC-only CBUFF 单 chirp 不少于 64 bytes，每 RX transfer 不超过
  `0x3fff` 个 2-byte unit；
- 有限帧的 RX、sample、chirp、loop 与 frame 数能推导出精确输出字节数。

`sensorStart` 可省略；若存在则必须唯一且位于最后，协调器会先移除它。`studio_cli`
全量配置必须以 `flushCfg` 开始并通过 raw-only 白名单。`--no-reconfig` 仍完整预检同一
CFG，但不下发配置，只发送 `sensorStart 0`；它仅适用于 `studio-cli`。

Advanced frame、monitor、continuous、test、loopback、软件 LVDS 和 LVDS header 在预检
阶段被拒绝。TI 的 monitor profile 不是运行依赖，也不能用于本项目的 raw-only capture。

## SOP2 直控边界

SOP2 主机下载与直控路径使用独立入口 `debug-capture`，不属于文本 CLI 方言；其控制器实现
`session.Radar`，以便复用同一 DCA1000 采集状态机。MSS/BSS 固件必须由用户显式提供；程序
不会自动发现 TI 安装，也不依赖 mmWave Studio runtime、Lua 或 C#。资产预检包括严格哈希、
RPRC、xWR68xx 内存窗口和每块不超过 4096 字节的写计划。

操作者显式传入 `--sop2-reset` 时，控制器先从已验证的 A/B selector 同源派生 D/C：D 以
async bit-bang 设置 SOP2，保持到 C 将 NRST 拉低再释放，随后按 C→D 关闭；任一未知写入或
关闭错误都停止，且不会打开 Enhanced COM。默认不执行该目标复位，程序也不枚举 C/D。

Enhanced COM 只打开操作者显式指定的端口，不扫描设备。连接先探测 TI monitor 的
921600 baud，并按 TI 的 `UInt32 HexNumber` 规则接受 1–8 位十六进制响应；只有该只读探测
超时或无法按该规则解析、且端口已成功关闭时，才执行一次固定的 115200 冷启动协商。
低速 monitor 探测成功后，程序先读取并验证 xWR6843 part number，再保留 `0xFFFFE144`
原值并置位 `0x7800`、写入 TI 的 921600 切换值，然后关闭端口并以 921600 重新验证。程序
不尝试其它波特率；低速响应无效、切换写入的未知结果以及关闭失败都立即终止，且不会重写
切换寄存器。每次 921600 初始化中的固定三次 `x0` 握手及
TOPRCM part number 门禁通过后，按 xWR6843 的 BSS→MSS 顺序提交固件；未知写结果不重试，
也不自动 release 或 reset。随后以显式 serial/description 选择同一 FTDI 的 D2XX A/B
接口，通过 MPSSE SPI/IRQ 完成 mmWaveLink 启动门禁，再关闭 Enhanced COM。设备选择不使用
枚举、索引或 location，主机也不实现 raw USB。

mmWaveLink 启动门禁要求 MSS 固件为 `2.0.0.3`、RF 固件为 `6.2.1.5`。通过 `studio-cli`
合同预检的 CFG 会由主机翻译为固定的 RF、LVDS、profile、chirp、frame 与 apply 消息；
CFG 不作为文本发送。
RF 初始化必须报告完整校准 mask；frame start 和显式 stop 都只发送一次 trigger 并验证对应
事件，有限帧自然结束则只消费 frame-end 事件。未知结果不会重试。该路线不支持
`--no-reconfig`。

`debug-capture native-check` 只加载 D2XX 库，不查询或打开 USB 设备。公开 capture 命令也在
固件提交前执行同一 library-only 门禁。协议、失败状态和编排目前经过 fake 离线测试；本轮
没有 Enhanced COM、D2XX 或 ADC 采集的实机兼容性结论。

## 一体化采集状态机

```text
预检 CFG、资产、原生边界与输出路径
  -> 打开显式雷达 transport 和 DCA 控制端
  -> radar stop + StopRecord，收敛上次会话
  -> 配置 DCA1000（默认不 reset）
  -> 下发雷达配置
  -> arm 数据接收并发送一次 StartRecord
  -> StartRecord status=0 后发送一次 radar start
  -> 接收并验证数据
  -> 有限 debug 成功：等待自然 frame-end；其它路径：radar stop
  -> bounded data drain -> StopRecord -> control drain
  -> sync/close -> 发布 OUT
```

functional/application 路线把 radar start/stop 映射为固件文本命令；debug 路线映射为
mmWaveLink frame trigger。两条路线不会同时打开或混用。

StartRecord 应答缺失代表卡端状态未知。实现不重发 Start，只允许一次独立、有界的
StopRecord 收敛。debug 路线的有效有限帧完成后只消费并验证自然 frame-end 事件；取消、
超时、数据不完整、无限帧和不提供该能力的文本 CLI 路线仍显式停止雷达。清理使用独立
context；即使数据持续到达，drain 也有绝对上限，随后仍会执行 StopRecord。

## DCA1000 协议与接收

默认网络为主机 `192.168.33.30`、DCA `192.168.33.180`、UDP 控制 `4096`、数据 `4098`。
正常 configure/capture 不先执行 `SystemAlive`，也不自动 reset FPGA。

控制帧使用 TI 的小端 `0xA55A ... 0xEEAA` 格式。同步响应必须精确匹配命令码；异步状态
单独排队。为兼容 TI 参考 CLI，控制响应不按来源地址认证，但仍要求固定长度、帧头、帧尾
和命令码全部有效。数据通道则只接受配置的 DCA IP。

每个 raw datagram 包含 10-byte 头和最多 1456-byte payload。头部提供 32 位序号与 48 位
字节偏移；接收器按偏移写入，允许乱序包回填，但不会掩盖空洞、重叠、前缀丢失或 malformed
payload。

有限帧只有在 `[0, ExpectedOutputBytes)` 完整覆盖并经过 post-target quiet 后才成功；额外
payload、字节不足或超时都失败。无限帧意外静默同样失败。DCA raw 模式的短尾 payload
可能由 FPGA 延迟约 2 秒，因此 quiet/drain 下限为 2500 ms；小帧聚包需要更长时间时，
协调器会根据 CFG 自动提升首包与 quiet timeout。

## 事务式输出

输出先以 `OUT.part` 独占创建；已有 `OUT` 或 `.part` 会在硬件访问前拒绝。仅当接收、雷达
停止、DCA 停止、异步状态检查和文件同步全部成功，才无覆盖发布为 `OUT`。失败或取消保留
`.part`。

Windows 使用不替换目标的同卷移动，支持 NTFS、exFAT 和 FAT32，但仍受文件系统单文件
大小限制。其他平台使用同目录无覆盖发布；文件系统不支持所需原子操作时安全失败。

## 支持证据

离线测试覆盖协议、CFG、loopback、取消和清理，但不能证明硬件兼容。functional/application
组合至少要用相同固件和 CFG 完成两次有限帧采集：第一次全量配置，第二次
`--no-reconfig`，两次都不 reset DCA1000；步骤见
[hardware-smoke-test.md](hardware-smoke-test.md)。debug 组合不支持复用雷达配置，必须用
相同 MSS/BSS、CFG、D2XX 库和硬件连续完成完整采集，并单独记录实机证据。
