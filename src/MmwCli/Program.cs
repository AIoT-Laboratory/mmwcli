using System;
using System.IO;
using System.Reflection;

namespace MmwCli
{
    internal static class Program
    {
        private const string Version = "0.1.0-dev";

        [STAThread]
        private static int Main(string[] rawArgs)
        {
            try
            {
                var args = new Arguments(rawArgs);
                string command = args.Shift();
                if (command == null || IsHelp(command))
                {
                    PrintHelp();
                    return 0;
                }

                switch (command.ToLowerInvariant())
                {
                    case "version":
                    case "--version":
                    case "-v":
                        Console.WriteLine("mmwcli " + Version);
                        return 0;
                    case "doctor":
                        return RunDoctor(args);
                    case "self-test":
                        return RunSelfTest(args);
                    case "demo":
                        return RunTextCli(args, "demo", 115200, true);
                    case "studio-cli":
                        return RunTextCli(args, "studio-cli", 921600, true);
                    case "studio":
                        return RunStudio(args);
                    case "dca":
                        return RunDca(args);
                    default:
                        throw new UsageException("未知命令: " + command);
                }
            }
            catch (UsageException exception)
            {
                Console.Error.WriteLine("参数错误: " + exception.Message);
                Console.Error.WriteLine("运行 mmwcli help 查看用法。");
                return 2;
            }
            catch (OperationCanceledException exception)
            {
                Console.Error.WriteLine(exception.Message);
                return 130;
            }
            catch (TargetInvocationException exception)
            {
                Exception actual = exception.InnerException ?? exception;
                Console.Error.WriteLine("执行失败: " + actual.Message);
                return 5;
            }
            catch (Exception exception)
            {
                Console.Error.WriteLine("执行失败: " + exception.Message);
                if (Environment.GetEnvironmentVariable("MMWCLI_TRACE") == "1")
                {
                    Console.Error.WriteLine(exception);
                }

                return 4;
            }
        }

        private static int RunDoctor(Arguments args)
        {
            string studioRoot = args.TakeOption("--studio-root", null);
            args.EnsureEmpty();
            return Doctor.Run(studioRoot);
        }

        private static int RunSelfTest(Arguments args)
        {
            string studioRoot = args.TakeOption("--studio-root", null);
            args.EnsureEmpty();
            return SelfTest.Run(studioRoot);
        }

        private static int RunTextCli(
            Arguments args,
            string commandName,
            int defaultBaud,
            bool supportsNoReconfigure)
        {
            string action = args.Shift();
            if (action == null)
            {
                throw new UsageException(commandName + " 需要 apply/start/stop/capture 子命令。");
            }

            if (string.Equals(action, "capture", StringComparison.OrdinalIgnoreCase))
            {
                return RunTextCliCapture(args, commandName, defaultBaud);
            }

            string configPath = null;
            if (string.Equals(action, "apply", StringComparison.OrdinalIgnoreCase))
            {
                configPath = args.Shift();
                if (configPath == null)
                {
                    throw new UsageException(commandName + " apply 需要 .cfg 文件路径。");
                }
            }

            string port = args.TakeOption("--port", null);
            if (port == null)
            {
                throw new UsageException(commandName + " 命令需要 --port COMx。");
            }

            int baud = args.TakeIntOption("--baud", defaultBaud, 1200, 4000000);
            int timeout = args.TakeIntOption("--timeout-ms", 10000, 100, 60000);
            bool noReconfigure = args.TakeFlag("--no-reconfig");
            if (noReconfigure &&
                (!supportsNoReconfigure ||
                 !string.Equals(action, "start", StringComparison.OrdinalIgnoreCase)))
            {
                throw new UsageException("--no-reconfig 只适用于 demo/studio-cli start。");
            }

            args.EnsureEmpty();

            using (var cli = new DemoCli(port, baud, timeout))
            {
                cli.Open();
                if (string.Equals(action, "apply", StringComparison.OrdinalIgnoreCase))
                {
                    cli.ApplyFile(configPath);
                }
                else if (string.Equals(action, "start", StringComparison.OrdinalIgnoreCase))
                {
                    Console.WriteLine(cli.SendCommand(noReconfigure ? "sensorStart 0" : "sensorStart"));
                }
                else if (string.Equals(action, "stop", StringComparison.OrdinalIgnoreCase))
                {
                    Console.WriteLine(cli.SendCommand("sensorStop"));
                }
                else
                {
                    throw new UsageException("未知 " + commandName + " 子命令: " + action);
                }
            }

            return 0;
        }

        private static int RunTextCliCapture(Arguments args, string commandName, int defaultBaud)
        {
            string configPath = args.Shift();
            string outputPath = args.Shift();
            if (configPath == null || outputPath == null)
            {
                throw new UsageException(commandName + " capture 需要 CFG 和输出 BIN 路径。");
            }

            string port = args.TakeOption("--port", null);
            if (port == null)
            {
                throw new UsageException(commandName + " capture 需要 --port COMx。");
            }

            int baud = args.TakeIntOption("--baud", defaultBaud, 1200, 4000000);
            string ambiguousTimeout = args.TakeOption("--timeout-ms", null);
            if (ambiguousTimeout != null)
            {
                throw new UsageException(
                    "capture 不接受有歧义的 --timeout-ms；请使用 --serial-timeout-ms 或 --dca-timeout-ms。");
            }

            int serialTimeout = args.TakeIntOption("--serial-timeout-ms", 10000, 100, 60000);
            int startTimeout = args.TakeIntOption("--start-timeout-ms", 30000, 100, 3600000);
            int idle = args.TakeIntOption("--idle-ms", 1500, 100, 3600000);

            var dcaConfiguration = new DcaCaptureConfiguration();
            dcaConfiguration.LogMode = args.TakeIntOption("--log-mode", 1, 1, 2);
            dcaConfiguration.LvdsMode = args.TakeIntOption("--lvds-mode", 2, 1, 2);
            dcaConfiguration.TransferMode = args.TakeIntOption("--transfer-mode", 1, 1, 2);
            dcaConfiguration.CaptureMode = args.TakeIntOption("--capture-mode", 2, 1, 2);
            dcaConfiguration.DataFormat = args.TakeIntOption("--data-format", 3, 1, 3);
            dcaConfiguration.Timer = args.TakeIntOption("--timer", 30, 0, 255);
            dcaConfiguration.PacketDelayMicroseconds = args.TakeIntOption("--delay-us", 25, 5, 500);
            dcaConfiguration.ResetFpga = args.TakeFlag("--reset");

            DcaEndpointOptions endpoint = DcaEndpointOptions.FromArguments(args, "--dca-timeout-ms");
            args.EnsureEmpty();

            string fullConfigPath = Path.GetFullPath(configPath);
            string fullOutputPath = Path.GetFullPath(outputPath);
            if (string.Equals(fullConfigPath, fullOutputPath, StringComparison.OrdinalIgnoreCase))
            {
                throw new UsageException("配置文件与采集输出不能是同一个路径。");
            }

            TextCliCapturePlan plan = TextCliCapturePlan.FromFile(fullConfigPath);
            bool verifyPlatform = string.Equals(
                commandName,
                "studio-cli",
                StringComparison.OrdinalIgnoreCase);
            if (!verifyPlatform && string.Equals(plan.StartCommand, "sensorStart 0", StringComparison.Ordinal))
            {
                throw new UsageException(
                    "demo capture 会重新下发完整配置，因此必须使用 sensorStart；" +
                    "sensorStart 0 仅适用于不重新配置的 demo start --no-reconfig。");
            }

            if (dcaConfiguration.LogMode != 1 ||
                dcaConfiguration.LvdsMode != 2 ||
                dcaConfiguration.TransferMode != 1 ||
                dcaConfiguration.CaptureMode != 2)
            {
                throw new UsageException(
                    "首期 68xx 一体化采集只支持 DCA1000 raw/two-lane/LVDS-to-Ethernet 模式" +
                    "（--log-mode 1 --lvds-mode 2 --transfer-mode 1 --capture-mode 2）。");
            }

            if (plan.StartWasSynthesized)
            {
                Console.WriteLine("[capture] 配置中没有 sensorStart；将在 DCA1000 armed 后自动发送。");
            }

            if (plan.ExpectedDcaDataFormat.HasValue &&
                plan.ExpectedDcaDataFormat.Value != dcaConfiguration.DataFormat)
            {
                throw new UsageException(string.Format(
                    "配置中的 adcCfg 需要 DCA data-format={0}，当前为 {1}；请修正 --data-format。",
                    plan.ExpectedDcaDataFormat.Value,
                    dcaConfiguration.DataFormat));
            }
            if (plan.HardwareLvdsEnabled.HasValue && !plan.HardwareLvdsEnabled.Value)
            {
                throw new UsageException(
                    "配置中的 lvdsStreamCfg 没有启用硬件 LVDS 数据，DCA1000 将收不到 ADC 包。");
            }
            if (plan.InfiniteFrame.HasValue && plan.InfiniteFrame.Value)
            {
                Console.WriteLine(
                    "[capture] frameCfg 为无限帧；请用 Ctrl+C 停止，意外数据静默将按失败处理。");
            }

            if (plan.FramePeriodicityMilliseconds.HasValue &&
                (!plan.NumberOfFrames.HasValue || plan.NumberOfFrames.Value != 1))
            {
                double framePeriod = plan.FramePeriodicityMilliseconds.Value;
                double requiredIdleValue = framePeriod + Math.Max(500.0, framePeriod * 0.1);
                if (requiredIdleValue > 3600000.0)
                {
                    throw new UsageException(
                        "frameCfg 周期过长，无法在首期最大 3600000 ms 静默窗口内安全判定采集结束。");
                }

                int requiredIdle = (int)Math.Ceiling(requiredIdleValue);
                if (idle < requiredIdle)
                {
                    Console.WriteLine(string.Format(
                        "[capture] --idle-ms={0} 不足以跨越 {1} ms 帧周期；自动提高到 {2} ms。",
                        idle,
                        framePeriod,
                        requiredIdle));
                    idle = requiredIdle;
                }
            }

            var cancellation = new System.Threading.CancellationTokenSource();
            ConsoleCancelEventHandler handler = delegate(object sender, ConsoleCancelEventArgs eventArgs)
            {
                eventArgs.Cancel = true;
                cancellation.Cancel();
            };

            Console.CancelKeyPress += handler;
            try
            {
                using (var cli = new DemoCli(port, baud, serialTimeout))
                {
                    var session = new TextCliCaptureSession(
                        cli,
                        endpoint,
                        dcaConfiguration,
                        delegate(string message) { Console.WriteLine("[capture] " + message); },
                        verifyPlatform);
                    DcaCaptureStats stats = session.Run(
                        plan,
                        fullOutputPath,
                        startTimeout,
                        idle,
                        cancellation.Token);
                    Console.WriteLine("一体化采集完成: " + stats);
                }
            }
            finally
            {
                Console.CancelKeyPress -= handler;
                cancellation.Dispose();
            }

            return 0;
        }

        private static int RunStudio(Arguments args)
        {
            string action = args.Shift();
            if (action == null)
            {
                throw new UsageException("studio 需要 lua/eval 子命令。");
            }

            string content = args.Shift();
            if (content == null)
            {
                throw new UsageException("studio " + action + " 缺少脚本路径或 Lua 代码。");
            }

            string studioRoot = args.TakeOption("--studio-root", null);
            bool useTiDca = args.TakeFlag("--ti-dca");
            int startTimeout = args.TakeIntOption("--capture-start-timeout-ms", 30000, 100, 3600000);
            int idle = args.TakeIntOption("--capture-idle-ms", 1500, 100, 60000);
            args.EnsureEmpty();

            StudioInstallation installation = StudioLocator.Find(studioRoot);
            using (var host = new StudioLuaHost(installation, !useTiDca))
            {
                object[] values;
                if (string.Equals(action, "lua", StringComparison.OrdinalIgnoreCase))
                {
                    values = host.RunFile(content);
                }
                else if (string.Equals(action, "eval", StringComparison.OrdinalIgnoreCase))
                {
                    values = host.Evaluate(content);
                }
                else
                {
                    throw new UsageException("未知 studio 子命令: " + action);
                }

                PrintLuaResults(values);
                host.FinishCapture(startTimeout, idle);
            }

            return 0;
        }

        private static int RunDca(Arguments args)
        {
            string action = args.Shift();
            if (action == null)
            {
                throw new UsageException("dca 需要 ping/version/configure/capture/start/stop/reset-fpga/reset-radar 子命令。");
            }

            string outputPath = null;
            if (string.Equals(action, "capture", StringComparison.OrdinalIgnoreCase))
            {
                outputPath = args.Shift();
                if (outputPath == null)
                {
                    throw new UsageException("dca capture 需要输出文件路径。");
                }
            }

            int logMode = args.TakeIntOption("--log-mode", 1, 1, 2);
            int lvdsMode = args.TakeIntOption("--lvds-mode", 2, 1, 2);
            int transferMode = args.TakeIntOption("--transfer-mode", 1, 1, 2);
            int captureMode = args.TakeIntOption("--capture-mode", 2, 1, 2);
            int dataFormat = args.TakeIntOption("--data-format", 3, 1, 3);
            int timer = args.TakeIntOption("--timer", 30, 0, 255);
            int delay = args.TakeIntOption("--delay-us", 25, 5, 500);
            int idle = args.TakeIntOption("--idle-ms", 1500, 100, 60000);
            int startTimeout = args.TakeIntOption("--start-timeout-ms", 30000, 100, 3600000);
            bool noReset = args.TakeFlag("--no-reset");
            DcaEndpointOptions options = DcaEndpointOptions.FromArguments(args);
            args.EnsureEmpty();

            using (var client = new Dca1000Client(options))
            {
                string normalized = action.ToLowerInvariant();
                switch (normalized)
                {
                    case "ping":
                        RequireSuccess("alive", client.Ping());
                        Console.WriteLine("DCA1000 alive: OK");
                        return 0;
                    case "version":
                        ushort version = client.ReadFpgaVersion();
                        Console.WriteLine("DCA1000 FPGA " + Dca1000Client.FormatFpgaVersion(version));
                        return 0;
                    case "reset-fpga":
                        RequireSuccess("reset FPGA", client.ResetFpga());
                        Console.WriteLine("DCA1000 FPGA reset: OK");
                        return 0;
                    case "reset-radar":
                        RequireSuccess("reset radar", client.ResetRadar());
                        Console.WriteLine("DCA1000 radar reset: OK");
                        return 0;
                    case "start":
                        RequireSuccess("start record", client.StartRecord());
                        Console.WriteLine("DCA1000 record start: OK");
                        return 0;
                    case "stop":
                        RequireSuccess("stop record", client.StopRecord());
                        Console.WriteLine("DCA1000 record stop: OK");
                        return 0;
                    case "configure":
                        ConfigureDca(client, noReset, logMode, lvdsMode, transferMode, captureMode, dataFormat, timer, delay);
                        Console.WriteLine("DCA1000 configure: OK");
                        return 0;
                    case "capture":
                        ConfigureDca(client, true, logMode, lvdsMode, transferMode, captureMode, dataFormat, timer, delay);
                        return CaptureDca(options, client, outputPath, startTimeout, idle);
                    default:
                        throw new UsageException("未知 dca 子命令: " + action);
                }
            }
        }

        private static void ConfigureDca(
            Dca1000Client client,
            bool noReset,
            int logMode,
            int lvdsMode,
            int transferMode,
            int captureMode,
            int dataFormat,
            int timer,
            int delay)
        {
            RequireSuccess("alive", client.Ping());
            if (!noReset)
            {
                RequireSuccess("reset FPGA", client.ResetFpga());
            }

            RequireSuccess(
                "configure FPGA",
                client.ConfigureFpga(logMode, lvdsMode, transferMode, captureMode, dataFormat, timer));
            RequireSuccess("configure packet", client.ConfigureRecord(delay));
        }

        private static int CaptureDca(
            DcaEndpointOptions options,
            Dca1000Client client,
            string outputPath,
            int startTimeout,
            int idle)
        {
            var cancellation = new System.Threading.CancellationTokenSource();
            ConsoleCancelEventHandler handler = delegate(object sender, ConsoleCancelEventArgs eventArgs)
            {
                eventArgs.Cancel = true;
                cancellation.Cancel();
            };
            Console.CancelKeyPress += handler;
            try
            {
                using (var capture = new Dca1000Capture(options, client))
                {
                    capture.Start(outputPath);
                    Console.WriteLine("DCA1000 已 armed，等待雷达数据；Ctrl+C 可停止。");
                    bool idleReached = capture.WaitUntilIdle(
                        startTimeout,
                        idle,
                        delegate { return cancellation.IsCancellationRequested; });
                    capture.Stop();
                    if (cancellation.IsCancellationRequested)
                    {
                        throw new OperationCanceledException(
                            "DCA1000 录制已由用户停止，StopRecord 与文件清理已完成。");
                    }

                    if (!idleReached)
                    {
                        throw new Dca1000Exception("等待 DCA1000 首包超时。");
                    }

                    Console.WriteLine("录制完成: " + capture.Stats);
                }
            }
            finally
            {
                Console.CancelKeyPress -= handler;
                cancellation.Dispose();
            }

            return 0;
        }

        private static void RequireSuccess(string operation, ushort status)
        {
            if (status != 0)
            {
                throw new Dca1000Exception(operation + " 失败，DCA1000 状态: " + status);
            }
        }

        private static void PrintLuaResults(object[] values)
        {
            if (values == null || values.Length == 0)
            {
                return;
            }

            for (int index = 0; index < values.Length; index++)
            {
                Console.WriteLine("return[{0}] = {1}", index + 1, values[index] ?? "nil");
            }
        }

        private static bool IsHelp(string command)
        {
            return string.Equals(command, "help", StringComparison.OrdinalIgnoreCase) ||
                   string.Equals(command, "--help", StringComparison.OrdinalIgnoreCase) ||
                   string.Equals(command, "-h", StringComparison.OrdinalIgnoreCase);
        }

        private static void PrintHelp()
        {
            Console.WriteLine("mmwcli " + Version + " - TI xWR68xx headless control");
            Console.WriteLine();
            Console.WriteLine("用法:");
            Console.WriteLine("  mmwcli doctor [--studio-root PATH]");
            Console.WriteLine("  mmwcli self-test [--studio-root PATH]");
            Console.WriteLine("  mmwcli demo apply FILE.cfg --port COMx [--baud 115200]");
            Console.WriteLine("  mmwcli demo start [--no-reconfig] --port COMx");
            Console.WriteLine("  mmwcli demo stop --port COMx");
            Console.WriteLine("  mmwcli demo capture FILE.cfg FILE.bin --port COMx [DCA options]");
            Console.WriteLine("  mmwcli studio-cli apply FILE.cfg --port COMx [--baud 921600]");
            Console.WriteLine("  mmwcli studio-cli start [--no-reconfig] --port COMx");
            Console.WriteLine("  mmwcli studio-cli stop --port COMx");
            Console.WriteLine("  mmwcli studio-cli capture FILE.cfg FILE.bin --port COMx [DCA options]");
            Console.WriteLine("  mmwcli studio lua FILE.lua [--studio-root PATH] [--ti-dca]");
            Console.WriteLine("  mmwcli studio eval CODE [--studio-root PATH]");
            Console.WriteLine("  mmwcli dca ping|version|configure|start|stop|reset-fpga|reset-radar [options]");
            Console.WriteLine("  mmwcli dca capture FILE.bin [--host IP] [--device IP] [--delay-us 25]");
            Console.WriteLine();
            Console.WriteLine("DCA 默认: host=192.168.33.30 device=192.168.33.180 config=4096 data=4098");
            Console.WriteLine("一体化 capture 默认复用 DCA1000；仅需恢复卡状态时显式传 --reset。");
            Console.WriteLine("设置 MMWCLI_TRACE=1 可显示完整异常堆栈。");
        }
    }
}
