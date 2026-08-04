using System;
using System.Diagnostics;
using System.IO;
using System.IO.Ports;
using System.Linq;
using System.Net;
using System.Net.NetworkInformation;
using System.Net.Sockets;
using System.Reflection;

namespace MmwCli
{
    internal static class Doctor
    {
        public static int Run(string explicitStudioRoot)
        {
            bool healthy = true;
            Console.WriteLine("mmwcli 环境检查");
            Console.WriteLine("  进程架构: {0}", Environment.Is64BitProcess ? "x64（Studio 兼容层不可用）" : "x86");
            if (Environment.Is64BitProcess)
            {
                healthy = false;
            }

            StudioInstallation installation = null;
            try
            {
                installation = StudioLocator.Find(explicitStudioRoot);
                Console.WriteLine("  mmWave Studio: " + installation.StudioRoot);
                foreach (string file in installation.RequiredFiles())
                {
                    bool exists = File.Exists(file);
                    Console.WriteLine("    [{0}] {1}", exists ? "OK" : "缺失", file);
                    healthy &= exists;
                }

                string controller = Path.Combine(installation.ClientDirectory, "AR1xController.dll");
                if (File.Exists(controller))
                {
                    AssemblyName name = AssemblyName.GetAssemblyName(controller);
                    Console.WriteLine("  AR1xController: " + name.Version);
                }
            }
            catch (Exception exception)
            {
                Console.WriteLine("  [失败] " + exception.Message);
                healthy = false;
            }

            string[] ports;
            try
            {
                ports = SerialPort.GetPortNames().OrderBy(delegate(string value) { return value; }).ToArray();
            }
            catch (Exception)
            {
                ports = new string[0];
            }

            Console.WriteLine("  串口: " + (ports.Length == 0 ? "未发现" : string.Join(", ", ports)));

            bool dcaHostAddressPresent = false;
            Console.WriteLine("  IPv4 地址:");
            foreach (NetworkInterface adapter in NetworkInterface.GetAllNetworkInterfaces())
            {
                foreach (UnicastIPAddressInformation address in adapter.GetIPProperties().UnicastAddresses)
                {
                    if (address.Address.AddressFamily != AddressFamily.InterNetwork)
                    {
                        continue;
                    }

                    Console.WriteLine("    {0}: {1}", adapter.Name, address.Address);
                    if (address.Address.Equals(IPAddress.Parse("192.168.33.30")))
                    {
                        dcaHostAddressPresent = true;
                    }
                }
            }

            if (!dcaHostAddressPresent)
            {
                Console.WriteLine("  [提示] 未发现默认 DCA1000 主机地址 192.168.33.30；可配置网卡或传 --host。");
            }

            Console.WriteLine(healthy ? "检查通过。" : "检查未通过；请先处理上面的必需项。");
            return healthy ? 0 : 3;
        }
    }
}
