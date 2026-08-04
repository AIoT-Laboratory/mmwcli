using System;
using System.Collections.Generic;
using System.ComponentModel;
using System.IO;
using System.Reflection;
using System.Runtime.InteropServices;
using System.Threading;

namespace MmwCli
{
    public sealed class RstdCompat
    {
        private readonly string _studioRoot;
        private readonly string _runtimeDirectory;
        private readonly string _invocationDirectory;

        internal RstdCompat(StudioInstallation installation, string invocationDirectory)
        {
            _studioRoot = installation.StudioRoot;
            _runtimeDirectory = installation.RuntimeDirectory;
            _invocationDirectory = invocationDirectory;
        }

        public void Sleep(int milliseconds)
        {
            if (milliseconds < 0)
            {
                throw new ArgumentOutOfRangeException("milliseconds");
            }

            Thread.Sleep(milliseconds);
        }

        public string GetRstdPath()
        {
            return _studioRoot;
        }

        public string GetApplicationDir()
        {
            return _runtimeDirectory;
        }

        public string GetWorkingDirectory()
        {
            return _invocationDirectory;
        }

        public int SetVar(string path, string value)
        {
            return 0;
        }

        public int SetAndTransmit(string path, string value)
        {
            return 0;
        }

        public int Transmit(string path)
        {
            return 0;
        }

        public int NetStart()
        {
            return 0;
        }

        public int NetClose()
        {
            return 0;
        }

        public void WriteToLogOne(string message)
        {
            Console.Write(message);
        }

        public void WriteToLogTwo(string message, string color)
        {
            Console.Write(message);
        }
    }

    internal sealed class StudioLuaHost : IDisposable
    {
        private readonly StudioInstallation _installation;
        private readonly string _originalDirectory;
        private readonly ResolveEventHandler _assemblyResolver;
        private readonly Dca1000LuaCompat _dca;
        private object _luaWrapper;
        private object _luaVm;
        private IntPtr _luaNativeHandle;
        private bool _disposed;

        public StudioLuaHost(StudioInstallation installation, bool useManagedDca)
        {
            if (Environment.Is64BitProcess)
            {
                throw new PlatformNotSupportedException(
                    "TI mmWave Studio 2.1.1 运行库是 32 位；请使用 build.ps1 生成的 x86 mmwcli.exe。");
            }

            _installation = installation;
            _originalDirectory = Environment.CurrentDirectory;
            _assemblyResolver = ResolveAssembly;
            AppDomain.CurrentDomain.AssemblyResolve += _assemblyResolver;
            string luaNativePath = Path.Combine(installation.RuntimeDirectory, "lua51.dll");
            _luaNativeHandle = LoadLibrary(luaNativePath);
            if (_luaNativeHandle == IntPtr.Zero)
            {
                int error = Marshal.GetLastWin32Error();
                AppDomain.CurrentDomain.AssemblyResolve -= _assemblyResolver;
                throw new Win32Exception(error, "无法加载 TI Lua 5.1 运行库: " + luaNativePath);
            }

            SetDllDirectory(installation.ClientDirectory);
            Environment.CurrentDirectory = installation.ClientDirectory;

            try
            {
                InitializeLua(useManagedDca);
                _dca = useManagedDca ? new Dca1000LuaCompat(Log) : null;
                if (_dca != null)
                {
                    RegisterDcaFunctions();
                }
            }
            catch
            {
                AppDomain.CurrentDomain.AssemblyResolve -= _assemblyResolver;
                SetDllDirectory(null);
                Environment.CurrentDirectory = _originalDirectory;
                FreeLuaNativeLibrary();
                throw;
            }
        }

        public object[] RunFile(string scriptPath)
        {
            string fullPath = Path.GetFullPath(scriptPath);
            if (!File.Exists(fullPath))
            {
                throw new FileNotFoundException("找不到 Lua 脚本。", fullPath);
            }

            ConfigureLuaPaths(Path.GetDirectoryName(fullPath));
            return InvokeLuaArray("DoFile", new object[] { fullPath });
        }

        public object[] Evaluate(string code)
        {
            ConfigureLuaPaths(_originalDirectory);
            return InvokeLuaArray("DoString", new object[] { code });
        }

        public void FinishCapture(int startTimeoutMilliseconds, int idleMilliseconds)
        {
            if (_dca == null || !_dca.IsRecording)
            {
                return;
            }

            bool cancelled = false;
            ConsoleCancelEventHandler handler = delegate(object sender, ConsoleCancelEventArgs eventArgs)
            {
                eventArgs.Cancel = true;
                cancelled = true;
            };

            Console.CancelKeyPress += handler;
            try
            {
                Console.WriteLine(
                    "Lua 已结束，DCA1000 仍在录制；等待首包/数据静默（Ctrl+C 可安全停止）...");
                bool idle = _dca.WaitUntilIdle(
                    startTimeoutMilliseconds,
                    idleMilliseconds,
                    delegate { return cancelled; });
                if (!idle && !cancelled)
                {
                    Console.Error.WriteLine("在等待时间内没有收到 DCA1000 数据，执行安全停止。");
                }

                int result = _dca.CaptureCardConfig_StopRecord();
                if (result != 0)
                {
                    throw new Dca1000Exception(_dca.LastError ?? "DCA1000 停止失败。");
                }
            }
            finally
            {
                Console.CancelKeyPress -= handler;
            }
        }

        public void Dispose()
        {
            if (_disposed)
            {
                return;
            }

            if (_dca != null)
            {
                _dca.Dispose();
            }

            if (_luaVm != null)
            {
                try
                {
                    MethodInfo dispose = _luaVm.GetType().GetMethod("Dispose", Type.EmptyTypes);
                    if (dispose != null)
                    {
                        dispose.Invoke(_luaVm, null);
                    }
                }
                catch (Exception)
                {
                }
            }

            AppDomain.CurrentDomain.AssemblyResolve -= _assemblyResolver;
            SetDllDirectory(null);
            Environment.CurrentDirectory = _originalDirectory;
            FreeLuaNativeLibrary();
            _disposed = true;
        }

        private void InitializeLua(bool useManagedDca)
        {
            string client = _installation.ClientDirectory;
            Assembly luaInterface = Assembly.LoadFrom(Path.Combine(client, "LuaInterface.dll"));
            Assembly luaRegister = Assembly.LoadFrom(Path.Combine(client, "LuaRegister.dll"));
            Assembly arController = Assembly.LoadFrom(Path.Combine(client, "AR1xController.dll"));

            Type luaWrapperType = luaRegister.GetType("LuaRegister.LuaWrapper", true);
            _luaWrapper = Activator.CreateInstance(luaWrapperType);
            _luaVm = luaWrapperType.GetProperty("LuaVM").GetValue(_luaWrapper, null);

            Type arWrapperType = arController.GetType("AR1xController.AR1xxxWrapper", true);
            object arWrapper = Activator.CreateInstance(arWrapperType, new object[] { _luaWrapper });
            MethodInfo registerPackage = FindMethod(
                luaWrapperType,
                "RegisterLuaFunctions",
                3);
            registerPackage.Invoke(_luaWrapper, new object[] { "ar1", arWrapper, "mmwcli headless TI compatibility" });

            var rstd = new RstdCompat(_installation, _originalDirectory);
            InvokeLua("NewTable", new object[] { "RSTD" });
            RegisterFunction("RSTD.Sleep", rstd, "Sleep");
            RegisterFunction("RSTD.GetRstdPath", rstd, "GetRstdPath");
            RegisterFunction("RSTD.GetApplicationDir", rstd, "GetApplicationDir");
            RegisterFunction("RSTD.GetWorkingDirectory", rstd, "GetWorkingDirectory");
            RegisterFunction("RSTD.SetVar", rstd, "SetVar");
            RegisterFunction("RSTD.SetAndTransmit", rstd, "SetAndTransmit");
            RegisterFunction("RSTD.Transmit", rstd, "Transmit");
            RegisterFunction("RSTD.NetStart", rstd, "NetStart");
            RegisterFunction("RSTD.NetClose", rstd, "NetClose");
            RegisterFunction("MMWCLI_WriteToLogOne", rstd, "WriteToLogOne");
            RegisterFunction("MMWCLI_WriteToLogTwo", rstd, "WriteToLogTwo");

            string init = "MMWCLI_HEADLESS=true; MMWCLI_MANAGED_DCA=" +
                          (useManagedDca ? "true" : "false") + ";" +
                          "WriteToLog=function(message,color) " +
                          "if color==nil then return MMWCLI_WriteToLogOne(message) " +
                          "else return MMWCLI_WriteToLogTwo(message,color) end end;";
            InvokeLua("DoString", new object[] { init });
        }

        private void RegisterDcaFunctions()
        {
            RegisterFunction("ar1.SelectCaptureDevice", _dca, "SelectCaptureDevice");
            RegisterFunction("ar1.CaptureCardConfig_EthInit", _dca, "CaptureCardConfig_EthInit");
            RegisterFunction("ar1.CaptureCardConfig_Mode", _dca, "CaptureCardConfig_Mode");
            RegisterFunction("ar1.CaptureCardConfig_PacketDelay", _dca, "CaptureCardConfig_PacketDelay");
            RegisterFunction("ar1.CaptureCardConfig_StartRecord", _dca, "CaptureCardConfig_StartRecord");
            RegisterFunction("ar1.CaptureCardConfig_StopRecord", _dca, "CaptureCardConfig_StopRecord");
            RegisterFunction("ar1.CaptureCard_DisConnect", _dca, "CaptureCard_DisConnect");
        }

        private void ConfigureLuaPaths(string scriptDirectory)
        {
            string code = string.Format(
                "MMWCLI_STUDIO_ROOT={0}; MMWCLI_INVOCATION_DIR={1}; " +
                "package.path={2}..'\\\\?.lua;'..{3}..'\\\\?.lua;'..package.path",
                LuaQuote(_installation.StudioRoot),
                LuaQuote(_originalDirectory),
                LuaQuote(scriptDirectory),
                LuaQuote(_installation.ScriptsDirectory));
            InvokeLua("DoString", new object[] { code });
        }

        private void RegisterFunction(string path, object target, string methodName)
        {
            MethodInfo targetMethod = target.GetType().GetMethod(methodName, BindingFlags.Public | BindingFlags.Instance);
            if (targetMethod == null)
            {
                throw new MissingMethodException(target.GetType().FullName, methodName);
            }

            MethodInfo registerFunction = FindMethod(_luaVm.GetType(), "RegisterFunction", 3);
            registerFunction.Invoke(_luaVm, new object[] { path, target, targetMethod });
        }

        private object InvokeLua(string methodName, object[] arguments)
        {
            MethodInfo method = FindMethod(_luaVm.GetType(), methodName, arguments.Length);
            try
            {
                return method.Invoke(_luaVm, arguments);
            }
            catch (TargetInvocationException exception)
            {
                throw exception.InnerException ?? exception;
            }
        }

        private object[] InvokeLuaArray(string methodName, object[] arguments)
        {
            object result = InvokeLua(methodName, arguments);
            return result as object[] ?? new object[0];
        }

        private static MethodInfo FindMethod(Type type, string name, int parameterCount)
        {
            foreach (MethodInfo method in type.GetMethods(BindingFlags.Public | BindingFlags.Instance))
            {
                if (method.Name == name && method.GetParameters().Length == parameterCount)
                {
                    return method;
                }
            }

            throw new MissingMethodException(type.FullName, name);
        }

        private Assembly ResolveAssembly(object sender, ResolveEventArgs args)
        {
            string fileName = new AssemblyName(args.Name).Name + ".dll";
            string[] directories =
            {
                _installation.ClientDirectory,
                _installation.RuntimeDirectory,
                Path.Combine(_installation.StudioRoot, "PostProc")
            };

            foreach (string directory in directories)
            {
                string candidate = Path.Combine(directory, fileName);
                if (File.Exists(candidate))
                {
                    return Assembly.LoadFrom(candidate);
                }
            }

            return null;
        }

        private static string LuaQuote(string value)
        {
            return "'" + value.Replace("\\", "\\\\").Replace("'", "\\'") + "'";
        }

        private static void Log(string message)
        {
            Console.WriteLine("[mmwcli] " + message);
        }

        [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
        private static extern bool SetDllDirectory(string pathName);

        [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
        private static extern IntPtr LoadLibrary(string fileName);

        [DllImport("kernel32.dll", SetLastError = true)]
        private static extern bool FreeLibrary(IntPtr module);

        private void FreeLuaNativeLibrary()
        {
            if (_luaNativeHandle != IntPtr.Zero)
            {
                FreeLibrary(_luaNativeHandle);
                _luaNativeHandle = IntPtr.Zero;
            }
        }
    }
}
