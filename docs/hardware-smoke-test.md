# xWR6843 + DCA1000 硬件冒烟测试

第 1–5 节定义 TI `studio_cli` 固件、xWR6843 ES2 和 DCA1000 的 functional/application
两轮复用验收流程；0.1 尚未完成这条路线的实机验收。一次测试只使用一种雷达 CLI 固件。

## 前置条件

- xWR6843 ES2、DCA1000、独立以太网口和匹配的电源/线缆；
- 已取得 `mmwave_Studio_cli_xwr68xx.bin`；
- 已构建的 mmwcli；
- 操作者确认的 CLI UART；
- 天线周围满足实验室 RF 安全要求。

按 TI 的 EVM、DCA1000 和 UniFlash 文档识别跳线与 SOP。不要带电切换 SOP 或插拔 MIPI。
烧录时按 Studio CLI 指南断开要求的连接；烧录后切回 functional/application 模式并重新
上电。SOP2 主机下载模式不能用于本测试。

## 1. 离线预检

以下命令不访问串口或 DCA1000：

```text
mmwcli firmware verify PATH/mmwave_Studio_cli_xwr68xx.bin
mmwcli studio-cli check hardware/studio-cli-xwr6843-raw.cfg
```

`firmware verify` 只校验上述单个文件，不需要完整 Radar Toolbox。将通过校验的文件烧录
到雷达；不要使用 TI 的 monitor profile 采集，它会启用本项目不接收的 monitor UART 数据。

记录雷达型号/ES、固件 SHA-256、SOP 和 CLI UART。`studio-cli version` 只能确认 xWR68xx
平台，不能替代板卡或采购记录对 ES2 的确认。

## 2. DCA1000 网络

默认拓扑：

```text
host 192.168.33.30/24
DCA  192.168.33.180
UDP control/data 4096/4098
```

确认没有其他 DCA 工具占用主机 UDP 4096。若需要连通性诊断，在用户确认 DCA 已供电后只
运行一次：

```text
mmwcli dca version
```

首次失败即停止排查，不循环探测。`dca ping` 的 SystemAlive 不属于 capture 前置门槛；
未 StartRecord 时它可能无响应。

## 3. 第一轮：完整配置

确认雷达已从 functional/application 模式启动，并把人工确认的串口替换为 `PORT`：

```text
mmwcli studio-cli capture hardware/studio-cli-xwr6843-raw.cfg capture-01.bin --port PORT
```

示例 CFG 为 100 个有限帧：每帧 64 chirps，每 chirp 包含 4 RX × 256 complex16 samples。
预期输出为：

```text
100 × 64 × 4 × 256 × 4 = 26,214,400 bytes
```

通过条件：

- 退出码为 0；
- `capture-01.bin` 恰好为 `26,214,400` bytes；
- 统计中没有 missing、discarded、malformed、overlap 或 fatal async status；
- 没有 `capture-01.bin.part`。

只看到非空文件不算通过。失败时保留的 `.part` 是诊断证据，不要改名冒充完整输出。

## 4. 第二轮：无重配复用

保持固件、SOP、CFG、串口和全部连接不变。不要 reset 雷达或 DCA1000：

```text
mmwcli studio-cli capture hardware/studio-cli-xwr6843-raw.cfg capture-02.bin --port PORT --no-reconfig
```

第二轮会再次 configure/start/stop DCA1000，但不 reset FPGA，也不重发雷达配置；雷达以
`sensorStart 0` 重启。它必须满足与第一轮相同的大小和完整性条件。任一轮使用 `--reset`
都不能作为复用验收结果。

## 5. 中断与失败处理

- Ctrl+C 或 `SIGTERM` 后等待程序完成 `sensorStop -> drain -> StopRecord`；不要立即断电。
- DCA StartRecord 应答超时表示状态未知。mmwcli 不重发 Start，只做一次 StopRecord 收敛。
- DCA raw 短尾包可能延迟约 2 秒；默认 2500 ms quiet/drain 属于正常等待。
- 不并行运行 DCA 命令；它们默认使用同一个本地 UDP 4096。
- 既有 `OUT` 或 `OUT.part` 会在硬件 I/O 前导致失败，工具不会覆盖它们。

## SDK demo 可选路径

SDK demo 固件使用 115200 baud，并由操作者自行确认平台：

```text
mmwcli demo capture profile_raw_68xx.cfg capture-sdk.bin --port PORT
```

profile 必须显式开启兼容的硬件 ADC LVDS。`demo capture` 不支持 `--no-reconfig`，因此不
用于上述 `studio_cli` 两轮复用验收。

## Functional/application 验收记录要求

保存以下信息：雷达型号/ES、固件版本和哈希、SOP、串口、波特率、DCA FPGA 版本、CFG
哈希、packet delay、两轮输出大小、包数、sequence gap、乱序数、退出原因、`.part` 状态，
以及第二轮是否明确使用 `--no-reconfig` 且未 reset。

## Debug mode 0.1 实机验收记录

debug mode 使用独立的 SOP2、Enhanced COM 与 D2XX/mmWaveLink 路线，不属于上面的
`studio_cli` 两轮复用测试。2026-08-05 使用以下等价命令完成了一次有限帧验收；`PORT`、
`PATH`、`BASE` 和 `OUT` 均由操作者显式提供：

```text
mmwcli debug-capture capture hardware/debug-capture-xwr6843-raw.cfg OUT --enhanced-port PORT --bss-fw PATH/xwr68xx_radarss.bin --mss-fw PATH/xwr68xx_masterss.bin --d2xx-description BASE --sop2-reset --delay-us 100
```

| 项目 | 验收值 |
| --- | --- |
| 雷达与采集卡 | IWR6843 QM、ES2；DCA1000 |
| 模式 | SOP2 `debug-capture`、legacy frame |
| 主机与原生边界 | Windows/amd64、FTDI D2XX 3.2.14、`CGO_ENABLED=0`、`ftd2xx` build tag |
| 测试源码 | revision `6b3e73e` |
| 测试程序 SHA-256 | `9A2271DB4D440DD1FF85237392D2254727C93592E60942FCF904D791AD07A80D` |
| BSS 固件 SHA-256 | `E2C69405394E35BA376EFE1A52305EE74DBD19F8BAB72BD5A9078878853CD77F` |
| MSS 固件 SHA-256 | `316911D4A8DBA1762714A3A107071BD0CF06A135FAE29BFBBC92B037592DE060` |
| CFG | `hardware/debug-capture-xwr6843-raw.cfg`；SHA-256 `69749D609DCF59CFF9C4F133BBEF11B4D3300CC71C0299E20998E969B7A5DC8D` |
| SOP2 与传输参数 | `--sop2-reset`；`--delay-us 100`；未 reset DCA FPGA |
| 预期与实际 payload | `26,214,400` bytes / `26,214,400` bytes |
| CLI 统计 | `packets=18005 payload=26214400 output=26214400 gaps=0 outOfOrder=0 missing=0` |
| 输出事务 | 成功发布 `OUT`，没有遗留 `OUT.part` |
| ADC 输出 SHA-256 | `418AFFD7705341CDE90D55EBBB7D01EE4C00C512DAE02DA031E99F400EE08DA1` |

ADC 输出不进入仓库或 release；其哈希只标识保留的实验室验收证据。该记录仅证明表中的
Windows/amd64、D2XX 3.2.14、固件、CFG 与硬件组合，不证明 functional/application、
SDK demo、Linux D2XX、arm64 或其它固件与配置。
