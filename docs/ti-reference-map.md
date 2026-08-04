# TI 资料与版本地图

本机实际 TI 根目录是 `D:\Apps\ti`（不是 `D:\App\ti`）；Radar Toolbox 位于
`C:\ti\radar_toolbox_4_00_00_05`。下列内容已作为首期实现的本地资料基线。

## 无 GUI 68xx 首选参考

`C:\ti\radar_toolbox_4_00_00_05\tools\studio_cli`

- `prebuilt_binaries\mmwave_Studio_cli_xwr68xx.bin`：xWR6843 ES2 预编译固件；
- `src\mss\mmw_cli.c`、`mss_main.c`：设备端命令与状态机；
- `src\common\mmwl_if.c`：设备端 mmWaveLink 边界；
- `gui\mmw_cli_tool\mmw_main.c`、`serial_comm`：C 语言无 GUI 主机参考；
- `gui\mmw_cli_tool\dca_comm\dca_control.c`：DCA1000 调用顺序；
- `docs`：开发指南、入门指南和发布说明。

该工程针对 mmWave SDK 3.5.0.01，并精确要求 SYS/BIOS 6.73.1.01、XDCtools
3.55.2.22 和 ARM CGT 16.9.6.LTS。本机没有完整匹配的构建组合，所以首轮使用预编译
68xx 固件做硬件验证；不能用 SDK 3.6.2 替换依赖后声称得到等价固件。

## mmWave SDK 3.6.2

根目录：`D:\Apps\ti\mmwave_sdk_03_06_02_00-LTS`

- `packages\ti\demo\xwr68xx\mmw\mss\mmw_cli.c`：115200 文本 CLI 与
  `sensorStart`/`sensorStart 0` 的真实语义；
- `packages\ti\demo\xwr68xx\mmw\profiles`：官方配置样例；
- `packages\ti\control\mmwavelink`：未来原生 RadarLink 后端候选源码；
- `docs\mmwave_sdk_software_manifest.html`：版本与许可清单。

SDK demo 停止后若没有重发完整配置，必须用 `sensorStart 0` 重启。它和 Toolbox
`studio_cli` 固件虽然都使用文本命令，但命令集合、启动语义及 UART 波特率不应混用。

## Legacy mmWave Studio 2.1.1

根目录：`D:\Apps\ti\mmwave_studio_02_01_01_00`

- `mmWaveStudio\Clients\AR1xController\AR1xController.dll`；
- `mmWaveStudio\Clients\AR1xController\RadarLinkDLL.dll`；
- `mmWaveStudio\RunTime\lua51.dll`；
- `rf_eval_firmware\radarss\xwr68xx_radarss.bin`；
- `rf_eval_firmware\masterss\xwr68xx_masterss.bin`；
- `mmWaveStudio\Scripts\DataCaptureDemo_xWR.lua` 与 `RadarStudioAPIsTest.lua`；
- `mmWaveStudio\ReferenceCode\DCA1000\SourceCode`。

本机 `Scripts\xwr68xx.lua` 和 `xwr68xx_sync_capture.lua` 含用户后加路径，不能作为未修改的
官方黄金样例。Studio 2.1.1 的 RF evaluation 固件和 SDK 3.6.2 的 68xx RadarSS 版本
也不相同，禁止跨控制路径混搭 MSS/BSS/RadarSS。

## 首期硬件合同

- xWR6843 ES2，单芯片，两路硬件 LVDS，legacy frame；
- Studio/SOP2 路径为 Windows x86，动态使用用户本机 TI 2.1.1 运行库；
- DCA1000 默认 `192.168.33.30 -> 192.168.33.180`、UDP 4096/4098；
- DCA1000 可以反复 start/stop，常规停止不隐式 reset FPGA；
- 保存 ADC 原始字节，不做重排、FFT、检测、MATLAB 或 GUI 后处理。
