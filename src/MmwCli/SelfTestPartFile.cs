using System;
using System.Collections.Generic;
using System.IO;
using System.Threading;

namespace MmwCli
{
    internal static class SelfTestPartFile
    {
        private static int _tests;

        public static int Run()
        {
            _tests = 0;
            TestSuccessfulCapturePublishesFinalFile();
            TestFailedCapturePreservesPartFile();
            TestExistingFinalFileIsRejected();
            TestExistingPartFileIsRejected();
            TestCommitCollisionPreservesBothFiles();
            return _tests;
        }

        private static void TestSuccessfulCapturePublishesFinalFile()
        {
            string outputPath = NewOutputPath("success");
            string partPath = outputPath + ".part";
            try
            {
                using (var fakeDca = new SelfTestDca1000Fake())
                using (var transport = new SelfTestTextCliFake())
                {
                    fakeDca.RequireAliveAsyncAcknowledgement = false;
                    fakeDca.Start();
                    transport.CommandReceived = delegate(string command)
                    {
                        if (string.Equals(command, "sensorStart", StringComparison.OrdinalIgnoreCase))
                        {
                            fakeDca.SendData(1, 0x200UL, new byte[] { 0x71, 0x72, 0x73 });
                        }
                    };

                    DcaCaptureStats stats = CreateSession(transport, fakeDca).Run(
                        CreatePlan(),
                        outputPath,
                        2000,
                        100,
                        CancellationToken.None);

                    Equal(true, File.Exists(outputPath), "part-file success publishes final output");
                    Equal(false, File.Exists(partPath), "part-file success removes temporary name");
                    Equal("71-72-73", BitConverter.ToString(File.ReadAllBytes(outputPath)), "part-file success data");
                    Equal(3L, stats.OutputBytes, "part-file success stats");
                    fakeDca.AssertHealthy();
                }
            }
            finally
            {
                DeleteIfPresent(outputPath);
                DeleteIfPresent(partPath);
            }
        }

        private static void TestFailedCapturePreservesPartFile()
        {
            string outputPath = NewOutputPath("failure");
            string partPath = outputPath + ".part";
            try
            {
                using (var fakeDca = new SelfTestDca1000Fake())
                using (var transport = new SelfTestTextCliFake())
                {
                    fakeDca.RequireAliveAsyncAcknowledgement = false;
                    fakeDca.Start();
                    transport.CommandReceived = delegate(string command)
                    {
                        if (string.Equals(command, "sensorStart", StringComparison.OrdinalIgnoreCase))
                        {
                            throw new IOException("模拟雷达启动失败");
                        }
                    };

                    Throws<IOException>(
                        delegate
                        {
                            CreateSession(transport, fakeDca).Run(
                                CreatePlan(),
                                outputPath,
                                2000,
                                100,
                                CancellationToken.None);
                        },
                        "part-file failed capture reports error");
                    Equal(false, File.Exists(outputPath), "part-file failure does not publish final output");
                    Equal(true, File.Exists(partPath), "part-file failure preserves temporary output");
                    Equal(1, fakeDca.StartRequests, "part-file failure DCA start count");
                    Equal(2, fakeDca.StopRequests, "part-file ensure-stop plus failure cleanup count");
                    fakeDca.AssertHealthy();
                }
            }
            finally
            {
                DeleteIfPresent(outputPath);
                DeleteIfPresent(partPath);
            }
        }

        private static void TestExistingFinalFileIsRejected()
        {
            string outputPath = NewOutputPath("existing");
            string partPath = outputPath + ".part";
            byte[] sentinel = { 0xC1, 0xC2, 0xC3 };
            try
            {
                File.WriteAllBytes(outputPath, sentinel);
                using (var transport = new SelfTestTextCliFake())
                {
                    var session = new TextCliCaptureSession(
                        transport,
                        new DcaEndpointOptions(),
                        new DcaCaptureConfiguration(),
                        null);
                    Throws<IOException>(
                        delegate
                        {
                            session.Run(
                                CreatePlan(),
                                outputPath,
                                2000,
                                100,
                                CancellationToken.None);
                        },
                        "part-file existing final is rejected");
                    Equal(false, transport.IsOpen, "part-file collision is rejected before hardware access");
                    Equal("C1-C2-C3", BitConverter.ToString(File.ReadAllBytes(outputPath)), "part-file existing final unchanged");
                    Equal(false, File.Exists(partPath), "part-file existing final creates no temporary output");
                }
            }
            finally
            {
                DeleteIfPresent(outputPath);
                DeleteIfPresent(partPath);
            }
        }

        private static void TestCommitCollisionPreservesBothFiles()
        {
            string outputPath = NewOutputPath("race");
            string partPath = outputPath + ".part";
            try
            {
                using (var fakeDca = new SelfTestDca1000Fake())
                using (var transport = new SelfTestTextCliFake())
                {
                    fakeDca.RequireAliveAsyncAcknowledgement = false;
                    fakeDca.Start();
                    transport.CommandReceived = delegate(string command)
                    {
                        if (string.Equals(command, "sensorStart", StringComparison.OrdinalIgnoreCase))
                        {
                            fakeDca.SendData(9, 0x300UL, new byte[] { 0x81, 0x82 });
                        }
                        else if (string.Equals(command, "sensorStop", StringComparison.OrdinalIgnoreCase) &&
                                 transport.Commands.Count >= 4)
                        {
                            File.WriteAllBytes(outputPath, new byte[] { 0xD1, 0xD2 });
                        }
                    };

                    Throws<IOException>(
                        delegate
                        {
                            CreateSession(transport, fakeDca).Run(
                                CreatePlan(),
                                outputPath,
                                2000,
                                100,
                                CancellationToken.None);
                        },
                        "part-file commit collision reports error");
                    Equal("D1-D2", BitConverter.ToString(File.ReadAllBytes(outputPath)), "part-file commit collision preserves target");
                    Equal(true, File.Exists(partPath), "part-file commit collision preserves temporary output");
                    Equal("81-82", BitConverter.ToString(File.ReadAllBytes(partPath)), "part-file commit collision preserves capture data");
                    Equal(2, fakeDca.StopRequests, "part-file commit collision includes ensure-stop and cleanup");
                    fakeDca.AssertHealthy();
                }
            }
            finally
            {
                DeleteIfPresent(outputPath);
                DeleteIfPresent(partPath);
            }
        }

        private static void TestExistingPartFileIsRejected()
        {
            string outputPath = NewOutputPath("existing-part");
            string partPath = outputPath + ".part";
            try
            {
                File.WriteAllBytes(partPath, new byte[] { 0xE1, 0xE2 });
                using (var transport = new SelfTestTextCliFake())
                {
                    var session = new TextCliCaptureSession(
                        transport,
                        new DcaEndpointOptions(),
                        new DcaCaptureConfiguration(),
                        null);
                    Throws<IOException>(
                        delegate
                        {
                            session.Run(
                                CreatePlan(),
                                outputPath,
                                2000,
                                100,
                                CancellationToken.None);
                        },
                        "part-file existing temporary output is rejected");
                    Equal(false, transport.IsOpen, "part-file stale temporary output is rejected before hardware access");
                    Equal(false, File.Exists(outputPath), "part-file stale temporary output creates no final file");
                    Equal("E1-E2", BitConverter.ToString(File.ReadAllBytes(partPath)), "part-file stale temporary output unchanged");
                }
            }
            finally
            {
                DeleteIfPresent(outputPath);
                DeleteIfPresent(partPath);
            }
        }

        private static TextCliCaptureSession CreateSession(
            SelfTestTextCliFake transport,
            SelfTestDca1000Fake fakeDca)
        {
            var endpoint = new DcaEndpointOptions();
            endpoint.HostAddress = fakeDca.HostAddress;
            endpoint.DeviceAddress = fakeDca.DeviceAddress;
            endpoint.ConfigPort = fakeDca.ConfigPort;
            endpoint.DataPort = fakeDca.DataPort;
            endpoint.TimeoutMilliseconds = 2000;
            return new TextCliCaptureSession(
                transport,
                endpoint,
                new DcaCaptureConfiguration(),
                null);
        }

        private static TextCliCapturePlan CreatePlan()
        {
            return TextCliCapturePlan.FromCommands(new[]
            {
                "dfeDataOutputMode 1",
                "adcCfg 2 1",
                "lvdsStreamCfg -1 0 1 0",
                "frameCfg 0 1 32 1 100 1 0",
                "sensorStart"
            });
        }

        private static string NewOutputPath(string suffix)
        {
            return Path.Combine(
                Path.GetTempPath(),
                "mmwcli-part-file-self-test-" + suffix + "-" + Guid.NewGuid().ToString("N") + ".bin");
        }

        private static void DeleteIfPresent(string path)
        {
            if (File.Exists(path))
            {
                File.Delete(path);
            }
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
