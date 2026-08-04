using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Threading;

namespace MmwCli
{
    internal static class SelfTest
    {
        private static int _tests;

        public static int Run(string explicitStudioRoot)
        {
            _tests = 0;
            TestRequestPackets();
            TestResponsePackets();
            TestDca1000Loopback();
            TestDca1000BoundedDrain();
            TestDca1000AmbiguousStartCleanup();
            TestDca1000NonzeroStartCleanup();
            TestConfigurationParser();
            TestCliTerminalParser();
            TestTextCapturePlan();
            TestIntegratedTextCapture();
            TestIntegratedWrongPlatformRejected();
            TestEnsureStoppedUnknownResultCleanup();
            TestIntegratedDemoCaptureWithoutVersion();
            TestIntegratedFiniteFrameEarlyIdle();
            TestIntegratedMissingBytesPreventsPublication();
            TestIntegratedFiniteFrameContinuousOverrun();
            TestIntegratedInfiniteFrameUnexpectedIdle();
            TestIntegratedDcaConfigurationAsyncFailure();
            TestIntegratedDcaFatalAsyncStatus();
            TestThrowingCaptureLogIsNonFatal();
            TestIntegratedDrainTimeoutPreventsPublication();
            TestIntegratedCancellationDuringCleanup();
            TestIntegratedTextCaptureCleanup();
            _tests += SelfTestPartFile.Run();
            TestStudioHeadless(explicitStudioRoot);
            Console.WriteLine("离线测试通过: " + _tests);
            return 0;
        }

        private static void TestRequestPackets()
        {
            Equal(
                "5A-A5-09-00-00-00-AA-EE",
                BitConverter.ToString(Dca1000Protocol.BuildRequest(DcaCommand.SystemAliveness, null)),
                "DCA alive golden packet");

            byte[] fpga = Dca1000Protocol.BuildFpgaConfiguration(1, 2, 1, 2, 3, 30);
            Equal(
                "5A-A5-03-00-06-00-01-02-01-02-03-1E-AA-EE",
                BitConverter.ToString(Dca1000Protocol.BuildRequest(DcaCommand.ConfigureFpga, fpga)),
                "DCA 68xx FPGA config golden packet");

            byte[] record = Dca1000Protocol.BuildRecordConfiguration(25);
            Equal(
                "5A-A5-0B-00-06-00-BE-05-35-0C-00-00-AA-EE",
                BitConverter.ToString(Dca1000Protocol.BuildRequest(DcaCommand.ConfigureRecord, record)),
                "DCA packet delay golden packet");

            Throws<ArgumentOutOfRangeException>(
                delegate { Dca1000Protocol.BuildRecordConfiguration(4); },
                "DCA packet delay lower bound");
            Throws<ArgumentOutOfRangeException>(
                delegate { Dca1000Protocol.BuildRecordConfiguration(501); },
                "DCA packet delay upper bound");
        }

        private static void TestResponsePackets()
        {
            byte[] response =
            {
                0x5A, 0xA5, 0x0E, 0x00, 0x81, 0x01, 0xAA, 0xEE
            };
            DcaResponse parsed = Dca1000Protocol.ParseResponse(response, response.Length);
            Equal(DcaCommand.ReadFpgaVersion, parsed.Command, "DCA response command");
            Equal((ushort)0x0181, parsed.Status, "DCA response status");

            byte[] datagram = new byte[13];
            datagram[0] = 0x78;
            datagram[1] = 0x56;
            datagram[2] = 0x34;
            datagram[3] = 0x12;
            datagram[4] = 0x06;
            datagram[5] = 0x05;
            datagram[6] = 0x04;
            datagram[7] = 0x03;
            datagram[8] = 0x02;
            datagram[9] = 0x01;
            Equal((uint)0x12345678, Dca1000Protocol.ReadSequence(datagram, datagram.Length), "ADC sequence LE");
            Equal((ulong)0x010203040506, Dca1000Protocol.ReadByteOffset(datagram, datagram.Length), "ADC u48 offset LE");
        }

        private static void TestConfigurationParser()
        {
            string[] lines =
            {
                "% TI comment",
                "",
                "  flushCfg  ",
                "profileCfg 0 60 7 // inline",
                "# ignored",
                "sensorStart"
            };
            IList<string> commands = DemoCli.ParseConfiguration(lines);
            Equal(3, commands.Count, "cfg command count");
            Equal("flushCfg", commands[0], "cfg trim");
            Equal("profileCfg 0 60 7", commands[1], "cfg inline comment");
            Equal("sensorStart", commands[2], "cfg final command");
        }

        private static void TestCliTerminalParser()
        {
            Equal(
                CliTerminalResult.Done,
                DemoCli.FindTerminalResponse("sensorStart\r\nDone\r\nmmwDemo:/>"),
                "CLI explicit Done terminal");
            Equal(
                CliTerminalResult.Error,
                DemoCli.FindTerminalResponse("sensorStop\r\nError -54\r\nmmwDemo:/>"),
                "CLI explicit Error terminal");
            Equal(
                CliTerminalResult.Error,
                DemoCli.FindTerminalResponse("sensorStart\r\nError: Full configuration is required\r\n"),
                "SDK demo Error colon terminal");
            Equal(
                CliTerminalResult.None,
                DemoCli.FindTerminalResponse("status: no error observed\r\nmmwDemo:/>"),
                "CLI prompt or prose is not a success terminal");
        }

        private static void TestTextCapturePlan()
        {
            TextCliCapturePlan plan = TextCliCapturePlan.FromCommands(new[]
            {
                "dfeDataOutputMode 1",
                "flushCfg",
                "adcCfg 2 1",
                "lvdsStreamCfg -1 0 1 0",
                "frameCfg 0 1 32 100 100 1 0",
                "profileCfg 0 60 7",
                "sensorStart 0"
            });
            Equal(6, plan.ConfigurationCommands.Count, "capture plan pre-start command count");
            Equal("sensorStart 0", plan.StartCommand, "capture plan preserves start command");
            Equal(false, plan.StartWasSynthesized, "capture plan preserves explicit start marker");
            Equal((int?)3, plan.ExpectedDcaDataFormat, "capture plan infers 16-bit DCA format");
            Equal((bool?)true, plan.HardwareLvdsEnabled, "capture plan detects hardware LVDS");
            Equal((bool?)false, plan.InfiniteFrame, "capture plan detects finite frame");
            Equal((int?)100, plan.NumberOfFrames, "capture plan parses frame count");
            Equal((double?)100.0, plan.FramePeriodicityMilliseconds, "capture plan parses frame period");
            Equal(25, new DcaCaptureConfiguration().PacketDelayMicroseconds, "studio-cli DCA delay defaults to TI 25 us");

            TextCliCapturePlan infinite = TextCliCapturePlan.FromCommands(CapturePlanCommands(
                "dfeDataOutputMode 1",
                "adcCfg 2 1",
                "lvdsStreamCfg -1 0 1 0",
                "frameCfg 0 1 32 0 50.5 1 0",
                "sensorStart"));
            Equal((bool?)true, infinite.InfiniteFrame, "capture plan detects infinite frame");
            Equal((int?)0, infinite.NumberOfFrames, "capture plan parses infinite frame count");
            Equal((double?)50.5, infinite.FramePeriodicityMilliseconds, "capture plan parses decimal frame period");

            TextCliCapturePlan synthesized = TextCliCapturePlan.FromCommands(CapturePlanCommands(
                "dfeDataOutputMode 1",
                "adcCfg 2 1",
                "lvdsStreamCfg -1 0 1 0",
                "frameCfg 0 1 32 10 100 1 0",
                null));
            Equal(4, synthesized.ConfigurationCommands.Count, "capture plan keeps commands when start is missing");
            Equal("sensorStart", synthesized.StartCommand, "capture plan synthesizes sensorStart");
            Equal(true, synthesized.StartWasSynthesized, "capture plan marks synthesized start");

            Throws<UsageException>(
                delegate
                {
                    TextCliCapturePlan.FromCommands(CapturePlanCommands(
                        "dfeDataOutputMode 3",
                        "adcCfg 2 1",
                        "lvdsStreamCfg -1 0 1 0",
                        "frameCfg 0 1 32 1 100 1 0",
                        "sensorStart"));
                },
                "capture plan rejects advanced frame mode");
            Throws<UsageException>(
                delegate
                {
                    TextCliCapturePlan.FromCommands(CapturePlanCommands(
                        null,
                        "adcCfg 2 1",
                        "lvdsStreamCfg -1 0 1 0",
                        "frameCfg 0 1 32 3 10 1 0",
                        "sensorStart"));
                },
                "capture plan requires dfeDataOutputMode");
            Throws<UsageException>(
                delegate
                {
                    TextCliCapturePlan.FromCommands(CapturePlanCommands(
                        "dfeDataOutputMode 1",
                        null,
                        "lvdsStreamCfg -1 0 1 0",
                        "frameCfg 0 1 32 10 100 1 0",
                        "sensorStart"));
                },
                "capture plan requires adcCfg");
            Throws<UsageException>(
                delegate
                {
                    TextCliCapturePlan.FromCommands(CapturePlanCommands(
                        "dfeDataOutputMode 1",
                        "adcCfg 1 1",
                        "lvdsStreamCfg -1 0 1 0",
                        "frameCfg 0 1 32 10 100 1 0",
                        "sensorStart"));
                },
                "capture plan rejects non-16-bit ADC");
            Throws<UsageException>(
                delegate
                {
                    TextCliCapturePlan.FromCommands(CapturePlanCommands(
                        "dfeDataOutputMode 1",
                        "adcCfg 2 garbage",
                        "lvdsStreamCfg -1 0 1 0",
                        "frameCfg 0 1 32 10 100 1 0",
                        "sensorStart"));
                },
                "capture plan rejects invalid ADC output format");
            Throws<UsageException>(
                delegate
                {
                    TextCliCapturePlan.FromCommands(CapturePlanCommands(
                        "dfeDataOutputMode 1",
                        "adcCfg 2 1",
                        "lvdsStreamCfg -1 1 1 0",
                        "frameCfg 0 1 32 10 100 1 0",
                        "sensorStart"));
                },
                "capture plan rejects LVDS header");
            Throws<UsageException>(
                delegate
                {
                    TextCliCapturePlan.FromCommands(CapturePlanCommands(
                        "dfeDataOutputMode 1",
                        "adcCfg 2 1",
                        "lvdsStreamCfg 0 0 1 0",
                        "frameCfg 0 1 32 10 100 1 0",
                        "sensorStart"));
                },
                "capture plan rejects non-global legacy LVDS subframe");
            Throws<UsageException>(
                delegate
                {
                    TextCliCapturePlan.FromCommands(CapturePlanCommands(
                        "dfeDataOutputMode 1",
                        "adcCfg 2 1",
                        "lvdsStreamCfg -1 0 0 0",
                        "frameCfg 0 1 32 10 100 1 0",
                        "sensorStart"));
                },
                "capture plan requires hardware LVDS ADC data");
            Throws<UsageException>(
                delegate
                {
                    TextCliCapturePlan.FromCommands(CapturePlanCommands(
                        "dfeDataOutputMode 1",
                        "adcCfg 2 1",
                        "lvdsStreamCfg -1 0 1 1",
                        "frameCfg 0 1 32 10 100 1 0",
                        "sensorStart"));
                },
                "capture plan rejects software LVDS");
            Throws<UsageException>(
                delegate
                {
                    TextCliCapturePlan.FromCommands(CapturePlanCommands(
                        "dfeDataOutputMode 1",
                        "adcCfg 2 1",
                        null,
                        "frameCfg 0 1 32 10 100 1 0",
                        "sensorStart"));
                },
                "capture plan requires lvdsStreamCfg");
            Throws<UsageException>(
                delegate
                {
                    TextCliCapturePlan.FromCommands(CapturePlanCommands(
                        "dfeDataOutputMode 1",
                        "adcCfg 2 1",
                        "lvdsStreamCfg -1 0 1 0",
                        "frameCfg 0 1 32 10 100 2 0",
                        "sensorStart"));
                },
                "capture plan rejects hardware frame trigger");
            Throws<UsageException>(
                delegate
                {
                    TextCliCapturePlan.FromCommands(CapturePlanCommands(
                        "dfeDataOutputMode 1",
                        "adcCfg 2 1",
                        "lvdsStreamCfg -1 0 1 0",
                        null,
                        "sensorStart"));
                },
                "capture plan requires frameCfg");
            Throws<UsageException>(
                delegate
                {
                    TextCliCapturePlan.FromCommands(new[]
                    {
                        "dfeDataOutputMode 1",
                        "adcCfg 2 1",
                        "lvdsStreamCfg -1 0 1 0",
                        "frameCfg 0 1 32 10 100 1 0",
                        "frameCfg 0 1 32 20 100 1 0",
                        "sensorStart"
                    });
                },
                "capture plan rejects duplicate frameCfg");
            Throws<UsageException>(
                delegate
                {
                    TextCliCapturePlan.FromCommands(CapturePlanCommands(
                        "dfeDataOutputMode 1",
                        "adcCfg 2 1",
                        "lvdsStreamCfg -1 0 1 0",
                        "frameCfg 0 1 32 10 100 1 0",
                        "sensorStart 1"));
                },
                "capture plan rejects sensorStart 1");
            Throws<UsageException>(
                delegate
                {
                    TextCliCapturePlan.FromCommands(CapturePlanCommands(
                        "dfeDataOutputMode 1",
                        "adcCfg 2 1",
                        "lvdsStreamCfg -1 0 1 0",
                        "frameCfg 0 1 32 10 100 1 0",
                        "sensorStart 0 extra"));
                },
                "capture plan rejects sensorStart extra arguments");
            Throws<UsageException>(
                delegate
                {
                    TextCliCapturePlan.FromCommands(CapturePlanCommands(
                        "dfeDataOutputMode 1",
                        "adcCfg 2 1",
                        "lvdsStreamCfg -1 0 1 0",
                        "frameCfg 0 1 32 10 100 1 0",
                        "sensorStart  0"));
                },
                "capture plan rejects non-exact sensorStart spacing");
            Throws<UsageException>(
                delegate
                {
                    TextCliCapturePlan.FromCommands(CapturePlanCommands(
                        "dfeDataOutputMode 1",
                        "adcCfg 2 1",
                        "lvdsStreamCfg -1 0 1 0",
                        "frameCfg 0 1 32 10 100 1 0",
                        "SensorStart"));
                },
                "capture plan rejects sensorStart case mismatch");
            Throws<UsageException>(
                delegate
                {
                    TextCliCapturePlan.FromCommands(CapturePlanCommands(
                        "dfeDataOutputMode 1",
                        "AdcCfg 2 1",
                        "lvdsStreamCfg -1 0 1 0",
                        "frameCfg 0 1 32 10 100 1 0",
                        "sensorStart"));
                },
                "capture plan rejects case-mismatched 68xx command names");
            Throws<UsageException>(
                delegate { TextCliCapturePlan.FromCommands(new[] { "sensorStart", "sensorStart 0" }); },
                "capture plan rejects duplicate sensorStart");
            Throws<UsageException>(
                delegate { TextCliCapturePlan.FromCommands(new[] { "sensorStart", "profileCfg 0" }); },
                "capture plan requires final sensorStart");
        }

        private static string[] CapturePlanCommands(
            string outputMode,
            string adc,
            string lvds,
            string frame,
            string start)
        {
            var commands = new List<string>();
            foreach (string command in new[] { outputMode, adc, lvds, frame, start })
            {
                if (command != null)
                {
                    commands.Add(command);
                }
            }

            return commands.ToArray();
        }

        private static void TestIntegratedTextCapture()
        {
            string outputPath = Path.Combine(
                Path.GetTempPath(),
                "mmwcli-integrated-capture-self-test-" + Guid.NewGuid().ToString("N") + ".bin");
            try
            {
                using (var fakeDca = new SelfTestDca1000Fake())
                using (var transport = new SelfTestTextCliFake())
                {
                    fakeDca.RequireAliveAsyncAcknowledgement = false;
                    fakeDca.Start();
                    DcaEndpointOptions endpoint = CreateLoopbackOptions(fakeDca);
                    var configuration = new DcaCaptureConfiguration();
                    var plan = TextCliCapturePlan.FromCommands(new[]
                    {
                        "sensorStop",
                        "dfeDataOutputMode 1",
                        "adcCfg 2 1",
                        "lvdsStreamCfg -1 0 1 0",
                        "frameCfg 0 1 32 1 100 1 0",
                        "flushCfg",
                        "profileCfg 0 60 7",
                        "sensorStart"
                    });

                    transport.CommandReceived = delegate(string command)
                    {
                        if (string.Equals(command, "sensorStart", StringComparison.OrdinalIgnoreCase))
                        {
                            fakeDca.SendData(1, 0x100UL, new byte[] { 0x51, 0x52, 0x53, 0x54 });
                        }
                        else if (string.Equals(command, "sensorStop", StringComparison.OrdinalIgnoreCase) &&
                                 transport.Commands.Count > 1)
                        {
                            throw new IOException("68xx 拒绝命令 sensorStop: Error -54");
                        }
                    };

                    var session = new TextCliCaptureSession(transport, endpoint, configuration, null);
                    DcaCaptureStats stats = session.Run(
                        plan,
                        outputPath,
                        2000,
                        100,
                        CancellationToken.None);

                    Equal(true, transport.IsOpen, "integrated capture opens text transport");
                    Equal(10, transport.Commands.Count, "integrated capture command count");
                    Equal("version", transport.Commands[0], "integrated capture verifies platform first");
                    Equal("sensorStop", transport.Commands[1], "integrated capture ensures radar stopped first");
                    Equal("dfeDataOutputMode 1", transport.Commands[2], "integrated capture output mode config");
                    Equal("adcCfg 2 1", transport.Commands[3], "integrated capture ADC config");
                    Equal("lvdsStreamCfg -1 0 1 0", transport.Commands[4], "integrated capture LVDS config");
                    Equal("frameCfg 0 1 32 1 100 1 0", transport.Commands[5], "integrated capture frame config");
                    Equal("flushCfg", transport.Commands[6], "integrated capture first extra config command");
                    Equal("profileCfg 0 60 7", transport.Commands[7], "integrated capture second extra config command");
                    Equal("sensorStart", transport.Commands[8], "integrated capture starts after DCA arm");
                    Equal("sensorStop", transport.Commands[9], "integrated capture stops radar before return");
                    Equal(1, fakeDca.StartRequests, "integrated capture DCA start count");
                    Equal(2, fakeDca.StopRequests, "integrated capture DCA ensure-stop plus final stop count");
                    Equal(1L, stats.PacketsReceived, "integrated capture packet count");
                    Equal("51-52-53-54", BitConverter.ToString(File.ReadAllBytes(outputPath)), "integrated capture output");
                    fakeDca.AssertHealthy();
                }
            }
            finally
            {
                if (File.Exists(outputPath))
                {
                    File.Delete(outputPath);
                }

                if (File.Exists(outputPath + ".part"))
                {
                    File.Delete(outputPath + ".part");
                }
            }
        }

        private static void TestIntegratedWrongPlatformRejected()
        {
            string outputPath = Path.Combine(
                Path.GetTempPath(),
                "mmwcli-wrong-platform-self-test-" + Guid.NewGuid().ToString("N") + ".bin");
            try
            {
                using (var fakeDca = new SelfTestDca1000Fake())
                using (var transport = new SelfTestTextCliFake())
                {
                    fakeDca.Start();
                    transport.VersionResponse = "Platform : xWR18xx\r\nDone\r\n";
                    var session = new TextCliCaptureSession(
                        transport,
                        CreateLoopbackOptions(fakeDca),
                        new DcaCaptureConfiguration(),
                        null);
                    TextCliCapturePlan plan = TextCliCapturePlan.FromCommands(CapturePlanCommands(
                        "dfeDataOutputMode 1",
                        "adcCfg 2 1",
                        "lvdsStreamCfg -1 0 1 0",
                        "frameCfg 0 1 32 1 100 1 0",
                        "sensorStart"));

                    Throws<InvalidOperationException>(
                        delegate
                        {
                            session.Run(plan, outputPath, 2000, 100, CancellationToken.None);
                        },
                        "studio-cli capture rejects a non-68xx platform");
                    Equal(1, transport.Commands.Count, "wrong platform stops after version command");
                    Equal(0, fakeDca.AliveRequests, "wrong platform sends no DCA command");
                    Equal(false, File.Exists(outputPath + ".part"), "wrong platform creates no partial output");
                    fakeDca.AssertHealthy();
                }
            }
            finally
            {
                if (File.Exists(outputPath))
                {
                    File.Delete(outputPath);
                }

                if (File.Exists(outputPath + ".part"))
                {
                    File.Delete(outputPath + ".part");
                }
            }
        }

        private static void TestEnsureStoppedUnknownResultCleanup()
        {
            string outputPath = Path.Combine(
                Path.GetTempPath(),
                "mmwcli-ensure-stop-self-test-" + Guid.NewGuid().ToString("N") + ".bin");
            try
            {
                using (var fakeDca = new SelfTestDca1000Fake())
                using (var transport = new SelfTestTextCliFake())
                {
                    fakeDca.Start();
                    int stopAttempts = 0;
                    transport.CommandReceived = delegate(string command)
                    {
                        if (string.Equals(command, "sensorStop", StringComparison.Ordinal) &&
                            ++stopAttempts == 1)
                        {
                            throw new TimeoutException("模拟首次 sensorStop 终态丢失");
                        }
                    };
                    TextCliCapturePlan plan = TextCliCapturePlan.FromCommands(CapturePlanCommands(
                        "dfeDataOutputMode 1",
                        "adcCfg 2 1",
                        "lvdsStreamCfg -1 0 1 0",
                        "frameCfg 0 1 32 1 100 1 0",
                        "sensorStart"));

                    Throws<IOException>(
                        delegate
                        {
                            new TextCliCaptureSession(
                                transport,
                                CreateLoopbackOptions(fakeDca),
                                new DcaCaptureConfiguration(),
                                null).Run(plan, outputPath, 2000, 100, CancellationToken.None);
                        },
                        "unknown ensure-stopped result aborts after one cleanup retry");
                    Equal(3, transport.Commands.Count, "ensure-stopped retry command count");
                    Equal("version", transport.Commands[0], "ensure-stopped verifies platform first");
                    Equal("sensorStop", transport.Commands[1], "ensure-stopped first attempt");
                    Equal("sensorStop", transport.Commands[2], "ensure-stopped one cleanup retry");
                    Equal(0, fakeDca.AliveRequests, "ensure-stopped failure sends no DCA command");
                    Equal(false, File.Exists(outputPath + ".part"), "ensure-stopped failure creates no partial output");
                    fakeDca.AssertHealthy();
                }
            }
            finally
            {
                if (File.Exists(outputPath))
                {
                    File.Delete(outputPath);
                }

                if (File.Exists(outputPath + ".part"))
                {
                    File.Delete(outputPath + ".part");
                }
            }
        }

        private static void TestIntegratedDemoCaptureWithoutVersion()
        {
            string outputPath = Path.Combine(
                Path.GetTempPath(),
                "mmwcli-integrated-demo-self-test-" + Guid.NewGuid().ToString("N") + ".bin");
            try
            {
                using (var fakeDca = new SelfTestDca1000Fake())
                using (var transport = new SelfTestTextCliFake())
                {
                    fakeDca.RequireAliveAsyncAcknowledgement = false;
                    fakeDca.Start();
                    TextCliCapturePlan plan = TextCliCapturePlan.FromCommands(CapturePlanCommands(
                        "dfeDataOutputMode 1",
                        "adcCfg 2 1",
                        "lvdsStreamCfg -1 0 1 0",
                        "frameCfg 0 1 32 1 100 1 0",
                        "sensorStart"));
                    transport.CommandReceived = delegate(string command)
                    {
                        if (string.Equals(command, "sensorStart", StringComparison.Ordinal))
                        {
                            fakeDca.SendData(1, 0x400UL, new byte[] { 0x61, 0x62 });
                        }
                    };

                    var session = new TextCliCaptureSession(
                        transport,
                        CreateLoopbackOptions(fakeDca),
                        new DcaCaptureConfiguration(),
                        null,
                        false);
                    DcaCaptureStats stats = session.Run(
                        plan,
                        outputPath,
                        2000,
                        100,
                        CancellationToken.None);

                    Equal(false, transport.Commands.Contains("version"), "SDK demo capture skips unsupported version command");
                    Equal("sensorStop", transport.Commands[0], "SDK demo capture still ensures stopped");
                    Equal(1L, stats.PacketsReceived, "SDK demo capture packet count");
                    Equal("61-62", BitConverter.ToString(File.ReadAllBytes(outputPath)), "SDK demo capture output");
                    fakeDca.AssertHealthy();
                }
            }
            finally
            {
                if (File.Exists(outputPath))
                {
                    File.Delete(outputPath);
                }

                if (File.Exists(outputPath + ".part"))
                {
                    File.Delete(outputPath + ".part");
                }
            }
        }

        private static void TestIntegratedFiniteFrameEarlyIdle()
        {
            string outputPath = Path.Combine(
                Path.GetTempPath(),
                "mmwcli-early-idle-self-test-" + Guid.NewGuid().ToString("N") + ".bin");
            try
            {
                using (var fakeDca = new SelfTestDca1000Fake())
                using (var transport = new SelfTestTextCliFake())
                {
                    fakeDca.RequireAliveAsyncAcknowledgement = false;
                    fakeDca.Start();
                    transport.CommandReceived = delegate(string command)
                    {
                        if (string.Equals(command, "sensorStart", StringComparison.Ordinal))
                        {
                            fakeDca.SendData(1, 0x500UL, new byte[] { 0x91, 0x92 });
                        }
                    };
                    TextCliCapturePlan plan = TextCliCapturePlan.FromCommands(CapturePlanCommands(
                        "dfeDataOutputMode 1",
                        "adcCfg 2 1",
                        "lvdsStreamCfg -1 0 1 0",
                        "frameCfg 0 1 32 2 1 1 0",
                        "sensorStart"));

                    Throws<IOException>(
                        delegate
                        {
                            new TextCliCaptureSession(
                                transport,
                                CreateLoopbackOptions(fakeDca),
                                new DcaCaptureConfiguration(),
                                null).Run(plan, outputPath, 2000, 100, CancellationToken.None);
                        },
                        "finite-frame capture rejects data idle before the planned schedule");
                    Equal(false, File.Exists(outputPath), "early finite-frame idle does not publish final output");
                    Equal(true, File.Exists(outputPath + ".part"), "early finite-frame idle preserves partial output");
                    Equal(2, fakeDca.StopRequests, "early finite-frame idle includes DCA ensure-stop and cleanup");
                    fakeDca.AssertHealthy();
                }
            }
            finally
            {
                if (File.Exists(outputPath))
                {
                    File.Delete(outputPath);
                }

                if (File.Exists(outputPath + ".part"))
                {
                    File.Delete(outputPath + ".part");
                }
            }
        }

        private static void TestIntegratedMissingBytesPreventsPublication()
        {
            string outputPath = Path.Combine(
                Path.GetTempPath(),
                "mmwcli-missing-bytes-self-test-" + Guid.NewGuid().ToString("N") + ".bin");
            try
            {
                using (var fakeDca = new SelfTestDca1000Fake())
                using (var transport = new SelfTestTextCliFake())
                {
                    fakeDca.RequireAliveAsyncAcknowledgement = false;
                    fakeDca.Start();
                    transport.CommandReceived = delegate(string command)
                    {
                        if (string.Equals(command, "sensorStart", StringComparison.Ordinal))
                        {
                            fakeDca.SendData(1, 0x540UL, new byte[] { 0x01, 0x02 });
                            fakeDca.SendData(3, 0x544UL, new byte[] { 0x05, 0x06 });
                        }
                    };
                    TextCliCapturePlan plan = TextCliCapturePlan.FromCommands(CapturePlanCommands(
                        "dfeDataOutputMode 1",
                        "adcCfg 2 1",
                        "lvdsStreamCfg -1 0 1 0",
                        "frameCfg 0 1 32 1 10 1 0",
                        "sensorStart"));

                    Throws<IOException>(
                        delegate
                        {
                            new TextCliCaptureSession(
                                transport,
                                CreateLoopbackOptions(fakeDca),
                                new DcaCaptureConfiguration(),
                                null).Run(plan, outputPath, 2000, 100, CancellationToken.None);
                        },
                        "unrecovered DCA byte range prevents final publication");
                    Equal(false, File.Exists(outputPath), "missing bytes do not publish final output");
                    Equal(true, File.Exists(outputPath + ".part"), "missing bytes preserve partial output");
                    Equal(
                        "01-02-00-00-05-06",
                        BitConverter.ToString(File.ReadAllBytes(outputPath + ".part")),
                        "missing byte range remains explicit in partial output");
                    Equal(2, fakeDca.StopRequests, "missing bytes still complete DCA ensure/final stop");
                    fakeDca.AssertHealthy();
                }
            }
            finally
            {
                if (File.Exists(outputPath))
                {
                    File.Delete(outputPath);
                }

                if (File.Exists(outputPath + ".part"))
                {
                    File.Delete(outputPath + ".part");
                }
            }
        }

        private static void TestIntegratedFiniteFrameContinuousOverrun()
        {
            string outputPath = Path.Combine(
                Path.GetTempPath(),
                "mmwcli-finite-overrun-self-test-" + Guid.NewGuid().ToString("N") + ".bin");
            var senderStop = new ManualResetEvent(false);
            Thread sender = null;
            try
            {
                using (var fakeDca = new SelfTestDca1000Fake())
                using (var transport = new SelfTestTextCliFake())
                {
                    fakeDca.RequireAliveAsyncAcknowledgement = false;
                    fakeDca.Start();
                    int stopCount = 0;
                    transport.CommandReceived = delegate(string command)
                    {
                        if (string.Equals(command, "sensorStart", StringComparison.Ordinal))
                        {
                            fakeDca.SendData(1, 0x560UL, new byte[] { 0x11 });
                            sender = new Thread(delegate()
                            {
                                uint sequence = 2;
                                while (!senderStop.WaitOne(20))
                                {
                                    fakeDca.SendData(
                                        sequence,
                                        0x560UL + sequence - 1,
                                        new byte[] { (byte)sequence });
                                    sequence++;
                                }
                            });
                            sender.IsBackground = true;
                            sender.Start();
                        }
                        else if (string.Equals(command, "sensorStop", StringComparison.Ordinal) &&
                                 ++stopCount == 2)
                        {
                            senderStop.Set();
                            if (sender != null)
                            {
                                sender.Join(1000);
                            }
                        }
                    };
                    TextCliCapturePlan plan = TextCliCapturePlan.FromCommands(CapturePlanCommands(
                        "dfeDataOutputMode 1",
                        "adcCfg 2 1",
                        "lvdsStreamCfg -1 0 1 0",
                        "frameCfg 0 1 32 1 10 1 0",
                        "sensorStart"));

                    Throws<IOException>(
                        delegate
                        {
                            new TextCliCaptureSession(
                                transport,
                                CreateLoopbackOptions(fakeDca),
                                new DcaCaptureConfiguration(),
                                null).Run(plan, outputPath, 2000, 100, CancellationToken.None);
                        },
                        "finite-frame continuous overrun reaches an absolute deadline");
                    Equal(false, File.Exists(outputPath), "finite overrun does not publish final output");
                    Equal(true, File.Exists(outputPath + ".part"), "finite overrun preserves partial output");
                    fakeDca.AssertHealthy();
                    Equal(1, fakeDca.StartRequests, "finite overrun starts DCA once");
                    Equal(2, fakeDca.StopRequests, "finite overrun completes DCA ensure/final stop");
                }
            }
            finally
            {
                senderStop.Set();
                if (sender != null && sender.IsAlive)
                {
                    sender.Join(1000);
                }

                senderStop.Dispose();
                if (File.Exists(outputPath))
                {
                    File.Delete(outputPath);
                }

                if (File.Exists(outputPath + ".part"))
                {
                    File.Delete(outputPath + ".part");
                }
            }
        }

        private static void TestIntegratedInfiniteFrameUnexpectedIdle()
        {
            string outputPath = Path.Combine(
                Path.GetTempPath(),
                "mmwcli-infinite-idle-self-test-" + Guid.NewGuid().ToString("N") + ".bin");
            try
            {
                using (var fakeDca = new SelfTestDca1000Fake())
                using (var transport = new SelfTestTextCliFake())
                {
                    fakeDca.RequireAliveAsyncAcknowledgement = false;
                    fakeDca.Start();
                    transport.CommandReceived = delegate(string command)
                    {
                        if (string.Equals(command, "sensorStart", StringComparison.Ordinal))
                        {
                            fakeDca.SendData(1, 0x580UL, new byte[] { 0x99, 0x9A });
                        }
                    };
                    TextCliCapturePlan plan = TextCliCapturePlan.FromCommands(CapturePlanCommands(
                        "dfeDataOutputMode 1",
                        "adcCfg 2 1",
                        "lvdsStreamCfg -1 0 1 0",
                        "frameCfg 0 1 32 0 10 1 0",
                        "sensorStart"));

                    Throws<IOException>(
                        delegate
                        {
                            new TextCliCaptureSession(
                                transport,
                                CreateLoopbackOptions(fakeDca),
                                new DcaCaptureConfiguration(),
                                null).Run(plan, outputPath, 2000, 100, CancellationToken.None);
                        },
                        "infinite-frame unexpected idle is an error");
                    Equal(false, File.Exists(outputPath), "infinite idle does not publish final output");
                    Equal(true, File.Exists(outputPath + ".part"), "infinite idle preserves partial output");
                    Equal(2, fakeDca.StopRequests, "infinite idle includes DCA ensure-stop and cleanup");
                    fakeDca.AssertHealthy();
                }
            }
            finally
            {
                if (File.Exists(outputPath))
                {
                    File.Delete(outputPath);
                }

                if (File.Exists(outputPath + ".part"))
                {
                    File.Delete(outputPath + ".part");
                }
            }
        }

        private static void TestIntegratedDcaConfigurationAsyncFailure()
        {
            string outputPath = Path.Combine(
                Path.GetTempPath(),
                "mmwcli-config-async-self-test-" + Guid.NewGuid().ToString("N") + ".bin");
            try
            {
                using (var fakeDca = new SelfTestDca1000Fake())
                using (var transport = new SelfTestTextCliFake())
                {
                    fakeDca.AsyncStatusBeforeConfigureRecordResponse = 0x0040;
                    fakeDca.Start();
                    TextCliCapturePlan plan = TextCliCapturePlan.FromCommands(CapturePlanCommands(
                        "dfeDataOutputMode 1",
                        "adcCfg 2 1",
                        "lvdsStreamCfg -1 0 1 0",
                        "frameCfg 0 1 32 1 100 1 0",
                        "sensorStart"));

                    Throws<Dca1000Exception>(
                        delegate
                        {
                            new TextCliCaptureSession(
                                transport,
                                CreateLoopbackOptions(fakeDca),
                                new DcaCaptureConfiguration(),
                                null).Run(plan, outputPath, 2000, 100, CancellationToken.None);
                        },
                        "fatal DCA configuration async bitmask aborts before StartRecord");
                    Equal(0, fakeDca.StartRequests, "configuration async failure sends no StartRecord");
                    Equal(1, fakeDca.StopRequests, "configuration async failure only performs ensure-stop");
                    Equal(false, File.Exists(outputPath + ".part"), "configuration async failure creates no partial output");
                    fakeDca.AssertHealthy();
                }
            }
            finally
            {
                if (File.Exists(outputPath))
                {
                    File.Delete(outputPath);
                }

                if (File.Exists(outputPath + ".part"))
                {
                    File.Delete(outputPath + ".part");
                }
            }
        }

        private static void TestIntegratedDcaFatalAsyncStatus()
        {
            string outputPath = Path.Combine(
                Path.GetTempPath(),
                "mmwcli-fatal-async-self-test-" + Guid.NewGuid().ToString("N") + ".bin");
            try
            {
                using (var fakeDca = new SelfTestDca1000Fake())
                using (var transport = new SelfTestTextCliFake())
                {
                    fakeDca.RequireAliveAsyncAcknowledgement = false;
                    fakeDca.AsyncStatusAfterStopResponse = 0x0200;
                    fakeDca.Start();
                    transport.CommandReceived = delegate(string command)
                    {
                        if (string.Equals(command, "sensorStart", StringComparison.Ordinal))
                        {
                            fakeDca.SendData(1, 0x600UL, new byte[] { 0xA1, 0xA2 });
                        }
                    };
                    TextCliCapturePlan plan = TextCliCapturePlan.FromCommands(CapturePlanCommands(
                        "dfeDataOutputMode 1",
                        "adcCfg 2 1",
                        "lvdsStreamCfg -1 0 1 0",
                        "frameCfg 0 1 32 1 100 1 0",
                        "sensorStart"));

                    Throws<IOException>(
                        delegate
                        {
                            new TextCliCaptureSession(
                                transport,
                                CreateLoopbackOptions(fakeDca),
                                new DcaCaptureConfiguration(),
                                null).Run(plan, outputPath, 2000, 100, CancellationToken.None);
                        },
                        "fatal DCA async bitmask rejects integrated capture");
                    Equal(false, File.Exists(outputPath), "fatal DCA async status does not publish output");
                    Equal(true, File.Exists(outputPath + ".part"), "fatal DCA async status preserves partial output");
                    Equal(2, fakeDca.StopRequests, "fatal DCA async status includes ensure-stop and cleanup");
                    fakeDca.AssertHealthy();
                }
            }
            finally
            {
                if (File.Exists(outputPath))
                {
                    File.Delete(outputPath);
                }

                if (File.Exists(outputPath + ".part"))
                {
                    File.Delete(outputPath + ".part");
                }
            }
        }

        private static void TestThrowingCaptureLogIsNonFatal()
        {
            string outputPath = Path.Combine(
                Path.GetTempPath(),
                "mmwcli-log-failure-self-test-" + Guid.NewGuid().ToString("N") + ".bin");
            try
            {
                using (var fakeDca = new SelfTestDca1000Fake())
                using (var transport = new SelfTestTextCliFake())
                {
                    fakeDca.RequireAliveAsyncAcknowledgement = false;
                    fakeDca.Start();
                    transport.CommandReceived = delegate(string command)
                    {
                        if (string.Equals(command, "sensorStart", StringComparison.Ordinal))
                        {
                            fakeDca.SendData(1, 0x700UL, new byte[] { 0xC1, 0xC2 });
                        }
                    };
                    TextCliCapturePlan plan = TextCliCapturePlan.FromCommands(CapturePlanCommands(
                        "dfeDataOutputMode 1",
                        "adcCfg 2 1",
                        "lvdsStreamCfg -1 0 1 0",
                        "frameCfg 0 1 32 1 100 1 0",
                        "sensorStart"));
                    var session = new TextCliCaptureSession(
                        transport,
                        CreateLoopbackOptions(fakeDca),
                        new DcaCaptureConfiguration(),
                        delegate(string message) { throw new IOException("模拟日志输出失败"); });

                    DcaCaptureStats stats = session.Run(
                        plan,
                        outputPath,
                        2000,
                        100,
                        CancellationToken.None);
                    Equal(1L, stats.PacketsReceived, "throwing log does not block capture");
                    Equal(true, File.Exists(outputPath), "throwing log does not block final publication");
                    Equal(2, fakeDca.StopRequests, "throwing log cannot block DCA ensure/final stop");
                    fakeDca.AssertHealthy();
                }
            }
            finally
            {
                if (File.Exists(outputPath))
                {
                    File.Delete(outputPath);
                }

                if (File.Exists(outputPath + ".part"))
                {
                    File.Delete(outputPath + ".part");
                }
            }
        }

        private static void TestIntegratedDrainTimeoutPreventsPublication()
        {
            string outputPath = Path.Combine(
                Path.GetTempPath(),
                "mmwcli-drain-timeout-self-test-" + Guid.NewGuid().ToString("N") + ".bin");
            var senderStop = new ManualResetEvent(false);
            Thread sender = null;
            try
            {
                using (var fakeDca = new SelfTestDca1000Fake())
                using (var transport = new SelfTestTextCliFake())
                {
                    fakeDca.RequireAliveAsyncAcknowledgement = false;
                    fakeDca.Start();
                    int stopCount = 0;
                    transport.CommandReceived = delegate(string command)
                    {
                        if (string.Equals(command, "sensorStart", StringComparison.Ordinal))
                        {
                            fakeDca.SendData(1, 0x780UL, new byte[] { 0xD1 });
                        }
                        else if (string.Equals(command, "sensorStop", StringComparison.Ordinal) &&
                                 ++stopCount == 2)
                        {
                            fakeDca.SendData(2, 0x781UL, new byte[] { 0xD2 });
                            sender = new Thread(delegate()
                            {
                                uint sequence = 3;
                                while (!senderStop.WaitOne(20))
                                {
                                    fakeDca.SendData(
                                        sequence,
                                        0x780UL + sequence - 1,
                                        new byte[] { (byte)sequence });
                                    sequence++;
                                }
                            });
                            sender.IsBackground = true;
                            sender.Start();
                        }
                    };
                    TextCliCapturePlan plan = TextCliCapturePlan.FromCommands(CapturePlanCommands(
                        "dfeDataOutputMode 1",
                        "adcCfg 2 1",
                        "lvdsStreamCfg -1 0 1 0",
                        "frameCfg 0 1 32 1 10 1 0",
                        "sensorStart"));

                    Throws<IOException>(
                        delegate
                        {
                            new TextCliCaptureSession(
                                transport,
                                CreateLoopbackOptions(fakeDca),
                                new DcaCaptureConfiguration(),
                                null).Run(plan, outputPath, 2000, 100, CancellationToken.None);
                        },
                        "bounded drain timeout prevents final publication");
                    senderStop.Set();
                    if (sender != null)
                    {
                        sender.Join(1000);
                    }

                    Equal(false, File.Exists(outputPath), "drain timeout does not publish final output");
                    Equal(true, File.Exists(outputPath + ".part"), "drain timeout preserves partial output");
                    Equal(2, fakeDca.StopRequests, "drain timeout still completes DCA ensure/final stop");
                    fakeDca.AssertHealthy();
                }
            }
            finally
            {
                senderStop.Set();
                if (sender != null && sender.IsAlive)
                {
                    sender.Join(1000);
                }

                senderStop.Dispose();
                if (File.Exists(outputPath))
                {
                    File.Delete(outputPath);
                }

                if (File.Exists(outputPath + ".part"))
                {
                    File.Delete(outputPath + ".part");
                }
            }
        }

        private static void TestIntegratedCancellationDuringCleanup()
        {
            string outputPath = Path.Combine(
                Path.GetTempPath(),
                "mmwcli-cleanup-cancel-self-test-" + Guid.NewGuid().ToString("N") + ".bin");
            using (var cancellation = new CancellationTokenSource())
            {
                try
                {
                    using (var fakeDca = new SelfTestDca1000Fake())
                    using (var transport = new SelfTestTextCliFake())
                    {
                        fakeDca.RequireAliveAsyncAcknowledgement = false;
                        fakeDca.Start();
                        int stopCount = 0;
                        transport.CommandReceived = delegate(string command)
                        {
                            if (string.Equals(command, "sensorStart", StringComparison.Ordinal))
                            {
                                fakeDca.SendData(1, 0x680UL, new byte[] { 0xB1, 0xB2 });
                            }
                            else if (string.Equals(command, "sensorStop", StringComparison.Ordinal) &&
                                     ++stopCount == 2)
                            {
                                cancellation.Cancel();
                            }
                        };
                        TextCliCapturePlan plan = TextCliCapturePlan.FromCommands(CapturePlanCommands(
                            "dfeDataOutputMode 1",
                            "adcCfg 2 1",
                            "lvdsStreamCfg -1 0 1 0",
                            "frameCfg 0 1 32 1 100 1 0",
                            "sensorStart"));

                        Throws<OperationCanceledException>(
                            delegate
                            {
                                new TextCliCaptureSession(
                                    transport,
                                    CreateLoopbackOptions(fakeDca),
                                    new DcaCaptureConfiguration(),
                                    null).Run(plan, outputPath, 2000, 100, cancellation.Token);
                            },
                            "cancellation during cleanup prevents final publication");
                        Equal(false, File.Exists(outputPath), "cleanup cancellation does not publish final output");
                        Equal(true, File.Exists(outputPath + ".part"), "cleanup cancellation preserves partial output");
                        Equal(2, fakeDca.StopRequests, "cleanup cancellation includes ensure-stop and final stop");
                        fakeDca.AssertHealthy();
                    }
                }
                finally
                {
                    if (File.Exists(outputPath))
                    {
                        File.Delete(outputPath);
                    }

                    if (File.Exists(outputPath + ".part"))
                    {
                        File.Delete(outputPath + ".part");
                    }
                }
            }
        }

        private static void TestIntegratedTextCaptureCleanup()
        {
            string outputPath = Path.Combine(
                Path.GetTempPath(),
                "mmwcli-integrated-cleanup-self-test-" + Guid.NewGuid().ToString("N") + ".bin");
            try
            {
                using (var fakeDca = new SelfTestDca1000Fake())
                using (var transport = new SelfTestTextCliFake())
                {
                    fakeDca.RequireAliveAsyncAcknowledgement = false;
                    fakeDca.Start();
                    DcaEndpointOptions endpoint = CreateLoopbackOptions(fakeDca);
                    var session = new TextCliCaptureSession(
                        transport,
                        endpoint,
                        new DcaCaptureConfiguration(),
                        null);
                    TextCliCapturePlan plan = TextCliCapturePlan.FromCommands(CapturePlanCommands(
                        "dfeDataOutputMode 1",
                        "adcCfg 2 1",
                        "lvdsStreamCfg -1 0 1 0",
                        "frameCfg 0 1 32 1 100 1 0",
                        "sensorStart"));

                    transport.CommandReceived = delegate(string command)
                    {
                        if (string.Equals(command, "sensorStart", StringComparison.OrdinalIgnoreCase))
                        {
                            throw new IOException("模拟 sensorStart 响应丢失");
                        }
                    };

                    Throws<IOException>(
                        delegate
                        {
                            session.Run(plan, outputPath, 2000, 100, CancellationToken.None);
                        },
                        "integrated capture reports start failure");
                    Equal(8, transport.Commands.Count, "integrated cleanup command count");
                    Equal("version", transport.Commands[0], "integrated cleanup verifies platform first");
                    Equal("sensorStop", transport.Commands[1], "integrated cleanup ensures radar stopped first");
                    Equal("dfeDataOutputMode 1", transport.Commands[2], "integrated cleanup output mode config");
                    Equal("adcCfg 2 1", transport.Commands[3], "integrated cleanup ADC config");
                    Equal("lvdsStreamCfg -1 0 1 0", transport.Commands[4], "integrated cleanup LVDS config");
                    Equal("frameCfg 0 1 32 1 100 1 0", transport.Commands[5], "integrated cleanup frame config");
                    Equal("sensorStart", transport.Commands[6], "integrated cleanup attempted start");
                    Equal("sensorStop", transport.Commands[7], "integrated cleanup stops radar");
                    Equal(1, fakeDca.StartRequests, "integrated cleanup DCA start count");
                    Equal(2, fakeDca.StopRequests, "integrated cleanup DCA ensure-stop plus final stop count");
                    fakeDca.AssertHealthy();
                }
            }
            finally
            {
                if (File.Exists(outputPath))
                {
                    File.Delete(outputPath);
                }

                if (File.Exists(outputPath + ".part"))
                {
                    File.Delete(outputPath + ".part");
                }
            }
        }

        private static DcaEndpointOptions CreateLoopbackOptions(SelfTestDca1000Fake fake)
        {
            var endpoint = new DcaEndpointOptions();
            endpoint.HostAddress = fake.HostAddress;
            endpoint.DeviceAddress = fake.DeviceAddress;
            endpoint.ConfigPort = fake.ConfigPort;
            endpoint.DataPort = fake.DataPort;
            endpoint.TimeoutMilliseconds = 2000;
            return endpoint;
        }

        private static void TestDca1000Loopback()
        {
            string outputPath = Path.Combine(
                Path.GetTempPath(),
                "mmwcli-dca1000-self-test-" + Guid.NewGuid().ToString("N") + ".bin");
            string secondOutputPath = Path.Combine(
                Path.GetTempPath(),
                "mmwcli-dca1000-self-test-reuse-" + Guid.NewGuid().ToString("N") + ".bin");

            try
            {
                using (var fake = new SelfTestDca1000Fake())
                {
                    fake.RequireAliveAsyncAcknowledgement = true;
                    fake.Start();
                    var options = new DcaEndpointOptions();
                    options.HostAddress = fake.HostAddress;
                    options.DeviceAddress = fake.DeviceAddress;
                    options.ConfigPort = fake.ConfigPort;
                    options.DataPort = fake.DataPort;
                    options.TimeoutMilliseconds = 2000;

                    using (var client = new Dca1000Client(options))
                    {
                        int asyncCount = 0;
                        ushort asyncStatus = 0;
                        client.AsyncStatus += delegate(ushort status)
                        {
                            asyncCount++;
                            asyncStatus = status;
                            fake.AcknowledgeAliveAsyncStatus();
                        };

                        Equal((ushort)0, client.Ping(), "DCA loopback alive after interleaved async");
                        Equal(1, asyncCount, "DCA loopback async event count");
                        Equal(SelfTestDca1000Fake.AliveAsyncStatus, asyncStatus, "DCA loopback async status");
                        Equal(1, fake.AliveRequests, "DCA loopback alive request count");

                        using (var capture = new Dca1000Capture(options, client))
                        {
                            capture.Start(outputPath);
                            const ulong baseOffset = 0x12FFFFFFFCUL;
                            fake.SendData(100, baseOffset, new byte[] { 0x10, 0x11, 0x12, 0x13 });
                            fake.SendData(102, baseOffset + 8, new byte[] { 0x30, 0x31, 0x32, 0x33 });
                            WaitUntil(
                                delegate { return capture.Stats.PacketsReceived >= 2; },
                                2000,
                                "DCA loopback initial packets received");

                            fake.SendData(101, baseOffset + 4, new byte[] { 0x20, 0x21, 0x22, 0x23 });
                            WaitUntil(
                                delegate { return capture.Stats.PacketsReceived >= 3; },
                                2000,
                                "DCA loopback late packet received");
                            fake.SendData(103, baseOffset + 12, new byte[] { 0x40, 0x41, 0x42, 0x43 });
                            WaitUntil(
                                delegate { return capture.Stats.PacketsReceived >= 4; },
                                2000,
                                "DCA loopback packet after late packet received");
                            capture.Stop();

                            Equal(1, fake.StartRequests, "DCA loopback start request count");
                            Equal(1, fake.StopRequests, "DCA loopback stop request count");
                            Equal(4L, capture.Stats.PacketsReceived, "DCA loopback packet count");
                            Equal(16L, capture.Stats.PayloadBytesReceived, "DCA loopback payload bytes");
                            Equal(16L, capture.Stats.OutputBytes, "DCA loopback output bytes");
                            Equal(1L, capture.Stats.SequenceGaps, "DCA loopback gap is not recounted after late packet");
                            Equal(1L, capture.Stats.OutOfOrderPackets, "DCA loopback observed late packet");
                            Equal(0L, capture.Stats.MissingBytes, "DCA loopback late packet fully repairs byte range");
                            Equal(0L, capture.Stats.DiscardedBeforeBasePackets, "DCA loopback discards no prefix packet");

                            Equal(
                                "10-11-12-13-20-21-22-23-30-31-32-33-40-41-42-43",
                                BitConverter.ToString(File.ReadAllBytes(outputPath)),
                                "DCA loopback first capture u48 offset late hole fill");

                            capture.Start(secondOutputPath);
                            const ulong secondBaseOffset = 0x80UL;
                            fake.SendData(7, secondBaseOffset, new byte[] { 0xA0, 0xA1, 0xA2 });
                            fake.SendData(8, secondBaseOffset + 3, new byte[] { 0xB0, 0xB1 });
                            WaitUntil(
                                delegate { return capture.Stats.PacketsReceived >= 2; },
                                2000,
                                "DCA loopback reused capture packets received");
                            capture.Stop();

                            Equal(2, fake.StartRequests, "DCA loopback reused start request count");
                            Equal(2, fake.StopRequests, "DCA loopback reused stop request count");
                            Equal(2L, capture.Stats.PacketsReceived, "DCA loopback reused packet count reset");
                            Equal(5L, capture.Stats.PayloadBytesReceived, "DCA loopback reused payload bytes reset");
                            Equal(5L, capture.Stats.OutputBytes, "DCA loopback reused output bytes reset");
                            Equal(0L, capture.Stats.SequenceGaps, "DCA loopback reused sequence state reset");
                            Equal(0L, capture.Stats.OutOfOrderPackets, "DCA loopback reused order state reset");
                            Equal(0L, capture.Stats.MissingBytes, "DCA loopback reused missing-byte state reset");
                        }

                        Equal(
                            "A0-A1-A2-B0-B1",
                            BitConverter.ToString(File.ReadAllBytes(secondOutputPath)),
                            "DCA loopback reused capture output");
                        Equal(
                            "10-11-12-13-20-21-22-23-30-31-32-33-40-41-42-43",
                            BitConverter.ToString(File.ReadAllBytes(outputPath)),
                            "DCA loopback first output remains unchanged after reuse");
                    }

                    fake.AssertHealthy();
                }
            }
            finally
            {
                if (File.Exists(outputPath))
                {
                    File.Delete(outputPath);
                }

                if (File.Exists(secondOutputPath))
                {
                    File.Delete(secondOutputPath);
                }
            }
        }

        private static void TestDca1000BoundedDrain()
        {
            string outputPath = Path.Combine(
                Path.GetTempPath(),
                "mmwcli-bounded-drain-self-test-" + Guid.NewGuid().ToString("N") + ".bin");
            var senderStop = new ManualResetEvent(false);
            Thread sender = null;
            try
            {
                using (var fake = new SelfTestDca1000Fake())
                {
                    fake.Start();
                    DcaEndpointOptions endpoint = CreateLoopbackOptions(fake);
                    using (var client = new Dca1000Client(endpoint))
                    using (var capture = new Dca1000Capture(endpoint, client))
                    {
                        capture.Start(outputPath);
                        sender = new Thread(delegate()
                        {
                            uint sequence = 1;
                            while (!senderStop.WaitOne(20))
                            {
                                fake.SendData(sequence, sequence - 1, new byte[] { (byte)sequence });
                                sequence++;
                            }
                        });
                        sender.IsBackground = true;
                        sender.Start();
                        WaitUntil(
                            delegate { return capture.Stats.PacketsReceived > 0; },
                            2000,
                            "bounded drain receives initial data");

                        DateTime started = DateTime.UtcNow;
                        Equal(false, capture.WaitForDrain(200, 100), "bounded drain stops at absolute deadline");
                        Equal(true, (DateTime.UtcNow - started).TotalMilliseconds < 1500, "bounded drain returns promptly");
                        senderStop.Set();
                        sender.Join(1000);
                        capture.Stop();
                    }

                    Equal(1, fake.StopRequests, "bounded drain sends one DCA stop");
                    fake.AssertHealthy();
                }
            }
            finally
            {
                senderStop.Set();
                if (sender != null && sender.IsAlive)
                {
                    sender.Join(1000);
                }

                senderStop.Dispose();
                if (File.Exists(outputPath))
                {
                    File.Delete(outputPath);
                }
            }
        }

        private static void TestDca1000AmbiguousStartCleanup()
        {
            string outputPath = Path.Combine(
                Path.GetTempPath(),
                "mmwcli-dca-start-timeout-self-test-" + Guid.NewGuid().ToString("N") + ".bin");
            try
            {
                using (var fake = new SelfTestDca1000Fake())
                {
                    fake.DropStartResponse = true;
                    fake.Start();
                    DcaEndpointOptions endpoint = CreateLoopbackOptions(fake);
                    endpoint.TimeoutMilliseconds = 150;
                    using (var client = new Dca1000Client(endpoint))
                    using (var capture = new Dca1000Capture(endpoint, client))
                    {
                        Throws<Dca1000Exception>(
                            delegate { capture.Start(outputPath); },
                            "DCA ambiguous start is reported");
                        Equal(false, capture.IsRecording, "DCA ambiguous start closes local receiver");
                    }

                    Equal(1, fake.StartRequests, "DCA ambiguous start is never retried");
                    Equal(1, fake.StopRequests, "DCA ambiguous start sends one stop cleanup");
                    fake.AssertHealthy();
                }
            }
            finally
            {
                if (File.Exists(outputPath))
                {
                    File.Delete(outputPath);
                }
            }
        }

        private static void TestDca1000NonzeroStartCleanup()
        {
            string outputPath = Path.Combine(
                Path.GetTempPath(),
                "mmwcli-dca-start-status-self-test-" + Guid.NewGuid().ToString("N") + ".bin");
            try
            {
                using (var fake = new SelfTestDca1000Fake())
                {
                    fake.StartResponseStatus = 0x00C4;
                    fake.Start();
                    DcaEndpointOptions endpoint = CreateLoopbackOptions(fake);
                    using (var client = new Dca1000Client(endpoint))
                    using (var capture = new Dca1000Capture(endpoint, client))
                    {
                        Throws<Dca1000Exception>(
                            delegate { capture.Start(outputPath); },
                            "DCA nonzero start status is reported");
                        Equal(false, capture.IsRecording, "DCA nonzero start status closes local receiver");
                    }

                    Equal(1, fake.StartRequests, "DCA nonzero start status is never retried");
                    Equal(1, fake.StopRequests, "DCA nonzero start status sends one stop cleanup");
                    fake.AssertHealthy();
                }
            }
            finally
            {
                if (File.Exists(outputPath))
                {
                    File.Delete(outputPath);
                }
            }
        }

        private static void TestStudioHeadless(string explicitStudioRoot)
        {
            StudioInstallation installation = StudioLocator.Find(explicitStudioRoot);
            using (var host = new StudioLuaHost(installation, true))
            {
                object[] values = host.Evaluate(
                    "WriteToLog(''); WriteToLog('', 'green'); " +
                    "return type(ar1), type(ar1.Connect), type(ar1.CaptureCardConfig_EthInit), " +
                    "type(RSTD.Sleep), MMWCLI_HEADLESS");
                Equal(5, values.Length, "headless Lua result count");
                Equal("table", Convert.ToString(values[0]), "ar1 table");
                Equal("userdata", Convert.ToString(values[1]), "ar1.Connect registered");
                Equal("userdata", Convert.ToString(values[2]), "managed DCA override registered");
                Equal("userdata", Convert.ToString(values[3]), "RSTD.Sleep registered");
                Equal(true, Convert.ToBoolean(values[4]), "headless marker");
            }
        }

        private static void WaitUntil(Func<bool> predicate, int timeoutMilliseconds, string name)
        {
            DateTime deadline = DateTime.UtcNow.AddMilliseconds(timeoutMilliseconds);
            bool completed = false;
            while (DateTime.UtcNow < deadline)
            {
                Thread.MemoryBarrier();
                if (predicate())
                {
                    completed = true;
                    break;
                }

                Thread.Sleep(10);
            }

            Equal(true, completed, name);
        }

        private static void Equal<T>(T expected, T actual, string name)
        {
            _tests++;
            if (!EqualityComparer<T>.Default.Equals(expected, actual))
            {
                throw new Exception(string.Format(
                    "测试失败 [{0}]：期望 {1}，实际 {2}",
                    name,
                    expected,
                    actual));
            }
        }

        private static void Throws<TException>(Action action, string name)
            where TException : Exception
        {
            bool thrown = false;
            try
            {
                action();
            }
            catch (TException)
            {
                thrown = true;
            }

            Equal(true, thrown, name);
        }
    }
}
