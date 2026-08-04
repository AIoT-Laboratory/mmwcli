# xWR6843 + DCA1000 硬件冒烟测试

本流程验证 TI `studio_cli` 固件、xWR6843 ES2 和 DCA1000 的两轮复用采集。
一次测试只使用一种雷达 CLI 固件。

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

## 验收记录

保存以下信息：雷达型号/ES、固件版本和哈希、SOP、串口、波特率、DCA FPGA 版本、CFG
哈希、packet delay、两轮输出大小、包数、sequence gap、乱序数、退出原因、`.part` 状态，
以及第二轮是否明确使用 `--no-reconfig` 且未 reset。
