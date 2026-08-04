using System;
using System.Net;

namespace MmwCli
{
    public sealed class Dca1000LuaCompat : IDisposable
    {
        private readonly object _sync = new object();
        private readonly Action<string> _log;
        private DcaEndpointOptions _options;
        private Dca1000Client _client;
        private Dca1000Capture _capture;
        private bool _selected;

        internal Dca1000LuaCompat(Action<string> log)
        {
            _log = log;
        }

        public string LastError { get; private set; }

        internal bool IsRecording
        {
            get { return _capture != null && _capture.IsRecording; }
        }

        internal DcaCaptureStats Stats
        {
            get { return _capture == null ? null : _capture.Stats; }
        }

        public int SelectCaptureDevice(string device)
        {
            if (!string.Equals(device, "DCA1000", StringComparison.OrdinalIgnoreCase))
            {
                return Fail("首期只支持 DCA1000，收到: " + device);
            }

            _selected = true;
            return 0;
        }

        public int CaptureCardConfig_EthInit(
            string hostAddress,
            string deviceAddress,
            string macAddress,
            int configPort,
            int dataPort)
        {
            lock (_sync)
            {
                try
                {
                    if (!_selected)
                    {
                        _selected = true;
                    }

                    IPAddress host;
                    IPAddress device;
                    if (!IPAddress.TryParse(hostAddress, out host) || !IPAddress.TryParse(deviceAddress, out device))
                    {
                        return Fail("DCA1000 EthInit 收到无效 IPv4 地址。");
                    }

                    DisposeSession();
                    _options = new DcaEndpointOptions();
                    _options.HostAddress = host;
                    _options.DeviceAddress = device;
                    _options.ConfigPort = configPort;
                    _options.DataPort = dataPort;
                    _client = new Dca1000Client(_options);
                    ushort status = _client.Ping();
                    if (status != 0)
                    {
                        return Fail("DCA1000 alive 返回状态 " + status);
                    }

                    _capture = new Dca1000Capture(_options, _client);
                    Log(string.Format(
                        "DCA1000 已连接: {0}:{1}, data={2}",
                        device,
                        configPort,
                        dataPort));
                    return 0;
                }
                catch (Exception exception)
                {
                    return Fail(exception.Message);
                }
            }
        }

        public int CaptureCardConfig_Mode(
            int logMode,
            int lvdsMode,
            int transferMode,
            int captureMode,
            int dataFormat,
            int timer)
        {
            lock (_sync)
            {
                try
                {
                    EnsureConnected();
                    ushort status = _client.ConfigureFpga(
                        logMode,
                        lvdsMode,
                        transferMode,
                        captureMode,
                        dataFormat,
                        timer);
                    return status == 0 ? 0 : Fail("DCA1000 mode 配置失败，状态 " + status);
                }
                catch (Exception exception)
                {
                    return Fail(exception.Message);
                }
            }
        }

        public int CaptureCardConfig_PacketDelay(int delayMicroseconds)
        {
            lock (_sync)
            {
                try
                {
                    if (delayMicroseconds < 5 || delayMicroseconds > 255)
                    {
                        return Fail("TI Lua packet delay 必须在 5..255 微秒之间。");
                    }

                    EnsureConnected();
                    ushort status = _client.ConfigureRecord(delayMicroseconds);
                    return status == 0 ? 0 : Fail("DCA1000 packet delay 配置失败，状态 " + status);
                }
                catch (Exception exception)
                {
                    return Fail(exception.Message);
                }
            }
        }

        public int CaptureCardConfig_StartRecord(string outputPath, int sequenceNumberEnable)
        {
            lock (_sync)
            {
                try
                {
                    EnsureConnected();
                    if (sequenceNumberEnable != 0)
                    {
                        return Fail("首期 DCA1000 录制只支持 sequenceNumberEnable=0。");
                    }

                    _capture.Start(outputPath);
                    Log("DCA1000 开始录制: " + outputPath);
                    return 0;
                }
                catch (Exception exception)
                {
                    return Fail(exception.Message);
                }
            }
        }

        public int CaptureCardConfig_StopRecord()
        {
            lock (_sync)
            {
                try
                {
                    if (_capture == null || !_capture.IsRecording)
                    {
                        return 0;
                    }

                    _capture.Stop();
                    Log("DCA1000 已停止录制: " + _capture.Stats);
                    return 0;
                }
                catch (Exception exception)
                {
                    return Fail(exception.Message);
                }
            }
        }

        public int CaptureCard_DisConnect()
        {
            lock (_sync)
            {
                try
                {
                    DisposeSession();
                    return 0;
                }
                catch (Exception exception)
                {
                    return Fail(exception.Message);
                }
            }
        }

        internal bool WaitUntilIdle(int startTimeoutMilliseconds, int idleMilliseconds, Func<bool> cancelled)
        {
            Dca1000Capture capture = _capture;
            return capture == null || !capture.IsRecording ||
                   capture.WaitUntilIdle(startTimeoutMilliseconds, idleMilliseconds, cancelled);
        }

        public void Dispose()
        {
            lock (_sync)
            {
                DisposeSession();
            }
        }

        private void EnsureConnected()
        {
            if (_client == null || _capture == null)
            {
                throw new InvalidOperationException("请先调用 ar1.CaptureCardConfig_EthInit。");
            }
        }

        private void DisposeSession()
        {
            if (_capture != null)
            {
                _capture.Dispose();
                _capture = null;
            }

            if (_client != null)
            {
                _client.Dispose();
                _client = null;
            }
        }

        private int Fail(string message)
        {
            LastError = message;
            Log("DCA1000 错误: " + message);
            return -1;
        }

        private void Log(string message)
        {
            if (_log != null)
            {
                _log(message);
            }
        }
    }
}
