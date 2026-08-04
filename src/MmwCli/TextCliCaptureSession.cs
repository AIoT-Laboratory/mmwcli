using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Threading;

namespace MmwCli
{
    internal sealed class TextCliCapturePlan
    {
        private TextCliCapturePlan(
            IList<string> configurationCommands,
            string startCommand,
            bool startWasSynthesized,
            int? expectedDcaDataFormat,
            bool? hardwareLvdsEnabled,
            bool? infiniteFrame,
            int? numberOfFrames,
            double? framePeriodicityMilliseconds)
        {
            ConfigurationCommands = configurationCommands;
            StartCommand = startCommand;
            StartWasSynthesized = startWasSynthesized;
            ExpectedDcaDataFormat = expectedDcaDataFormat;
            HardwareLvdsEnabled = hardwareLvdsEnabled;
            InfiniteFrame = infiniteFrame;
            NumberOfFrames = numberOfFrames;
            FramePeriodicityMilliseconds = framePeriodicityMilliseconds;
        }

        public IList<string> ConfigurationCommands { get; private set; }
        public string StartCommand { get; private set; }
        public bool StartWasSynthesized { get; private set; }
        public int? ExpectedDcaDataFormat { get; private set; }
        public bool? HardwareLvdsEnabled { get; private set; }
        public bool? InfiniteFrame { get; private set; }
        public int? NumberOfFrames { get; private set; }
        public double? FramePeriodicityMilliseconds { get; private set; }

        public static TextCliCapturePlan FromFile(string path)
        {
            if (!File.Exists(path))
            {
                throw new FileNotFoundException("找不到配置文件。", path);
            }

            return FromCommands(DemoCli.ParseConfiguration(File.ReadAllLines(path)));
        }

        internal static TextCliCapturePlan FromCommands(IList<string> commands)
        {
            if (commands == null)
            {
                throw new ArgumentNullException("commands");
            }

            int startIndex = -1;
            for (int index = 0; index < commands.Count; index++)
            {
                string command = commands[index];
                if (!IsCommand(command, "sensorStart"))
                {
                    continue;
                }

                if (!string.Equals(command, "sensorStart", StringComparison.Ordinal) &&
                    !string.Equals(command, "sensorStart 0", StringComparison.Ordinal))
                {
                    throw new UsageException(
                        "首期 integrated capture 只接受精确小写的 sensorStart 或 sensorStart 0: " +
                        command);
                }

                if (startIndex >= 0)
                {
                    throw new UsageException("一体化采集配置只能包含一个 sensorStart。");
                }

                startIndex = index;
            }

            IList<string> configuration;
            string startCommand;
            bool startWasSynthesized;
            if (startIndex < 0)
            {
                configuration = new List<string>(commands);
                startCommand = "sensorStart";
                startWasSynthesized = true;
            }
            else
            {
                if (startIndex != commands.Count - 1)
                {
                    throw new UsageException("sensorStart 必须是采集配置中的最后一条命令。");
                }

                var preStartCommands = new List<string>();
                for (int index = 0; index < startIndex; index++)
                {
                    preStartCommands.Add(commands[index]);
                }

                configuration = preStartCommands;
                startCommand = commands[startIndex];
                startWasSynthesized = false;
            }

            ValidateLegacyFrameMode(configuration);
            int expectedDcaDataFormat = FindExpectedDcaDataFormat(configuration);
            ValidateHardwareLvds(configuration);
            FrameConfiguration frame = FindFrameConfiguration(configuration);

            return new TextCliCapturePlan(
                configuration,
                startCommand,
                startWasSynthesized,
                expectedDcaDataFormat,
                true,
                frame.NumberOfFrames == 0,
                frame.NumberOfFrames,
                frame.PeriodicityMilliseconds);
        }

        internal static bool IsCommand(string command, string expected)
        {
            if (string.IsNullOrWhiteSpace(command))
            {
                return false;
            }

            string trimmed = command.TrimStart();
            int separator = trimmed.IndexOfAny(new[] { ' ', '\t' });
            string name = separator < 0 ? trimmed : trimmed.Substring(0, separator);
            return string.Equals(name, expected, StringComparison.OrdinalIgnoreCase);
        }

        private static void ValidateLegacyFrameMode(IList<string> commands)
        {
            int count = 0;
            for (int index = 0; index < commands.Count; index++)
            {
                string command = commands[index];
                if (!IsCommand(command, "dfeDataOutputMode"))
                {
                    continue;
                }

                RequireExactCommandName(command, "dfeDataOutputMode");

                string[] fields = SplitFields(command);
                int mode;
                if (fields.Length != 2 || !int.TryParse(fields[1], out mode))
                {
                    throw new UsageException("无法解析 dfeDataOutputMode: " + command);
                }

                if (mode != 1)
                {
                    throw new UsageException(
                        "首期 integrated capture 仅支持 legacy frame（dfeDataOutputMode 1）: " +
                        command);
                }

                count++;
            }

            if (count == 0)
            {
                throw new UsageException(
                    "首期 integrated capture 要求配置包含 dfeDataOutputMode 1。");
            }
        }

        private static int FindExpectedDcaDataFormat(IList<string> commands)
        {
            int? expected = null;
            for (int index = 0; index < commands.Count; index++)
            {
                string command = commands[index];
                if (!IsCommand(command, "adcCfg"))
                {
                    continue;
                }

                RequireExactCommandName(command, "adcCfg");

                string[] fields = SplitFields(command);
                int adcBitsCode;
                int adcOutputFormat;
                if (fields.Length != 3 ||
                    !int.TryParse(fields[1], out adcBitsCode) ||
                    !int.TryParse(fields[2], out adcOutputFormat))
                {
                    throw new UsageException("无法解析 adcCfg 数据位宽或输出格式: " + command);
                }

                if (adcBitsCode != 2 || (adcOutputFormat != 1 && adcOutputFormat != 2))
                {
                    throw new UsageException(
                        "首期 xWR68xx capture 只支持 16-bit complex ADC（adcCfg 2 1 或 adcCfg 2 2）: " +
                        command);
                }

                int candidate = adcBitsCode + 1;
                if (expected.HasValue && expected.Value != candidate)
                {
                    throw new UsageException(
                        "首期 integrated capture 不接受相互冲突的 adcCfg 数据位宽。");
                }

                expected = candidate;
            }

            if (!expected.HasValue)
            {
                throw new UsageException(
                    "首期 integrated capture 要求配置包含可解析的 adcCfg。");
            }

            return expected.Value;
        }

        private static void ValidateHardwareLvds(IList<string> commands)
        {
            int count = 0;
            for (int index = 0; index < commands.Count; index++)
            {
                string command = commands[index];
                if (!IsCommand(command, "lvdsStreamCfg"))
                {
                    continue;
                }

                RequireExactCommandName(command, "lvdsStreamCfg");

                string[] fields = SplitFields(command);
                int subFrame;
                int headerEnabled;
                int hardwareDataFormat;
                int softwareEnabled;
                if (fields.Length != 5 ||
                    !int.TryParse(fields[1], out subFrame) ||
                    !int.TryParse(fields[2], out headerEnabled) ||
                    !int.TryParse(fields[3], out hardwareDataFormat) ||
                    !int.TryParse(fields[4], out softwareEnabled))
                {
                    throw new UsageException("无法解析 lvdsStreamCfg: " + command);
                }

                if (subFrame != -1 ||
                    headerEnabled != 0 || hardwareDataFormat != 1 || softwareEnabled != 0)
                {
                    throw new UsageException(
                        "首期 integrated capture 要求 lvdsStreamCfg 的 subFrame=-1、header=0、" +
                        "HW dataFmt=1、SW=0: " +
                        command);
                }

                count++;
            }

            if (count == 0)
            {
                throw new UsageException(
                    "首期 integrated capture 要求配置包含 lvdsStreamCfg -1 0 1 0。");
            }
        }

        private static FrameConfiguration FindFrameConfiguration(IList<string> commands)
        {
            FrameConfiguration result = null;
            int count = 0;
            for (int index = 0; index < commands.Count; index++)
            {
                string command = commands[index];
                if (!IsCommand(command, "frameCfg"))
                {
                    continue;
                }

                RequireExactCommandName(command, "frameCfg");

                string[] fields = SplitFields(command);
                int numberOfFrames;
                double periodicityMilliseconds;
                int triggerSelect;
                if (fields.Length != 8 ||
                    !int.TryParse(fields[4], out numberOfFrames) ||
                    numberOfFrames < 0 || numberOfFrames > ushort.MaxValue ||
                    !double.TryParse(
                        fields[5],
                        NumberStyles.Float,
                        CultureInfo.InvariantCulture,
                        out periodicityMilliseconds) ||
                    double.IsNaN(periodicityMilliseconds) ||
                    double.IsInfinity(periodicityMilliseconds) ||
                    periodicityMilliseconds <= 0 ||
                    !int.TryParse(fields[6], out triggerSelect))
                {
                    throw new UsageException(
                        "无法解析首期 legacy frameCfg 的帧数、周期或 triggerSelect: " + command);
                }

                if (triggerSelect != 1)
                {
                    throw new UsageException(
                        "首期 integrated capture 仅支持软件触发（frameCfg triggerSelect=1）: " +
                        command);
                }

                result = new FrameConfiguration(numberOfFrames, periodicityMilliseconds);
                count++;
            }

            if (count != 1)
            {
                throw new UsageException(
                    "首期 integrated capture 要求配置恰好包含一个 legacy frameCfg。");
            }

            return result;
        }

        private static string[] SplitFields(string command)
        {
            return (command ?? string.Empty).Split(
                new[] { ' ', '\t' },
                StringSplitOptions.RemoveEmptyEntries);
        }

        private static void RequireExactCommandName(string command, string expected)
        {
            string[] fields = SplitFields(command);
            if (fields.Length == 0 || !string.Equals(fields[0], expected, StringComparison.Ordinal))
            {
                throw new UsageException(
                    "68xx CLI 命令名区分大小写，必须写为 " + expected + ": " + command);
            }
        }

        private sealed class FrameConfiguration
        {
            public FrameConfiguration(int numberOfFrames, double periodicityMilliseconds)
            {
                NumberOfFrames = numberOfFrames;
                PeriodicityMilliseconds = periodicityMilliseconds;
            }

            public int NumberOfFrames { get; private set; }
            public double PeriodicityMilliseconds { get; private set; }
        }
    }

    internal sealed class DcaCaptureConfiguration
    {
        public DcaCaptureConfiguration()
        {
            LogMode = 1;
            LvdsMode = 2;
            TransferMode = 1;
            CaptureMode = 2;
            DataFormat = 3;
            Timer = 30;
            PacketDelayMicroseconds = 25;
            ResetFpga = false;
        }

        public int LogMode { get; set; }
        public int LvdsMode { get; set; }
        public int TransferMode { get; set; }
        public int CaptureMode { get; set; }
        public int DataFormat { get; set; }
        public int Timer { get; set; }
        public int PacketDelayMicroseconds { get; set; }
        public bool ResetFpga { get; set; }
    }

    internal sealed class TextCliCaptureOutput
    {
        public TextCliCaptureOutput(string outputPath)
        {
            if (string.IsNullOrWhiteSpace(outputPath))
            {
                throw new ArgumentException("采集输出路径不能为空。", "outputPath");
            }

            FinalPath = Path.GetFullPath(outputPath);
            PartPath = FinalPath + ".part";
        }

        public string FinalPath { get; private set; }
        public string PartPath { get; private set; }

        public void EnsureAvailable()
        {
            if (PathExists(FinalPath))
            {
                throw new IOException("采集输出已存在，拒绝覆盖: " + FinalPath);
            }

            if (PathExists(PartPath))
            {
                throw new IOException(
                    "采集临时输出已存在；请先保留、改名或删除上次失败文件: " + PartPath);
            }
        }

        public void Commit()
        {
            if (PathExists(FinalPath))
            {
                throw new IOException(
                    "提交采集输出时目标已存在；未覆盖目标，临时文件保留在: " + PartPath);
            }

            File.Move(PartPath, FinalPath);
        }

        private static bool PathExists(string path)
        {
            return File.Exists(path) || Directory.Exists(path);
        }
    }

    internal sealed class TextCliCaptureSession
    {
        private readonly ITextCliTransport _transport;
        private readonly DcaEndpointOptions _endpoint;
        private readonly DcaCaptureConfiguration _configuration;
        private readonly Action<string> _log;
        private readonly bool _verifyPlatform;

        public TextCliCaptureSession(
            ITextCliTransport transport,
            DcaEndpointOptions endpoint,
            DcaCaptureConfiguration configuration,
            Action<string> log)
            : this(transport, endpoint, configuration, log, true)
        {
        }

        public TextCliCaptureSession(
            ITextCliTransport transport,
            DcaEndpointOptions endpoint,
            DcaCaptureConfiguration configuration,
            Action<string> log,
            bool verifyPlatform)
        {
            if (transport == null)
            {
                throw new ArgumentNullException("transport");
            }

            if (endpoint == null)
            {
                throw new ArgumentNullException("endpoint");
            }

            if (configuration == null)
            {
                throw new ArgumentNullException("configuration");
            }

            _transport = transport;
            _endpoint = endpoint;
            _configuration = configuration;
            _log = log;
            _verifyPlatform = verifyPlatform;
        }

        public DcaCaptureStats Run(
            TextCliCapturePlan plan,
            string outputPath,
            int startTimeoutMilliseconds,
            int idleMilliseconds,
            CancellationToken cancellationToken)
        {
            if (plan == null)
            {
                throw new ArgumentNullException("plan");
            }

            double minimumStreamingMilliseconds = GetMinimumStreamingMilliseconds(plan);
            long maximumStreamingMilliseconds = GetMaximumStreamingMilliseconds(
                plan,
                idleMilliseconds);
            var captureOutput = new TextCliCaptureOutput(outputPath);
            captureOutput.EnsureAvailable();

            bool startAttempted = false;
            bool cancelled = false;
            Exception operationError = null;
            Exception radarStopError = null;
            Exception dcaStopError = null;
            var fatalAsyncStatuses = new List<ushort>();
            var asyncStatusSync = new object();

            using (var client = new Dca1000Client(_endpoint))
            using (var capture = new Dca1000Capture(_endpoint, client))
            {
                client.AsyncStatus += delegate(ushort status)
                {
                    Log(string.Format("DCA1000 async status=0x{0:X4}", status));
                    if (IsFatalDcaAsyncStatus(status))
                    {
                        lock (asyncStatusSync)
                        {
                            fatalAsyncStatuses.Add(status);
                        }
                    }
                };
                _transport.Open();
                if (_verifyPlatform)
                {
                    VerifyPlatform(cancellationToken);
                }
                else
                {
                    Log("目标固件没有统一的 version 命令；按调用方指定的 xWR68xx 边界继续");
                }
                Log("确保雷达处于停止状态");
                EnsureRadarStopped(cancellationToken);
                EnsureDcaStopped(client, cancellationToken);
                lock (asyncStatusSync)
                {
                    fatalAsyncStatuses.Clear();
                }
                ConfigureDca(client, cancellationToken);
                ApplyRadarConfiguration(plan.ConfigurationCommands, cancellationToken);
                Exception configurationAsyncError = CreateDcaAsyncStatusError(
                    fatalAsyncStatuses,
                    asyncStatusSync);
                if (configurationAsyncError != null)
                {
                    throw configurationAsyncError;
                }

                try
                {
                    cancellationToken.ThrowIfCancellationRequested();
                    Log("DCA1000 开始录制，随后触发 " + plan.StartCommand);
                    capture.StartNew(captureOutput.PartPath);
                    Exception startAsyncError = CreateDcaAsyncStatusError(
                        fatalAsyncStatuses,
                        asyncStatusSync);
                    if (startAsyncError != null)
                    {
                        throw startAsyncError;
                    }

                    cancellationToken.ThrowIfCancellationRequested();
                    startAttempted = true;
                    _transport.SendCommand(plan.StartCommand, cancellationToken);

                    bool idleReached = capture.WaitUntilIdle(
                        startTimeoutMilliseconds,
                        idleMilliseconds,
                        minimumStreamingMilliseconds,
                        maximumStreamingMilliseconds,
                        delegate { return cancellationToken.IsCancellationRequested; });
                    cancelled = cancellationToken.IsCancellationRequested;
                    if (!idleReached && !cancelled)
                    {
                        throw new Dca1000Exception("等待 DCA1000 首包超时。");
                    }

                    if (idleReached && plan.InfiniteFrame == true)
                    {
                        throw new Dca1000Exception(
                            "无限帧采集意外进入数据静默；按链路异常处理，不发布最终输出。");
                    }
                }
                catch (OperationCanceledException)
                {
                    cancelled = true;
                }
                catch (Exception exception)
                {
                    operationError = exception;
                }
                finally
                {
                    if (startAttempted)
                    {
                        try
                        {
                            Log("停止雷达传感器");
                            StopRadarIdempotently(CancellationToken.None);
                        }
                        catch (Exception exception)
                        {
                            radarStopError = exception;
                        }
                    }

                    if (capture.IsRecording)
                    {
                        if (radarStopError == null && capture.Stats.PacketsReceived > 0)
                        {
                            try
                            {
                                bool drained = capture.WaitForDrain(
                                    1000,
                                    Math.Min(idleMilliseconds, 1000));
                                if (!drained)
                                {
                                    Log("DCA1000 尾包 drain 达到 1000 ms 上限；继续强制停止录制");
                                    if (operationError == null && !cancelled)
                                    {
                                        operationError = new Dca1000Exception(
                                            "雷达停止后 DCA1000 数据仍持续超过 1000 ms；" +
                                            "已强制停止且不会发布最终输出。");
                                    }
                                }
                            }
                            catch (Exception exception)
                            {
                                Log("DCA1000 尾包 drain 警告: " + exception.Message);
                            }
                        }
                        else if (radarStopError != null)
                        {
                            Log("雷达停止未确认；跳过尾包 drain 并立即停止 DCA1000");
                        }

                        try
                        {
                            Log("停止 DCA1000 录制");
                            capture.Stop();
                        }
                        catch (Exception exception)
                        {
                            dcaStopError = exception;
                        }
                    }

                    try
                    {
                        bool controlDrained = client.DrainAsyncStatuses(50, 500);
                        if (!controlDrained)
                        {
                            dcaStopError = CombineErrors(
                                dcaStopError,
                                new Dca1000Exception(
                                    "DCA1000 控制端口在 500 ms 内未达到 50 ms 静默；" +
                                    "不会发布最终输出。"));
                        }
                    }
                    catch (Exception exception)
                    {
                        dcaStopError = CombineErrors(dcaStopError, exception);
                    }
                }

                cancelled = cancelled || cancellationToken.IsCancellationRequested;
                if (capture.Stats.MissingBytes > 0 ||
                    capture.Stats.DiscardedBeforeBasePackets > 0)
                {
                    Exception integrityError = new Dca1000Exception(string.Format(
                        "DCA1000 原始流不完整：missing={0} bytes, discarded_before_base={1}；" +
                        "临时文件保留但不会发布。",
                        capture.Stats.MissingBytes,
                        capture.Stats.DiscardedBeforeBasePackets));
                    operationError = CombineErrors(operationError, integrityError);
                }

                Exception asyncStatusError = CreateDcaAsyncStatusError(
                    fatalAsyncStatuses,
                    asyncStatusSync);
                if (cancelled)
                {
                    ThrowCombined(operationError, radarStopError, dcaStopError, asyncStatusError);
                    throw new OperationCanceledException("采集已由用户停止，已执行雷达与 DCA1000 清理。");
                }

                ThrowCombined(operationError, radarStopError, dcaStopError, asyncStatusError);
                captureOutput.Commit();
                return capture.Stats;
            }
        }

        private void ConfigureDca(Dca1000Client client, CancellationToken cancellationToken)
        {
            if (_configuration.ResetFpga)
            {
                cancellationToken.ThrowIfCancellationRequested();
                RequireSuccess("reset FPGA", client.ResetFpga());
            }

            cancellationToken.ThrowIfCancellationRequested();
            RequireSuccess(
                "configure FPGA",
                client.ConfigureFpga(
                    _configuration.LogMode,
                    _configuration.LvdsMode,
                    _configuration.TransferMode,
                    _configuration.CaptureMode,
                    _configuration.DataFormat,
                    _configuration.Timer));
            cancellationToken.ThrowIfCancellationRequested();
            RequireSuccess(
                "configure packet",
                client.ConfigureRecord(_configuration.PacketDelayMicroseconds));
        }

        private void EnsureDcaStopped(Dca1000Client client, CancellationToken cancellationToken)
        {
            cancellationToken.ThrowIfCancellationRequested();
            RequireSuccess("alive", client.Ping());
            cancellationToken.ThrowIfCancellationRequested();
            Log("确保 DCA1000 录制处于停止状态");
            RequireSuccess("ensure DCA1000 stopped", client.StopRecord());
            if (!client.DrainAsyncStatuses(50, 500))
            {
                throw new Dca1000Exception(
                    "DCA1000 ensure-stop 后控制端口持续有响应，无法建立稳定会话边界。");
            }
        }

        private static double GetMinimumStreamingMilliseconds(TextCliCapturePlan plan)
        {
            if (!plan.NumberOfFrames.HasValue ||
                !plan.FramePeriodicityMilliseconds.HasValue ||
                plan.NumberOfFrames.Value <= 1)
            {
                return 0;
            }

            double minimum =
                (plan.NumberOfFrames.Value - 1) * plan.FramePeriodicityMilliseconds.Value;
            if (double.IsNaN(minimum) || double.IsInfinity(minimum) ||
                minimum < 0 || minimum > long.MaxValue)
            {
                throw new UsageException("frameCfg 推导出的有限帧持续时间超出首期支持范围。");
            }

            double framePeriod = plan.FramePeriodicityMilliseconds.Value;
            double schedulingTolerance = Math.Min(
                framePeriod * 0.5,
                Math.Max(1.0, Math.Min(25.0, framePeriod * 0.1)));
            return Math.Max(double.Epsilon, minimum - schedulingTolerance);
        }

        private static long GetMaximumStreamingMilliseconds(
            TextCliCapturePlan plan,
            int idleMilliseconds)
        {
            if (!plan.NumberOfFrames.HasValue ||
                !plan.FramePeriodicityMilliseconds.HasValue ||
                plan.NumberOfFrames.Value == 0)
            {
                return 0;
            }

            double framePeriod = plan.FramePeriodicityMilliseconds.Value;
            double expectedSpan = Math.Max(0, plan.NumberOfFrames.Value - 1) * framePeriod;
            double maximum =
                expectedSpan + idleMilliseconds + Math.Max(1000.0, framePeriod * 2.0);
            if (double.IsNaN(maximum) || double.IsInfinity(maximum) ||
                maximum <= 0 || maximum > long.MaxValue)
            {
                throw new UsageException("frameCfg 推导出的有限帧最长等待时间超出首期支持范围。");
            }

            return (long)Math.Ceiling(maximum);
        }

        private void ApplyRadarConfiguration(IList<string> commands, CancellationToken cancellationToken)
        {
            for (int index = 0; index < commands.Count; index++)
            {
                cancellationToken.ThrowIfCancellationRequested();
                string command = commands[index];
                if (TextCliCapturePlan.IsCommand(command, "sensorStop"))
                {
                    Log("雷达配置中的 sensorStop 已由会话前置 ensure-stopped 执行");
                    continue;
                }

                Log(string.Format("雷达配置 [{0}/{1}] {2}", index + 1, commands.Count, command));
                _transport.SendCommand(command, cancellationToken);
            }
        }

        private void StopRadarIdempotently(CancellationToken cancellationToken)
        {
            try
            {
                _transport.SendCommand("sensorStop", cancellationToken);
            }
            catch (Exception exception)
            {
                if (IsAlreadyStopped(exception))
                {
                    Log("雷达已经停止（sensorStop 幂等成功）");
                    return;
                }

                throw;
            }
        }

        private void EnsureRadarStopped(CancellationToken cancellationToken)
        {
            try
            {
                StopRadarIdempotently(cancellationToken);
            }
            catch (Exception initialError)
            {
                try
                {
                    Log("ensure-stopped 结果未知；再发送一次幂等 sensorStop 收敛状态");
                    StopRadarIdempotently(CancellationToken.None);
                }
                catch (Exception cleanupError)
                {
                    throw new IOException(
                        "ensure-stopped 失败，且一次性 best-effort sensorStop 也失败。",
                        new AggregateException(initialError, cleanupError));
                }

                if (initialError is OperationCanceledException)
                {
                    throw;
                }

                throw new IOException(
                    "ensure-stopped 首次结果未知；第二次 sensorStop 已收敛状态，但本次采集仍安全中止。",
                    initialError);
            }
        }

        private void VerifyPlatform(CancellationToken cancellationToken)
        {
            Log("读取雷达版本并确认 xWR68xx");
            string response = _transport.SendCommand("version", cancellationToken);
            if (response == null ||
                response.IndexOf("Platform", StringComparison.OrdinalIgnoreCase) < 0 ||
                response.IndexOf("xWR68", StringComparison.OrdinalIgnoreCase) < 0)
            {
                throw new InvalidOperationException(
                    "version 响应未确认 xWR68xx；拒绝在未知设备/固件上启动采集。响应: " +
                    (response ?? "<null>"));
            }
        }

        private static bool IsAlreadyStopped(Exception exception)
        {
            for (Exception current = exception; current != null; current = current.InnerException)
            {
                string message = current.Message ?? string.Empty;
                if (message.IndexOf("Error -54", StringComparison.OrdinalIgnoreCase) >= 0 ||
                    message.IndexOf("already stopped", StringComparison.OrdinalIgnoreCase) >= 0 ||
                    message.IndexOf("already start/stop", StringComparison.OrdinalIgnoreCase) >= 0)
                {
                    return true;
                }
            }

            return false;
        }

        private static void RequireSuccess(string operation, ushort status)
        {
            if (status != 0)
            {
                throw new Dca1000Exception(operation + " 失败，DCA1000 状态: " + status);
            }
        }

        private static bool IsFatalDcaAsyncStatus(ushort status)
        {
            const ushort fatalMask =
                0x0001 | // no LVDS data
                0x0002 | // no header
                0x0004 | // EEPROM failure
                0x0040 | // mode configuration failure
                0x0080 | // DDR full
                0x0200;  // LVDS buffer full
            return (status & fatalMask) != 0;
        }

        private static Exception CreateDcaAsyncStatusError(
            IList<ushort> statuses,
            object statusSync)
        {
            ushort[] snapshot;
            lock (statusSync)
            {
                if (statuses.Count == 0)
                {
                    return null;
                }

                snapshot = new ushort[statuses.Count];
                statuses.CopyTo(snapshot, 0);
            }

            var formatted = new string[snapshot.Length];
            for (int index = 0; index < snapshot.Length; index++)
            {
                formatted[index] = string.Format("0x{0:X4}", snapshot[index]);
            }

            return new Dca1000Exception(
                "DCA1000 报告致命异步状态: " + string.Join(", ", formatted));
        }

        private static void ThrowCombined(
            Exception operationError,
            Exception radarStopError,
            Exception dcaStopError,
            Exception asyncStatusError)
        {
            if (operationError == null && radarStopError == null &&
                dcaStopError == null && asyncStatusError == null)
            {
                return;
            }

            var messages = new List<string>();
            if (operationError != null)
            {
                messages.Add("主流程: " + operationError.Message);
            }

            if (radarStopError != null)
            {
                messages.Add("雷达停止: " + radarStopError.Message);
            }

            if (dcaStopError != null)
            {
                messages.Add("DCA1000 停止: " + dcaStopError.Message);
            }

            if (asyncStatusError != null)
            {
                messages.Add("DCA1000 异步状态: " + asyncStatusError.Message);
            }

            Exception inner = operationError ?? radarStopError ?? dcaStopError ?? asyncStatusError;
            throw new IOException("一体化采集失败；" + string.Join("；", messages.ToArray()), inner);
        }

        private static Exception CombineErrors(Exception first, Exception second)
        {
            if (first == null)
            {
                return second;
            }

            if (second == null)
            {
                return first;
            }

            return new IOException(
                "DCA1000 清理发生多个错误。",
                new AggregateException(first, second));
        }

        private void Log(string message)
        {
            if (_log != null)
            {
                try
                {
                    _log(message);
                }
                catch (Exception)
                {
                    // Logging must never prevent radar/DCA cleanup.
                }
            }
        }
    }
}
