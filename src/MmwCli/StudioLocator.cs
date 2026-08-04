using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;

namespace MmwCli
{
    internal sealed class StudioInstallation
    {
        public StudioInstallation(string studioRoot)
        {
            StudioRoot = Path.GetFullPath(studioRoot);
            InstallRoot = Directory.GetParent(StudioRoot).FullName;
            ClientDirectory = Path.Combine(StudioRoot, "Clients", "AR1xController");
            RuntimeDirectory = Path.Combine(StudioRoot, "RunTime");
            ScriptsDirectory = Path.Combine(StudioRoot, "Scripts");
            FirmwareDirectory = Path.Combine(InstallRoot, "rf_eval_firmware");
        }

        public string StudioRoot { get; private set; }
        public string InstallRoot { get; private set; }
        public string ClientDirectory { get; private set; }
        public string RuntimeDirectory { get; private set; }
        public string ScriptsDirectory { get; private set; }
        public string FirmwareDirectory { get; private set; }

        public IEnumerable<string> RequiredFiles()
        {
            yield return Path.Combine(ClientDirectory, "AR1xController.dll");
            yield return Path.Combine(ClientDirectory, "LuaInterface.dll");
            yield return Path.Combine(ClientDirectory, "LuaRegister.dll");
            yield return Path.Combine(ClientDirectory, "RadarLinkDLL.dll");
            yield return Path.Combine(RuntimeDirectory, "lua51.dll");
            yield return Path.Combine(FirmwareDirectory, "radarss", "xwr68xx_radarss.bin");
            yield return Path.Combine(FirmwareDirectory, "masterss", "xwr68xx_masterss.bin");
        }
    }

    internal static class StudioLocator
    {
        public static StudioInstallation Find(string explicitPath)
        {
            var candidates = new List<string>();
            AddCandidate(candidates, explicitPath);
            AddCandidate(candidates, Environment.GetEnvironmentVariable("MMWAVE_STUDIO_ROOT"));

            AddInstallationsUnder(candidates, @"D:\Apps\ti");
            AddInstallationsUnder(candidates, @"D:\App\ti");
            AddInstallationsUnder(candidates, @"C:\ti");

            foreach (string candidate in candidates)
            {
                string normalized = Normalize(candidate);
                if (normalized == null)
                {
                    continue;
                }

                string controller = Path.Combine(normalized, "Clients", "AR1xController", "AR1xController.dll");
                if (File.Exists(controller))
                {
                    return new StudioInstallation(normalized);
                }
            }

            throw new FileNotFoundException(
                "未找到 mmWave Studio。请安装 2.1.1，或通过 --studio-root / MMWAVE_STUDIO_ROOT 指定目录。");
        }

        internal static string Normalize(string path)
        {
            if (string.IsNullOrWhiteSpace(path))
            {
                return null;
            }

            string fullPath;
            try
            {
                fullPath = Path.GetFullPath(Environment.ExpandEnvironmentVariables(path.Trim('"')));
            }
            catch (Exception)
            {
                return null;
            }

            if (!Directory.Exists(fullPath))
            {
                return null;
            }

            if (string.Equals(Path.GetFileName(fullPath), "mmWaveStudio", StringComparison.OrdinalIgnoreCase))
            {
                return fullPath;
            }

            string nested = Path.Combine(fullPath, "mmWaveStudio");
            return Directory.Exists(nested) ? nested : fullPath;
        }

        private static void AddCandidate(List<string> candidates, string path)
        {
            if (!string.IsNullOrWhiteSpace(path))
            {
                candidates.Add(path);
            }
        }

        private static void AddInstallationsUnder(List<string> candidates, string parent)
        {
            if (!Directory.Exists(parent))
            {
                return;
            }

            try
            {
                string[] directories = Directory.GetDirectories(parent, "mmwave_studio_*");
                Array.Sort(directories, delegate(string left, string right)
                {
                    return string.Compare(right, left, StringComparison.OrdinalIgnoreCase);
                });

                foreach (string directory in directories)
                {
                    candidates.Add(directory);
                }
            }
            catch (UnauthorizedAccessException)
            {
            }
        }
    }
}
