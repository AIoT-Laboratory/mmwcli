using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.IO.Ports;
using System.Text;
using System.Threading;

namespace MmwCli
{
    internal enum CliTerminalResult
    {
        None,
        Done,
        Error
    }

    internal interface ITextCliTransport : IDisposable
    {
        void Open();
        string SendCommand(string command, CancellationToken cancellationToken);
    }

    internal sealed class DemoCli : ITextCliTransport
    {
        private readonly SerialPort _serial;
        private readonly int _timeoutMilliseconds;

        public DemoCli(string portName, int baudRate, int timeoutMilliseconds)
        {
            if (string.IsNullOrWhiteSpace(portName))
            {
                throw new ArgumentException("串口名不能为空。", "portName");
            }

            int numericPort;
            if (int.TryParse(portName, out numericPort))
            {
                portName = "COM" + numericPort;
            }

            _timeoutMilliseconds = timeoutMilliseconds;
            _serial = new SerialPort(portName, baudRate, Parity.None, 8, StopBits.One);
            _serial.Handshake = Handshake.None;
            _serial.NewLine = "\n";
            _serial.ReadTimeout = 100;
            _serial.WriteTimeout = timeoutMilliseconds;
        }

        public string PortName
        {
            get { return _serial.PortName; }
        }

        public void Open()
        {
            try
            {
                _serial.Open();
                Thread.Sleep(100);
                _serial.DiscardInBuffer();
                _serial.DiscardOutBuffer();
            }
            catch (Exception exception)
            {
                throw new IOException("无法打开 68xx CLI 串口 " + _serial.PortName + "。", exception);
            }
        }

        public string SendCommand(string command)
        {
            return SendCommand(command, CancellationToken.None);
        }

        public string SendCommand(string command, CancellationToken cancellationToken)
        {
            if (!_serial.IsOpen)
            {
                throw new InvalidOperationException("串口尚未打开。");
            }

            cancellationToken.ThrowIfCancellationRequested();
            command = command.Trim();
            _serial.DiscardInBuffer();
            _serial.Write(command + "\n");

            var output = new StringBuilder();
            var stopwatch = Stopwatch.StartNew();
            bool receivedAny = false;
            CliTerminalResult terminal = CliTerminalResult.None;
            while (stopwatch.ElapsedMilliseconds < _timeoutMilliseconds)
            {
                cancellationToken.ThrowIfCancellationRequested();
                string chunk = _serial.ReadExisting();
                if (!string.IsNullOrEmpty(chunk))
                {
                    output.Append(chunk);
                    receivedAny = true;
                    string current = output.ToString();
                    terminal = FindTerminalResponse(current);
                    if (terminal != CliTerminalResult.None)
                    {
                        break;
                    }
                }

                Thread.Sleep(20);
            }

            string response = output.ToString();
            if (!receivedAny)
            {
                throw new TimeoutException(string.Format(
                    "命令在 {0} ms 内没有响应: {1}",
                    _timeoutMilliseconds,
                    command));
            }

            if (terminal == CliTerminalResult.None)
            {
                throw new TimeoutException(string.Format(
                    "命令在 {0} ms 内没有收到明确的 Done/Error 终态，执行结果未知且不会自动重试: {1}\n{2}",
                    _timeoutMilliseconds,
                    command,
                    response.Trim()));
            }

            if (terminal == CliTerminalResult.Error)
            {
                throw new IOException("68xx 拒绝命令 " + command + ":\n" + response.Trim());
            }

            return response;
        }

        public void ApplyFile(string path)
        {
            if (!File.Exists(path))
            {
                throw new FileNotFoundException("找不到配置文件。", path);
            }

            string[] lines = File.ReadAllLines(path);
            IList<string> commands = ParseConfiguration(lines);
            ApplyCommands(commands);
        }

        public void ApplyCommands(IList<string> commands)
        {
            ApplyCommands(commands, CancellationToken.None);
        }

        public void ApplyCommands(IList<string> commands, CancellationToken cancellationToken)
        {
            if (commands == null)
            {
                throw new ArgumentNullException("commands");
            }

            for (int index = 0; index < commands.Count; index++)
            {
                cancellationToken.ThrowIfCancellationRequested();
                string command = commands[index];
                Console.WriteLine("[{0}/{1}] {2}", index + 1, commands.Count, command);
                string response = SendCommand(command, cancellationToken);
                string summary = SummarizeResponse(response, command);
                if (!string.IsNullOrEmpty(summary))
                {
                    Console.WriteLine("  " + summary);
                }
            }
        }

        public void Dispose()
        {
            if (_serial.IsOpen)
            {
                _serial.Close();
            }

            _serial.Dispose();
        }

        internal static IList<string> ParseConfiguration(IEnumerable<string> lines)
        {
            var commands = new List<string>();
            foreach (string rawLine in lines)
            {
                string line = rawLine == null ? string.Empty : rawLine.Trim();
                if (line.Length == 0 || line.StartsWith("%", StringComparison.Ordinal) ||
                    line.StartsWith("#", StringComparison.Ordinal) ||
                    line.StartsWith("//", StringComparison.Ordinal))
                {
                    continue;
                }

                int inlineComment = line.IndexOf(" //", StringComparison.Ordinal);
                if (inlineComment >= 0)
                {
                    line = line.Substring(0, inlineComment).TrimEnd();
                }

                if (line.Length != 0)
                {
                    commands.Add(line);
                }
            }

            return commands;
        }

        internal static CliTerminalResult FindTerminalResponse(string response)
        {
            string[] lines = response.Replace("\r", string.Empty).Split('\n');
            for (int index = 0; index < lines.Length; index++)
            {
                string line = lines[index].Trim();
                if (string.Equals(line, "Done", StringComparison.OrdinalIgnoreCase))
                {
                    return CliTerminalResult.Done;
                }

                if (string.Equals(line, "Error", StringComparison.OrdinalIgnoreCase) ||
                    line.StartsWith("Error ", StringComparison.OrdinalIgnoreCase) ||
                    line.StartsWith("Error:", StringComparison.OrdinalIgnoreCase) ||
                    line.StartsWith("Error\t", StringComparison.OrdinalIgnoreCase))
                {
                    return CliTerminalResult.Error;
                }
            }

            return CliTerminalResult.None;
        }

        private static string SummarizeResponse(string response, string command)
        {
            string[] lines = response.Replace("\r", string.Empty).Split('\n');
            var useful = new List<string>();
            foreach (string raw in lines)
            {
                string line = raw.Trim();
                if (line.Length == 0 || line == command || line.IndexOf(":/>", StringComparison.Ordinal) >= 0)
                {
                    continue;
                }

                useful.Add(line);
            }

            return string.Join(" | ", useful.ToArray());
        }
    }
}
