# 第三方组件与边界

本仓库当前不包含或重新分发 TI mmWave Studio 的 DLL、固件或 Lua 运行库。
`mmwcli studio` 只在运行时从用户自己的 mmWave Studio 2.1.1 安装目录加载这些文件。
它们继续受各自的 TI 许可条款约束，不能随本项目产物打包发布。

DCA1000 协议实现参考了本机 mmWave Studio 安装中的 DCA1000 ReferenceCode；后续
计划移植的 Radar Toolbox `tools/studio_cli` 主机/设备源码和 mmWave SDK mmWaveLink
源码均带有各自的 BSD-3-Clause notice。复制或派生这些源码时，必须把对应源文件头、
版权声明和许可文本一并保留。

本项目自身的许可证尚未确定；仓库根目录的 `LICENSE` 当前为空，发布前必须由维护者
选择并填写，不应把 TI 二进制组件的许可误当作本项目许可证。
