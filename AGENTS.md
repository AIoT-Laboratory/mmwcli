# mmwcli Agent Guide

本文件约束仓库内的自动化开发。面向用户的行为与用法以 `README.md` 和 `docs/` 为准。

## 项目边界

- `mmwcli` 是 TI xWR68xx 的跨平台命令行控制与 DCA1000 原始 ADC 采集工具。
- 首期基线是 xWR6843 ES2、legacy frame、16-bit complex ADC、两路 LVDS。
- 雷达只通过 SDK demo CLI 或 Radar Toolbox `studio_cli` 文本串口控制。
- 主机端使用 Go 1.26+ 标准库，正式构建固定 `CGO_ENABLED=0`；支持 Windows/Linux
  amd64 与 arm64。
- 不引入 GUI、MATLAB、后处理链、C#/.NET、CGo、mmWave Studio 运行时、TI DCA CLI、
  Lua host/interpreter 或第三方 Go 模块。
- TI 固件、配置和工具由用户从自己的 TI 安装中提供；不得复制到仓库或发布包。
- `sensorStop` 只停止传感器，不代表雷达或采集卡断电。

## 强制小批次

大型任务必须先拆分，禁止把实现、全库审计、文档重写和发布合并成一个批次。

满足任一条件时，编辑前必须列出多个可独立验收的批次：

- 涉及超过 2 个 package 或 8 个仓库文件；
- 改变超过 1 项公开行为；
- 同时包含实现与仓库级文档、发布或迁移工作；
- 预计连续工作超过 30 分钟。

每个批次必须遵守：

1. 只有一个主要目标，并写明退出条件。
2. 只修改达成该目标所需的文件；行为变化同时带最窄相关测试。
3. 完成后先运行窄验证、检查 diff，并向用户报告检查点；用户已授权提交时按批提交。
4. 新问题跨越额外 package、文件数越界或改变既定方案时，立即停止扩张并重新拆分。
5. 子代理只能承担边界明确、互不重叠的只读审计或小批实现，不能把拆分后的工作重新汇总成
   一个超大改动。

超过上述上限的例外必须在编辑前获得用户明确授权。机械生成文件不用于规避文件数限制。

## 开始工作

1. 阅读本文件、`README.md` 和与任务直接相关的 `docs/`。
2. 用只读 Git 命令确认分支、工作树和上游；未提交内容默认属于用户。
3. 声明当前批次、退出条件和是否涉及硬件。
4. 优先查 TI 官方本地资料与随附源码，不凭记忆猜协议。

## 硬件安全

- 默认只运行离线测试和 loopback fake。
- 没有用户在当前任务中的明确授权，不打开串口，不发送雷达命令，也不运行任何
  `dca ping/version/configure/start/stop/reset-*`。
- 用户授权探测时，每个目标只尝试一次；首次超时、bind error 或无响应后停止。电源、线缆
  或占用状态改变且用户要求后，才能重试。
- 不扫描串口，不猜测 CLI 端口、固件或波特率。
- 不并行执行两个 DCA 控制命令；它们默认竞争主机 UDP 4096。
- `SystemAlive` 只属于显式 `dca ping`，不是 configure/capture 的自动前置条件。
- StartRecord 结果未知时不得重发 Start；只允许一次有界 StopRecord 收敛。
- reset 只能由显式参数或用户明确请求触发。常规采集必须允许 DCA1000 复用。

## 实现约束

- 保持 `radar`、`dca`、`session`、`capturefile` 和 `serialport` 的职责边界；平台差异只放在
  小型 OS transport 文件中。
- 所有 CFG 与采集组合必须在创建输出文件和硬件 I/O 前完成预检。
- 一体化采集顺序为：配置雷达、启动 DCA 记录、启动雷达；清理顺序为：停止雷达、有限
  drain、停止 DCA、有限控制状态 drain。
- 任何 start 的未知结果都不得自动重试。清理使用独立、有界 context。
- 输出先独占创建为 `OUT.part`；仅在采集与清理全部成功后无覆盖发布为 `OUT`。失败保留
  `.part` 供诊断。
- 有限帧必须匹配 CFG 推导的精确字节数；空洞、重叠、前缀丢失、过短或过长流都失败。
- DCA 响应必须按协议验证帧结构和命令；保持与 TI CLI 一致的未知来源响应兼容行为。
- 原始输出保持原样，不做重排、解析、FFT、检测或隐式修复。
- 离线测试不能证明硬件兼容；兼容性结论必须来自可复现的实机验收记录。

详细协议与状态机见 `docs/architecture.md`。

## 验证

普通改动从最窄相关命令开始；涉及协议、生命周期、并发或发布路径时再扩展到全库：

```text
CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 go build -trimpath -o bin/mmwcli ./cmd/mmwcli
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/mmwcli-linux-amd64 ./cmd/mmwcli
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o bin/mmwcli-linux-arm64 ./cmd/mmwcli
```

报告实际运行的命令，并区分离线验证、环境检查和实机验收。默认验证不得访问硬件、安装
依赖或下载工具链。

## Git 与公开文档

- 默认开发分支是 `dev`；普通提交推送到 `origin/dev`，不得 force-push。
- 未经用户明确要求，不 commit、push、切换分支、改写历史、打 tag 或发布。
- 提交格式为 `<type>(<scope>): <description>`，一个提交只表达一个逻辑变化。
- 公开文档只描述可复现的安装、能力、限制和验证方法；不写本机绝对路径、代理工作过程、
  历史失误、临时状态或未验证的兼容性声明。
- 代码行为变化必须同步更新测试和直接相关文档，避免在多份文档重复同一段细节。
