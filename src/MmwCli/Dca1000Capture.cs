using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Net;
using System.Net.Sockets;
using System.Threading;

namespace MmwCli
{
    internal sealed class DcaCaptureStats
    {
        private long _packetsReceived;
        private long _payloadBytesReceived;
        private long _outputBytes;
        private long _sequenceGaps;
        private long _outOfOrderPackets;
        private long _missingBytes;
        private long _discardedBeforeBasePackets;

        public long PacketsReceived
        {
            get { return Interlocked.Read(ref _packetsReceived); }
            internal set { Interlocked.Exchange(ref _packetsReceived, value); }
        }

        public long PayloadBytesReceived
        {
            get { return Interlocked.Read(ref _payloadBytesReceived); }
            internal set { Interlocked.Exchange(ref _payloadBytesReceived, value); }
        }

        public long OutputBytes
        {
            get { return Interlocked.Read(ref _outputBytes); }
            internal set { Interlocked.Exchange(ref _outputBytes, value); }
        }

        public long SequenceGaps
        {
            get { return Interlocked.Read(ref _sequenceGaps); }
            internal set { Interlocked.Exchange(ref _sequenceGaps, value); }
        }

        public long OutOfOrderPackets
        {
            get { return Interlocked.Read(ref _outOfOrderPackets); }
            internal set { Interlocked.Exchange(ref _outOfOrderPackets, value); }
        }

        public long MissingBytes
        {
            get { return Interlocked.Read(ref _missingBytes); }
            internal set { Interlocked.Exchange(ref _missingBytes, value); }
        }

        public long DiscardedBeforeBasePackets
        {
            get { return Interlocked.Read(ref _discardedBeforeBasePackets); }
            internal set { Interlocked.Exchange(ref _discardedBeforeBasePackets, value); }
        }

        public override string ToString()
        {
            return string.Format(
                "packets={0}, payload={1} bytes, output={2} bytes, seq_gaps={3}, " +
                "out_of_order={4}, missing={5} bytes, discarded_before_base={6}",
                PacketsReceived,
                PayloadBytesReceived,
                OutputBytes,
                SequenceGaps,
                OutOfOrderPackets,
                MissingBytes,
                DiscardedBeforeBasePackets);
        }
    }

    internal sealed class Dca1000Capture : IDisposable
    {
        private readonly object _sync = new object();
        private readonly DcaEndpointOptions _options;
        private readonly Dca1000Client _client;
        private readonly ManualResetEvent _firstPacket = new ManualResetEvent(false);
        private readonly ManualResetEvent _receiverDone = new ManualResetEvent(true);
        private Socket _dataSocket;
        private FileStream _output;
        private Thread _receiverThread;
        private volatile bool _stopRequested;
        private volatile bool _recording;
        private long _firstPacketTimestamp;
        private long _lastPacketTimestamp;
        private volatile Exception _receiverError;
        private ulong _baseByteOffset;
        private bool _baseByteOffsetSet;
        private long _highestOutputOffset;
        private uint _lastSequence;
        private bool _lastSequenceSet;
        private readonly List<ByteRange> _receivedRanges = new List<ByteRange>();
        private bool _disposed;

        public Dca1000Capture(DcaEndpointOptions options, Dca1000Client client)
        {
            _options = options;
            _client = client;
            Stats = new DcaCaptureStats();
        }

        public DcaCaptureStats Stats { get; private set; }

        public bool IsRecording
        {
            get { return _recording; }
        }

        public void Start(string outputPath)
        {
            Start(outputPath, false);
        }

        internal void StartNew(string outputPath)
        {
            Start(outputPath, true);
        }

        private void Start(string outputPath, bool failIfOutputExists)
        {
            lock (_sync)
            {
                ThrowIfDisposed();
                if (_recording)
                {
                    throw new InvalidOperationException("DCA1000 已在录制；请先停止当前录制。");
                }

                string fullPath = Path.GetFullPath(outputPath);
                string parent = Path.GetDirectoryName(fullPath);
                if (!string.IsNullOrEmpty(parent))
                {
                    Directory.CreateDirectory(parent);
                }

                _output = new FileStream(
                    fullPath,
                    failIfOutputExists ? FileMode.CreateNew : FileMode.Create,
                    FileAccess.Write,
                    FileShare.Read,
                    1024 * 1024);
                _dataSocket = new Socket(AddressFamily.InterNetwork, SocketType.Dgram, ProtocolType.Udp);
                _dataSocket.ReceiveTimeout = 250;
                try
                {
                    _dataSocket.ReceiveBufferSize = 16 * 1024 * 1024;
                }
                catch (SocketException)
                {
                }

                try
                {
                    _dataSocket.Bind(new IPEndPoint(_options.HostAddress, _options.DataPort));
                }
                catch (Exception exception)
                {
                    _dataSocket.Close();
                    _output.Dispose();
                    _dataSocket = null;
                    _output = null;
                    throw new Dca1000Exception(string.Format(
                        "无法绑定 DCA1000 数据端口 {0}:{1}。",
                        _options.HostAddress,
                        _options.DataPort), exception);
                }

                Stats = new DcaCaptureStats();
                _stopRequested = false;
                _recording = true;
                _receiverError = null;
                Interlocked.Exchange(ref _firstPacketTimestamp, 0);
                Interlocked.Exchange(ref _lastPacketTimestamp, 0);
                _baseByteOffsetSet = false;
                _highestOutputOffset = 0;
                _lastSequenceSet = false;
                _receivedRanges.Clear();
                _firstPacket.Reset();
                _receiverDone.Reset();
                _receiverThread = new Thread(ReceiveLoop);
                _receiverThread.IsBackground = true;
                _receiverThread.Name = "mmwcli DCA1000 receiver";
                _receiverThread.Start();

                ushort status;
                try
                {
                    status = _client.StartRecord();
                }
                catch (Exception startException)
                {
                    Exception stopException = null;
                    try
                    {
                        ushort stopStatus = _client.StopRecord();
                        if (stopStatus != 0)
                        {
                            stopException = new Dca1000Exception(
                                "DCA1000 start 未确认后的 stop 返回状态: " + stopStatus);
                        }
                    }
                    catch (Exception exception)
                    {
                        stopException = exception;
                    }

                    try
                    {
                        StopReceiverOnly();
                    }
                    catch (Exception exception)
                    {
                        stopException = CombineExceptions(stopException, exception);
                    }
                    if (stopException != null)
                    {
                        throw new Dca1000Exception(
                            "DCA1000 start record 结果未知，且一次性 stop 清理也失败。禁止自动重试 start。",
                            new AggregateException(startException, stopException));
                    }

                    throw new Dca1000Exception(
                        "DCA1000 start record 未确认；未重试 start，并已发送一次 stop 收敛状态。",
                        startException);
                }

                if (status != 0)
                {
                    Exception stopException = null;
                    try
                    {
                        ushort stopStatus = _client.StopRecord();
                        if (stopStatus != 0)
                        {
                            stopException = new Dca1000Exception(
                                "DCA1000 start 返回失败状态后的 stop 也失败，状态: " + stopStatus);
                        }
                    }
                    catch (Exception exception)
                    {
                        stopException = exception;
                    }

                    try
                    {
                        StopReceiverOnly();
                    }
                    catch (Exception exception)
                    {
                        stopException = CombineExceptions(stopException, exception);
                    }
                    var startStatusException = new Dca1000Exception(
                        "DCA1000 start record 失败，状态: " + status);
                    if (stopException != null)
                    {
                        throw new Dca1000Exception(
                            "DCA1000 start record 失败，且一次性 stop 清理也失败。禁止自动重试 start。",
                            new AggregateException(startStatusException, stopException));
                    }

                    throw new Dca1000Exception(
                        "DCA1000 start record 失败，未重试 start，并已发送一次 stop 收敛状态。",
                        startStatusException);
                }
            }
        }

        public bool WaitUntilIdle(int startTimeoutMilliseconds, int idleMilliseconds, Func<bool> cancelRequested)
        {
            return WaitUntilIdle(
                startTimeoutMilliseconds,
                idleMilliseconds,
                0,
                0,
                cancelRequested);
        }

        public bool WaitUntilIdle(
            int startTimeoutMilliseconds,
            int idleMilliseconds,
            double minimumStreamingMilliseconds,
            Func<bool> cancelRequested)
        {
            return WaitUntilIdle(
                startTimeoutMilliseconds,
                idleMilliseconds,
                minimumStreamingMilliseconds,
                0,
                cancelRequested);
        }

        public bool WaitUntilIdle(
            int startTimeoutMilliseconds,
            int idleMilliseconds,
            double minimumStreamingMilliseconds,
            long maximumStreamingMilliseconds,
            Func<bool> cancelRequested)
        {
            Stopwatch firstPacketWait = Stopwatch.StartNew();
            while (_recording && !_firstPacket.WaitOne(100))
            {
                if ((cancelRequested != null && cancelRequested()) ||
                    firstPacketWait.ElapsedMilliseconds >= startTimeoutMilliseconds)
                {
                    return false;
                }

                ThrowReceiverError();
            }

            Stopwatch streamingWait = Stopwatch.StartNew();
            while (_recording)
            {
                if (cancelRequested != null && cancelRequested())
                {
                    return false;
                }

                ThrowReceiverError();
                if (maximumStreamingMilliseconds > 0 &&
                    streamingWait.ElapsedMilliseconds >= maximumStreamingMilliseconds)
                {
                    throw new Dca1000Exception(string.Format(
                        "有限帧数据持续超过计划上限 {0} ms；拒绝无限等待异常数据流。",
                        maximumStreamingMilliseconds));
                }

                long lastPacketTimestamp = Interlocked.Read(ref _lastPacketTimestamp);
                if (ElapsedMilliseconds(lastPacketTimestamp, Stopwatch.GetTimestamp()) >= idleMilliseconds)
                {
                    long firstPacketTimestamp = Interlocked.Read(ref _firstPacketTimestamp);
                    double streamingMilliseconds = ElapsedMillisecondsPrecise(
                        firstPacketTimestamp,
                        lastPacketTimestamp);
                    if (streamingMilliseconds < minimumStreamingMilliseconds)
                    {
                        throw new Dca1000Exception(string.Format(
                            "DCA1000 数据在有限帧计划完成前静默：首末包跨度约 {0:F3} ms，" +
                            "至少应跨越 {1:F3} ms。",
                            streamingMilliseconds,
                            minimumStreamingMilliseconds));
                    }

                    return true;
                }

                Thread.Sleep(100);
            }

            return true;
        }

        public bool WaitForDrain(int maximumWaitMilliseconds, int idleMilliseconds)
        {
            Stopwatch drainWait = Stopwatch.StartNew();
            while (_recording)
            {
                ThrowReceiverError();
                long lastPacketTimestamp = Interlocked.Read(ref _lastPacketTimestamp);
                long idleForMilliseconds = ElapsedMilliseconds(
                    lastPacketTimestamp,
                    Stopwatch.GetTimestamp());
                if (idleForMilliseconds >= idleMilliseconds)
                {
                    return true;
                }

                if (drainWait.ElapsedMilliseconds >= maximumWaitMilliseconds)
                {
                    return false;
                }

                Thread.Sleep(20);
            }

            return true;
        }

        public void Stop()
        {
            lock (_sync)
            {
                if (!_recording)
                {
                    return;
                }

                Exception commandError = null;
                try
                {
                    ushort status = _client.StopRecord();
                    if (status != 0)
                    {
                        commandError = new Dca1000Exception("DCA1000 stop record 失败，状态: " + status);
                    }
                }
                catch (Exception exception)
                {
                    commandError = exception;
                }

                Exception receiverStopError = null;
                try
                {
                    StopReceiverOnly();
                }
                catch (Exception exception)
                {
                    receiverStopError = exception;
                }

                Exception receiverError = _receiverError == null
                    ? null
                    : new Dca1000Exception("DCA1000 数据接收线程失败。", _receiverError);
                Exception stopError = CombineExceptions(
                    CombineExceptions(commandError, receiverStopError),
                    receiverError);
                if (stopError != null)
                {
                    throw stopError;
                }
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

                if (_recording)
                {
                    try
                    {
                        Stop();
                    }
                    catch (Exception)
                    {
                    }
                }

                if (_receiverThread != null && _receiverThread.IsAlive)
                {
                    _stopRequested = true;
                    if (_dataSocket != null)
                    {
                        try
                        {
                            _dataSocket.Close();
                        }
                        catch (Exception)
                        {
                        }
                    }

                    if (_output != null)
                    {
                        try
                        {
                            _output.Dispose();
                        }
                        catch (Exception)
                        {
                        }
                    }

                    _receiverDone.WaitOne(1000);
                }

                bool receiverExited = _receiverThread == null || !_receiverThread.IsAlive;
                if (receiverExited)
                {
                    if (_output != null)
                    {
                        try
                        {
                            _output.Dispose();
                        }
                        catch (Exception)
                        {
                        }
                    }

                    _output = null;
                    _dataSocket = null;
                    _receiverThread = null;
                    _firstPacket.Dispose();
                    _receiverDone.Dispose();
                }

                _disposed = true;
            }
        }

        private void ReceiveLoop()
        {
            var datagram = new byte[65536];
            try
            {
                while (!_stopRequested)
                {
                    EndPoint sender = new IPEndPoint(IPAddress.Any, 0);
                    int length;
                    try
                    {
                        length = _dataSocket.ReceiveFrom(datagram, ref sender);
                    }
                    catch (SocketException exception)
                    {
                        if (exception.SocketErrorCode == SocketError.TimedOut ||
                            exception.SocketErrorCode == SocketError.WouldBlock)
                        {
                            continue;
                        }

                        if (_stopRequested || exception.SocketErrorCode == SocketError.Interrupted)
                        {
                            break;
                        }

                        throw;
                    }
                    catch (ObjectDisposedException)
                    {
                        break;
                    }

                    var senderIp = sender as IPEndPoint;
                    if (senderIp == null || !senderIp.Address.Equals(_options.DeviceAddress) ||
                        length <= Dca1000Protocol.NetworkDataHeaderSize)
                    {
                        continue;
                    }

                    uint sequence = Dca1000Protocol.ReadSequence(datagram, length);
                    ulong byteOffset = Dca1000Protocol.ReadByteOffset(datagram, length);
                    int payloadLength = length - Dca1000Protocol.NetworkDataHeaderSize;

                    if (!_baseByteOffsetSet)
                    {
                        _baseByteOffset = byteOffset;
                        _baseByteOffsetSet = true;
                    }

                    if (byteOffset < _baseByteOffset)
                    {
                        Stats.OutOfOrderPackets++;
                        Stats.DiscardedBeforeBasePackets++;
                        continue;
                    }

                    ulong relativeOffsetUnsigned = byteOffset - _baseByteOffset;
                    if (relativeOffsetUnsigned > long.MaxValue)
                    {
                        throw new IOException("DCA1000 字节偏移超过本地文件限制。");
                    }

                    long relativeOffset = (long)relativeOffsetUnsigned;
                    if (relativeOffset > long.MaxValue - payloadLength)
                    {
                        throw new IOException("DCA1000 数据包末端偏移超过本地文件限制。");
                    }

                    _output.Position = relativeOffset;
                    _output.Write(datagram, Dca1000Protocol.NetworkDataHeaderSize, payloadLength);
                    long packetEnd = relativeOffset + payloadLength;
                    AddReceivedRange(relativeOffset, packetEnd);
                    if (packetEnd > _highestOutputOffset)
                    {
                        _highestOutputOffset = packetEnd;
                    }

                    bool advanceSequence = true;
                    if (_lastSequenceSet)
                    {
                        uint expected = unchecked(_lastSequence + 1U);
                        if (sequence != expected)
                        {
                            if (unchecked(sequence - expected) < 0x80000000U)
                            {
                                Stats.SequenceGaps += unchecked((uint)(sequence - expected));
                            }
                            else
                            {
                                Stats.OutOfOrderPackets++;
                                advanceSequence = false;
                            }
                        }
                    }

                    if (advanceSequence)
                    {
                        _lastSequence = sequence;
                        _lastSequenceSet = true;
                    }

                    Stats.PacketsReceived++;
                    Stats.PayloadBytesReceived += payloadLength;
                    long packetTimestamp = Stopwatch.GetTimestamp();
                    Interlocked.CompareExchange(ref _firstPacketTimestamp, packetTimestamp, 0);
                    Interlocked.Exchange(ref _lastPacketTimestamp, packetTimestamp);
                    _firstPacket.Set();
                }
            }
            catch (Exception exception)
            {
                _receiverError = exception;
            }
            finally
            {
                _receiverDone.Set();
            }
        }

        private void StopReceiverOnly()
        {
            _stopRequested = true;
            if (_dataSocket != null)
            {
                _dataSocket.Close();
            }

            bool receiverExited = _receiverDone.WaitOne(3000);
            if (!receiverExited && _receiverThread != null && _receiverThread.IsAlive)
            {
                _receiverThread.Join(1000);
            }

            if (_receiverThread != null && _receiverThread.IsAlive)
            {
                _recording = false;
                throw new Dca1000Exception(
                    "DCA1000 数据接收线程未在 4000 ms 内退出；临时文件不会发布为最终输出。");
            }

            Exception outputError = null;
            FileStream output = _output;
            try
            {
                Stats.MissingBytes = CalculateMissingBytes();
                if (output != null)
                {
                    if (_highestOutputOffset > output.Length)
                    {
                        output.SetLength(_highestOutputOffset);
                    }

                    Stats.OutputBytes = output.Length;
                    output.Flush();
                }
            }
            catch (Exception exception)
            {
                outputError = exception;
            }
            finally
            {
                if (output != null)
                {
                    try
                    {
                        output.Dispose();
                    }
                    catch (Exception exception)
                    {
                        outputError = CombineExceptions(outputError, exception);
                    }
                }

                _dataSocket = null;
                _output = null;
                _receiverThread = null;
                _recording = false;
            }

            if (outputError != null)
            {
                throw new Dca1000Exception(
                    "DCA1000 临时输出 flush/close 失败；文件不会发布为最终输出。",
                    outputError);
            }
        }

        private void ThrowReceiverError()
        {
            if (_receiverError != null)
            {
                throw new Dca1000Exception("DCA1000 数据接收线程失败。", _receiverError);
            }
        }

        private void ThrowIfDisposed()
        {
            if (_disposed)
            {
                throw new ObjectDisposedException("Dca1000Capture");
            }
        }

        private static long ElapsedMilliseconds(long startTimestamp, long endTimestamp)
        {
            return (long)ElapsedMillisecondsPrecise(startTimestamp, endTimestamp);
        }

        private static double ElapsedMillisecondsPrecise(long startTimestamp, long endTimestamp)
        {
            if (startTimestamp <= 0 || endTimestamp <= startTimestamp)
            {
                return 0;
            }

            return ((double)(endTimestamp - startTimestamp) * 1000.0) / Stopwatch.Frequency;
        }

        private static Exception CombineExceptions(Exception first, Exception second)
        {
            if (first == null)
            {
                return second;
            }

            if (second == null)
            {
                return first;
            }

            return new Dca1000Exception(
                "DCA1000 清理发生多个错误。",
                new AggregateException(first, second));
        }

        private void AddReceivedRange(long start, long end)
        {
            int index = 0;
            while (index < _receivedRanges.Count && _receivedRanges[index].End < start)
            {
                index++;
            }

            long mergedStart = start;
            long mergedEnd = end;
            while (index < _receivedRanges.Count && _receivedRanges[index].Start <= mergedEnd)
            {
                ByteRange existing = _receivedRanges[index];
                mergedStart = Math.Min(mergedStart, existing.Start);
                mergedEnd = Math.Max(mergedEnd, existing.End);
                _receivedRanges.RemoveAt(index);
            }

            _receivedRanges.Insert(index, new ByteRange(mergedStart, mergedEnd));
        }

        private long CalculateMissingBytes()
        {
            long missing = 0;
            long coveredUntil = 0;
            for (int index = 0; index < _receivedRanges.Count; index++)
            {
                ByteRange range = _receivedRanges[index];
                if (range.Start > coveredUntil)
                {
                    missing += range.Start - coveredUntil;
                }

                if (range.End > coveredUntil)
                {
                    coveredUntil = range.End;
                }
            }

            if (_highestOutputOffset > coveredUntil)
            {
                missing += _highestOutputOffset - coveredUntil;
            }

            return missing;
        }

        private sealed class ByteRange
        {
            public ByteRange(long start, long end)
            {
                Start = start;
                End = end;
            }

            public long Start { get; private set; }
            public long End { get; private set; }
        }
    }
}
