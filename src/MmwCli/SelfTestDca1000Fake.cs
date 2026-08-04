using System;
using System.IO;
using System.Net;
using System.Net.Sockets;
using System.Threading;

namespace MmwCli
{
    internal sealed class SelfTestDca1000Fake : IDisposable
    {
        public const ushort AliveAsyncStatus = 0x0100;

        private readonly object _errorSync = new object();
        private readonly ManualResetEvent _aliveAsyncObserved = new ManualResetEvent(false);
        private readonly Socket _controlSocket;
        private readonly Socket _dataSocket;
        private Thread _controlThread;
        private Exception _controlError;
        private volatile bool _stopping;
        private bool _started;
        private int _aliveRequests;
        private int _startRequests;
        private int _stopRequests;

        public SelfTestDca1000Fake()
        {
            HostAddress = IPAddress.Parse("127.0.0.1");
            DeviceAddress = IPAddress.Parse("127.0.0.2");
            int configPort;
            _controlSocket = BindDeviceSocketOnHostFreePort(0, out configPort);
            ConfigPort = configPort;
            try
            {
                int dataPort;
                _dataSocket = BindDeviceSocketOnHostFreePort(ConfigPort, out dataPort);
                DataPort = dataPort;
            }
            catch
            {
                _controlSocket.Close();
                _aliveAsyncObserved.Dispose();
                throw;
            }

            _controlSocket.ReceiveTimeout = 250;
        }

        public IPAddress HostAddress { get; private set; }
        public IPAddress DeviceAddress { get; private set; }
        public int ConfigPort { get; private set; }
        public int DataPort { get; private set; }
        public bool RequireAliveAsyncAcknowledgement { get; set; }
        public bool DropStartResponse { get; set; }
        public ushort StartResponseStatus { get; set; }
        public ushort? AsyncStatusBeforeStopResponse { get; set; }
        public ushort? AsyncStatusAfterStopResponse { get; set; }
        public ushort? AsyncStatusBeforeConfigureRecordResponse { get; set; }

        public int AliveRequests
        {
            get { return Interlocked.CompareExchange(ref _aliveRequests, 0, 0); }
        }

        public int StartRequests
        {
            get { return Interlocked.CompareExchange(ref _startRequests, 0, 0); }
        }

        public int StopRequests
        {
            get { return Interlocked.CompareExchange(ref _stopRequests, 0, 0); }
        }

        public void Start()
        {
            if (_started)
            {
                throw new InvalidOperationException("DCA1000 fake card 已启动。");
            }

            _started = true;
            _controlThread = new Thread(ControlLoop);
            _controlThread.IsBackground = true;
            _controlThread.Name = "mmwcli DCA1000 self-test fake card";
            _controlThread.Start();
        }

        public void SendData(uint sequence, ulong byteOffset, byte[] payload)
        {
            if (payload == null || payload.Length == 0)
            {
                throw new ArgumentException("测试 ADC payload 不能为空。", "payload");
            }

            if (byteOffset > 0xFFFFFFFFFFFFUL)
            {
                throw new ArgumentOutOfRangeException("byteOffset", "DCA1000 byte offset 必须适合 u48。");
            }

            AssertHealthy();
            byte[] datagram = new byte[Dca1000Protocol.NetworkDataHeaderSize + payload.Length];
            WriteUInt32(datagram, 0, sequence);
            for (int index = 0; index < 6; index++)
            {
                datagram[4 + index] = (byte)(byteOffset >> (index * 8));
            }

            Buffer.BlockCopy(payload, 0, datagram, Dca1000Protocol.NetworkDataHeaderSize, payload.Length);
            _dataSocket.SendTo(datagram, new IPEndPoint(HostAddress, DataPort));
        }

        public void AcknowledgeAliveAsyncStatus()
        {
            _aliveAsyncObserved.Set();
        }

        public void AssertHealthy()
        {
            Exception error;
            lock (_errorSync)
            {
                error = _controlError;
            }

            if (error != null)
            {
                throw new Exception("DCA1000 fake card 控制线程失败。", error);
            }
        }

        public void Dispose()
        {
            _stopping = true;
            _aliveAsyncObserved.Set();
            _controlSocket.Close();
            _dataSocket.Close();
            if (_controlThread != null && _controlThread.IsAlive)
            {
                _controlThread.Join(1000);
            }

            _aliveAsyncObserved.Dispose();
        }

        private Socket BindDeviceSocketOnHostFreePort(int excludedPort, out int port)
        {
            for (int attempt = 0; attempt < 100; attempt++)
            {
                using (var hostReservation = new Socket(AddressFamily.InterNetwork, SocketType.Dgram, ProtocolType.Udp))
                {
                    hostReservation.Bind(new IPEndPoint(HostAddress, 0));
                    int candidate = ((IPEndPoint)hostReservation.LocalEndPoint).Port;
                    if (candidate == excludedPort)
                    {
                        continue;
                    }

                    var deviceSocket = new Socket(AddressFamily.InterNetwork, SocketType.Dgram, ProtocolType.Udp);
                    try
                    {
                        deviceSocket.Bind(new IPEndPoint(DeviceAddress, candidate));
                        port = candidate;
                        return deviceSocket;
                    }
                    catch (SocketException)
                    {
                        deviceSocket.Close();
                    }
                }
            }

            throw new IOException("无法为 DCA1000 回环测试分配成对 UDP 端口。");
        }

        private void ControlLoop()
        {
            var buffer = new byte[1024];
            try
            {
                while (!_stopping)
                {
                    EndPoint sender = new IPEndPoint(IPAddress.Any, 0);
                    int length;
                    try
                    {
                        length = _controlSocket.ReceiveFrom(buffer, ref sender);
                    }
                    catch (SocketException exception)
                    {
                        if (_stopping)
                        {
                            break;
                        }

                        if (exception.SocketErrorCode == SocketError.TimedOut ||
                            exception.SocketErrorCode == SocketError.WouldBlock)
                        {
                            continue;
                        }

                        throw;
                    }
                    catch (ObjectDisposedException)
                    {
                        break;
                    }

                    ValidateRequest(buffer, length);
                    DcaCommand command = (DcaCommand)Dca1000Protocol.ReadUInt16(buffer, 2);
                    if (command == DcaCommand.SystemAliveness)
                    {
                        Interlocked.Increment(ref _aliveRequests);
                        SendResponse(sender, DcaCommand.SystemAsyncStatus, AliveAsyncStatus);
                        if (RequireAliveAsyncAcknowledgement && !_aliveAsyncObserved.WaitOne(1000))
                        {
                            throw new TimeoutException("客户端未在 alive 响应前分发 DCA1000 异步状态。");
                        }
                    }
                    else if (command == DcaCommand.StartRecord)
                    {
                        Interlocked.Increment(ref _startRequests);
                        if (DropStartResponse)
                        {
                            continue;
                        }
                    }
                    else if (command == DcaCommand.StopRecord)
                    {
                        Interlocked.Increment(ref _stopRequests);
                        if (AsyncStatusBeforeStopResponse.HasValue)
                        {
                            SendResponse(
                                sender,
                                DcaCommand.SystemAsyncStatus,
                                AsyncStatusBeforeStopResponse.Value);
                        }
                    }
                    else if (command == DcaCommand.ConfigureRecord &&
                             AsyncStatusBeforeConfigureRecordResponse.HasValue)
                    {
                        SendResponse(
                            sender,
                            DcaCommand.SystemAsyncStatus,
                            AsyncStatusBeforeConfigureRecordResponse.Value);
                    }

                    ushort responseStatus = command == DcaCommand.StartRecord
                        ? StartResponseStatus
                        : (ushort)0;
                    SendResponse(sender, command, responseStatus);
                    if (command == DcaCommand.StopRecord && AsyncStatusAfterStopResponse.HasValue)
                    {
                        SendResponse(
                            sender,
                            DcaCommand.SystemAsyncStatus,
                            AsyncStatusAfterStopResponse.Value);
                    }
                }
            }
            catch (Exception exception)
            {
                if (!_stopping)
                {
                    lock (_errorSync)
                    {
                        _controlError = exception;
                    }
                }
            }
        }

        private void SendResponse(EndPoint destination, DcaCommand command, ushort status)
        {
            byte[] response = new byte[Dca1000Protocol.FixedPacketSize];
            WriteUInt16(response, 0, Dca1000Protocol.Header);
            WriteUInt16(response, 2, (ushort)command);
            WriteUInt16(response, 4, status);
            WriteUInt16(response, 6, Dca1000Protocol.Footer);
            _controlSocket.SendTo(response, destination);
        }

        private static void ValidateRequest(byte[] packet, int length)
        {
            if (length < Dca1000Protocol.FixedPacketSize)
            {
                throw new FormatException("DCA1000 fake card 收到过短控制包。");
            }

            int payloadLength = Dca1000Protocol.ReadUInt16(packet, 4);
            if (length != Dca1000Protocol.FixedPacketSize + payloadLength ||
                Dca1000Protocol.ReadUInt16(packet, 0) != Dca1000Protocol.Header ||
                Dca1000Protocol.ReadUInt16(packet, length - 2) != Dca1000Protocol.Footer)
            {
                throw new FormatException("DCA1000 fake card 收到无效控制包。");
            }
        }

        private static void WriteUInt16(byte[] buffer, int offset, ushort value)
        {
            buffer[offset] = (byte)(value & 0xFF);
            buffer[offset + 1] = (byte)(value >> 8);
        }

        private static void WriteUInt32(byte[] buffer, int offset, uint value)
        {
            buffer[offset] = (byte)(value & 0xFF);
            buffer[offset + 1] = (byte)((value >> 8) & 0xFF);
            buffer[offset + 2] = (byte)((value >> 16) & 0xFF);
            buffer[offset + 3] = (byte)((value >> 24) & 0xFF);
        }
    }
}
