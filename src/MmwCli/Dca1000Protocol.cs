using System;

namespace MmwCli
{
    internal enum DcaCommand : ushort
    {
        ResetFpga = 0x01,
        ResetRadar = 0x02,
        ConfigureFpga = 0x03,
        ConfigureEeprom = 0x04,
        StartRecord = 0x05,
        StopRecord = 0x06,
        StartPlayback = 0x07,
        StopPlayback = 0x08,
        SystemAliveness = 0x09,
        SystemAsyncStatus = 0x0A,
        ConfigureRecord = 0x0B,
        ConfigureRadar = 0x0C,
        InitializePlayback = 0x0D,
        ReadFpgaVersion = 0x0E
    }

    internal sealed class DcaResponse
    {
        public DcaResponse(DcaCommand command, ushort status)
        {
            Command = command;
            Status = status;
        }

        public DcaCommand Command { get; private set; }
        public ushort Status { get; private set; }
    }

    internal static class Dca1000Protocol
    {
        public const ushort Header = 0xA55A;
        public const ushort Footer = 0xEEAA;
        public const int FixedPacketSize = 8;
        public const int MaximumPayloadSize = 504;
        public const int NetworkDataHeaderSize = 10;

        public static byte[] BuildRequest(DcaCommand command, byte[] payload)
        {
            payload = payload ?? new byte[0];
            if (payload.Length > MaximumPayloadSize)
            {
                throw new ArgumentOutOfRangeException("payload", "DCA1000 命令负载不能超过 504 字节。");
            }

            byte[] packet = new byte[FixedPacketSize + payload.Length];
            WriteUInt16(packet, 0, Header);
            WriteUInt16(packet, 2, (ushort)command);
            WriteUInt16(packet, 4, (ushort)payload.Length);
            Buffer.BlockCopy(payload, 0, packet, 6, payload.Length);
            WriteUInt16(packet, 6 + payload.Length, Footer);
            return packet;
        }

        public static DcaResponse ParseResponse(byte[] packet, int length)
        {
            if (packet == null || length < FixedPacketSize || length > packet.Length)
            {
                throw new FormatException("DCA1000 响应长度不足 8 字节。");
            }

            ushort header = ReadUInt16(packet, 0);
            ushort command = ReadUInt16(packet, 2);
            ushort status = ReadUInt16(packet, 4);
            ushort footer = ReadUInt16(packet, 6);
            if (header != Header || footer != Footer)
            {
                throw new FormatException(string.Format(
                    "无效 DCA1000 响应边界: header=0x{0:X4}, footer=0x{1:X4}",
                    header,
                    footer));
            }

            return new DcaResponse((DcaCommand)command, status);
        }

        public static byte[] BuildFpgaConfiguration(
            int logMode,
            int lvdsMode,
            int transferMode,
            int captureMode,
            int dataFormat,
            int timer)
        {
            ValidateByte("logMode", logMode, 1, 2);
            ValidateByte("lvdsMode", lvdsMode, 1, 2);
            ValidateByte("transferMode", transferMode, 1, 2);
            ValidateByte("captureMode", captureMode, 1, 2);
            ValidateByte("dataFormat", dataFormat, 1, 3);
            ValidateByte("timer", timer, 0, 255);

            return new[]
            {
                (byte)logMode,
                (byte)lvdsMode,
                (byte)transferMode,
                (byte)captureMode,
                (byte)dataFormat,
                (byte)timer
            };
        }

        public static byte[] BuildRecordConfiguration(int delayMicroseconds)
        {
            if (delayMicroseconds < 5 || delayMicroseconds > 500)
            {
                throw new ArgumentOutOfRangeException(
                    "delayMicroseconds",
                    "DCA1000 packet delay 必须在 5..500 微秒之间。");
            }

            const ushort packetSize = 1470;
            ushort delayCycles = checked((ushort)(delayMicroseconds * 125));
            byte[] payload = new byte[6];
            WriteUInt16(payload, 0, packetSize);
            WriteUInt16(payload, 2, delayCycles);
            WriteUInt16(payload, 4, 0);
            return payload;
        }

        public static uint ReadSequence(byte[] datagram, int length)
        {
            if (datagram == null || length < NetworkDataHeaderSize)
            {
                throw new FormatException("DCA1000 数据包没有完整的 10 字节头。");
            }

            return ReadUInt32(datagram, 0);
        }

        public static ulong ReadByteOffset(byte[] datagram, int length)
        {
            if (datagram == null || length < NetworkDataHeaderSize)
            {
                throw new FormatException("DCA1000 数据包没有完整的 10 字节头。");
            }

            ulong value = 0;
            for (int index = 0; index < 6; index++)
            {
                value |= ((ulong)datagram[4 + index]) << (index * 8);
            }

            return value;
        }

        internal static ushort ReadUInt16(byte[] buffer, int offset)
        {
            return (ushort)(buffer[offset] | (buffer[offset + 1] << 8));
        }

        private static uint ReadUInt32(byte[] buffer, int offset)
        {
            return (uint)(
                buffer[offset] |
                (buffer[offset + 1] << 8) |
                (buffer[offset + 2] << 16) |
                (buffer[offset + 3] << 24));
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

        private static void ValidateByte(string name, int value, int minimum, int maximum)
        {
            if (value < minimum || value > maximum)
            {
                throw new ArgumentOutOfRangeException(
                    name,
                    string.Format("{0} 必须在 {1}..{2} 之间。", name, minimum, maximum));
            }
        }
    }
}
