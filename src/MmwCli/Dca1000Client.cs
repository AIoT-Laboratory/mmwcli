using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Net;
using System.Net.Sockets;

namespace MmwCli
{
    internal sealed class DcaEndpointOptions
    {
        public DcaEndpointOptions()
        {
            HostAddress = IPAddress.Parse("192.168.33.30");
            DeviceAddress = IPAddress.Parse("192.168.33.180");
            ConfigPort = 4096;
            DataPort = 4098;
            TimeoutMilliseconds = 3000;
        }

        public IPAddress HostAddress { get; set; }
        public IPAddress DeviceAddress { get; set; }
        public int ConfigPort { get; set; }
        public int DataPort { get; set; }
        public int TimeoutMilliseconds { get; set; }

        public static DcaEndpointOptions FromArguments(Arguments args)
        {
            return FromArguments(args, "--timeout-ms");
        }

        public static DcaEndpointOptions FromArguments(Arguments args, string timeoutOption)
        {
            var options = new DcaEndpointOptions();
            options.HostAddress = ParseAddress(args.TakeOption("--host", options.HostAddress.ToString()), "--host");
            options.DeviceAddress = ParseAddress(args.TakeOption("--device", options.DeviceAddress.ToString()), "--device");
            options.ConfigPort = args.TakeIntOption("--config-port", options.ConfigPort, 1, 65535);
            options.DataPort = args.TakeIntOption("--data-port", options.DataPort, 1, 65535);
            options.TimeoutMilliseconds = args.TakeIntOption(
                timeoutOption,
                options.TimeoutMilliseconds,
                100,
                60000);
            return options;
        }

        private static IPAddress ParseAddress(string value, string option)
        {
            IPAddress address;
            if (!IPAddress.TryParse(value, out address) || address.AddressFamily != AddressFamily.InterNetwork)
            {
                throw new UsageException(option + " 必须是 IPv4 地址，收到: " + value);
            }

            return address;
        }
    }

    internal sealed class Dca1000Exception : Exception
    {
        public Dca1000Exception(string message)
            : base(message)
        {
        }

        public Dca1000Exception(string message, Exception innerException)
            : base(message, innerException)
        {
        }
    }

    internal sealed class Dca1000Client : IDisposable
    {
        private readonly object _sync = new object();
        private readonly DcaEndpointOptions _options;
        private readonly Socket _socket;
        private readonly EndPoint _remoteEndpoint;
        private bool _disposed;

        public Dca1000Client(DcaEndpointOptions options)
        {
            if (options == null)
            {
                throw new ArgumentNullException("options");
            }

            _options = options;
            _socket = new Socket(AddressFamily.InterNetwork, SocketType.Dgram, ProtocolType.Udp);
            _socket.ReceiveTimeout = options.TimeoutMilliseconds;
            _socket.SendTimeout = options.TimeoutMilliseconds;

            try
            {
                _socket.Bind(new IPEndPoint(options.HostAddress, options.ConfigPort));
            }
            catch (Exception exception)
            {
                _socket.Close();
                throw new Dca1000Exception(string.Format(
                    "无法绑定 DCA1000 配置端口 {0}:{1}。请确认网卡地址已配置，且 Studio/TI CLI 没有占用该端口。",
                    options.HostAddress,
                    options.ConfigPort), exception);
            }

            _remoteEndpoint = new IPEndPoint(options.DeviceAddress, options.ConfigPort);
        }

        public event Action<ushort> AsyncStatus;

        public ushort Execute(DcaCommand command, byte[] payload)
        {
            lock (_sync)
            {
                ThrowIfDisposed();
                byte[] request = Dca1000Protocol.BuildRequest(command, payload);
                try
                {
                    _socket.SendTo(request, _remoteEndpoint);
                }
                catch (SocketException exception)
                {
                    throw new Dca1000Exception("发送 DCA1000 命令失败: " + command, exception);
                }

                Stopwatch wait = Stopwatch.StartNew();
                var responseBuffer = new byte[64];
                while (wait.ElapsedMilliseconds < _options.TimeoutMilliseconds)
                {
                    int remaining = Math.Max(
                        1,
                        _options.TimeoutMilliseconds - (int)wait.ElapsedMilliseconds);
                    _socket.ReceiveTimeout = remaining;
                    EndPoint sender = new IPEndPoint(IPAddress.Any, 0);
                    int received;
                    try
                    {
                        received = _socket.ReceiveFrom(responseBuffer, ref sender);
                    }
                    catch (SocketException exception)
                    {
                        if (exception.SocketErrorCode == SocketError.TimedOut ||
                            exception.SocketErrorCode == SocketError.WouldBlock)
                        {
                            break;
                        }

                        throw new Dca1000Exception("接收 DCA1000 响应失败: " + command, exception);
                    }

                    var senderAddress = sender as IPEndPoint;
                    if (senderAddress == null ||
                        !senderAddress.Address.Equals(_options.DeviceAddress) ||
                        senderAddress.Port != _options.ConfigPort)
                    {
                        continue;
                    }

                    DcaResponse response;
                    try
                    {
                        response = Dca1000Protocol.ParseResponse(responseBuffer, received);
                    }
                    catch (FormatException exception)
                    {
                        throw new Dca1000Exception(
                            "DCA1000 控制端口收到来自设备地址的损坏响应。",
                            exception);
                    }

                    if (response.Command == DcaCommand.SystemAsyncStatus)
                    {
                        Action<ushort> handler = AsyncStatus;
                        if (handler != null)
                        {
                            handler(response.Status);
                        }

                        continue;
                    }

                    if (response.Command != command)
                    {
                        continue;
                    }

                    return response.Status;
                }

                throw new Dca1000Exception(string.Format(
                    "等待 DCA1000 命令 {0} 响应超时（{1} ms）。",
                    command,
                    _options.TimeoutMilliseconds));
            }
        }

        public ushort Ping()
        {
            return Execute(DcaCommand.SystemAliveness, null);
        }

        public ushort ReadFpgaVersion()
        {
            return Execute(DcaCommand.ReadFpgaVersion, null);
        }

        public ushort ResetFpga()
        {
            return Execute(DcaCommand.ResetFpga, null);
        }

        public ushort ResetRadar()
        {
            return Execute(DcaCommand.ResetRadar, null);
        }

        public ushort ConfigureFpga(int logMode, int lvdsMode, int transferMode, int captureMode, int dataFormat, int timer)
        {
            return Execute(
                DcaCommand.ConfigureFpga,
                Dca1000Protocol.BuildFpgaConfiguration(
                    logMode,
                    lvdsMode,
                    transferMode,
                    captureMode,
                    dataFormat,
                    timer));
        }

        public ushort ConfigureRecord(int delayMicroseconds)
        {
            return Execute(DcaCommand.ConfigureRecord, Dca1000Protocol.BuildRecordConfiguration(delayMicroseconds));
        }

        public ushort StartRecord()
        {
            return Execute(DcaCommand.StartRecord, null);
        }

        public ushort StopRecord()
        {
            return Execute(DcaCommand.StopRecord, null);
        }

        public bool DrainAsyncStatuses(
            int quietMilliseconds,
            int maximumWaitMilliseconds)
        {
            lock (_sync)
            {
                ThrowIfDisposed();
                Stopwatch totalWait = Stopwatch.StartNew();
                Stopwatch quietWait = Stopwatch.StartNew();
                var responseBuffer = new byte[64];
                while (totalWait.ElapsedMilliseconds < maximumWaitMilliseconds)
                {
                    int remainingQuiet = Math.Max(
                        1,
                        quietMilliseconds - (int)quietWait.ElapsedMilliseconds);
                    int remainingTotal = Math.Max(
                        1,
                        maximumWaitMilliseconds - (int)totalWait.ElapsedMilliseconds);
                    _socket.ReceiveTimeout = Math.Min(remainingQuiet, remainingTotal);
                    EndPoint sender = new IPEndPoint(IPAddress.Any, 0);
                    int received;
                    try
                    {
                        received = _socket.ReceiveFrom(responseBuffer, ref sender);
                    }
                    catch (SocketException exception)
                    {
                        if (exception.SocketErrorCode == SocketError.TimedOut ||
                            exception.SocketErrorCode == SocketError.WouldBlock)
                        {
                            if (quietWait.ElapsedMilliseconds >= quietMilliseconds)
                            {
                                return true;
                            }

                            continue;
                        }

                        throw new Dca1000Exception("排空 DCA1000 异步状态失败。", exception);
                    }

                    var senderAddress = sender as IPEndPoint;
                    if (senderAddress == null ||
                        !senderAddress.Address.Equals(_options.DeviceAddress) ||
                        senderAddress.Port != _options.ConfigPort)
                    {
                        continue;
                    }

                    DcaResponse response;
                    try
                    {
                        response = Dca1000Protocol.ParseResponse(responseBuffer, received);
                    }
                    catch (FormatException exception)
                    {
                        throw new Dca1000Exception(
                            "DCA1000 控制端口收到来自设备地址的损坏响应。",
                            exception);
                    }

                    quietWait.Restart();

                    if (response.Command == DcaCommand.SystemAsyncStatus)
                    {
                        Action<ushort> handler = AsyncStatus;
                        if (handler != null)
                        {
                            handler(response.Status);
                        }
                    }
                }

                return false;
            }
        }

        public void Dispose()
        {
            lock (_sync)
            {
                if (_disposed)
                {
                    return;
                }

                _disposed = true;
                _socket.Close();
            }
        }

        public static string FormatFpgaVersion(ushort encoded)
        {
            int major = encoded & 0x7F;
            int minor = (encoded >> 7) & 0x7F;
            string mode = (encoded & 0x4000) != 0 ? "Playback" : "Record";
            return string.Format("{0}.{1} ({2})", major, minor, mode);
        }

        private void ThrowIfDisposed()
        {
            if (_disposed)
            {
                throw new ObjectDisposedException("Dca1000Client");
            }
        }
    }
}
