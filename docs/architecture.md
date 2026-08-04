# 架构与安全保证

mmwcli 将设备协议、采集状态机和操作系统 I/O 分开，使 Windows 与 Linux 共用同一套行为。

```text
cmd/mmwcli
  -> internal/app          命令解析与退出码
  -> internal/firmware     单个 TI 设备固件的离线校验
  -> internal/radar        CLI 方言、CFG 预检、应答解析
     -> internal/serialport
  -> internal/session      雷达与 DCA1000 采集状态机
     -> internal/dca       UDP 控制与原始数据接收
     -> internal/capturefile
```

项目以 Go 1.26+ 标准库实现，正式构建使用 `CGO_ENABLED=0`。平台代码仅负责 Windows COM
与 Linux tty；协议和状态机不得按操作系统分叉。0.1 的发布架构为 Windows/Linux amd64
与 arm64。

## 雷达 CLI 方言

| 方言 | 命令前缀 | 默认波特率 | 设备固件 |
| --- | --- | ---: | --- |
| TI `studio_cli` | `studio-cli` | 921600 | `mmwave_Studio_cli_xwr68xx.bin` |
| mmWave SDK demo | `demo` | 115200 | xWR68xx SDK demo 或兼容固件 |

两种方言共享传输层，但命令集合和启动语义隔离。`studio-cli version` 必须返回 xWR68xx
平台；SDK demo 没有统一的同类门禁。串口只能由操作者显式指定，程序不枚举或试探端口。

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

SOP2 主机下载与直控路径使用独立入口 `debug-capture`，不属于文本 CLI 方言，也不接入
当前 `session.Radar` 接口。MSS/BSS 固件必须由用户显式提供；该路径不得自动发现 TI 安装，
不得依赖 mmWave Studio runtime、Lua、C# 或 CGo。只有 CLI 中实际公开的子命令才视为已实现。

## 一体化采集状态机

```text
预检 CFG 与输出路径
  -> 打开显式串口和 DCA 控制端
  -> sensorStop + StopRecord，收敛上次会话
  -> 配置 DCA1000（默认不 reset）
  -> 下发雷达配置
  -> arm 数据接收并发送一次 StartRecord
  -> StartRecord status=0 后发送一次 sensorStart
  -> 接收并验证数据
  -> sensorStop -> bounded data drain -> StopRecord -> control drain
  -> sync/close -> 发布 OUT
```

StartRecord 应答缺失代表卡端状态未知。实现不重发 Start，只允许一次独立、有界的
StopRecord 收敛。取消和主流程错误同样使用独立清理 context；即使数据持续到达，drain
也有绝对上限，随后仍会执行 StopRecord。

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

离线测试覆盖协议、CFG、loopback、取消和清理，但不能证明硬件兼容。一个硬件组合至少要
用相同固件和 CFG 完成两次有限帧采集：第一次全量配置，第二次 `--no-reconfig`，两次都
不 reset DCA1000。验收步骤见 [hardware-smoke-test.md](hardware-smoke-test.md)。
