using System;
using System.Collections.Generic;
using System.Threading;

namespace MmwCli
{
    internal sealed class SelfTestTextCliFake : ITextCliTransport
    {
        private readonly List<string> _commands = new List<string>();
        private bool _disposed;

        public SelfTestTextCliFake()
        {
            VersionResponse = "Platform                : xWR68xx\r\nDone\r\n";
        }

        public Action<string> CommandReceived { get; set; }
        public string VersionResponse { get; set; }

        public IList<string> Commands
        {
            get { return _commands.AsReadOnly(); }
        }

        public bool IsOpen { get; private set; }

        public void Open()
        {
            if (_disposed)
            {
                throw new ObjectDisposedException("SelfTestTextCliFake");
            }

            IsOpen = true;
        }

        public string SendCommand(string command, CancellationToken cancellationToken)
        {
            if (!IsOpen)
            {
                throw new InvalidOperationException("测试文本串口尚未打开。");
            }

            cancellationToken.ThrowIfCancellationRequested();
            _commands.Add(command);
            Action<string> handler = CommandReceived;
            if (handler != null)
            {
                handler(command);
            }

            if (string.Equals(command, "version", StringComparison.OrdinalIgnoreCase))
            {
                return VersionResponse;
            }

            return "Done";
        }

        public void Dispose()
        {
            IsOpen = false;
            _disposed = true;
        }
    }
}
