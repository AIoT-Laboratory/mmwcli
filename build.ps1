param(
    [ValidateSet("Debug", "Release")]
    [string]$Configuration = "Debug"
)

$ErrorActionPreference = "Stop"
$repoRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$sourceRoot = Join-Path $repoRoot "src\MmwCli"
$outputRoot = Join-Path $repoRoot ("bin\" + $Configuration)
$compiler = Join-Path $env:WINDIR "Microsoft.NET\Framework\v4.0.30319\csc.exe"

if (-not (Test-Path -LiteralPath $compiler)) {
    throw "找不到 .NET Framework C# 编译器: $compiler"
}

New-Item -ItemType Directory -Path $outputRoot -Force | Out-Null
$sources = Get-ChildItem -LiteralPath $sourceRoot -Filter "*.cs" -File | Sort-Object Name | ForEach-Object FullName
if ($sources.Count -eq 0) {
    throw "没有找到 C# 源文件: $sourceRoot"
}

$options = @(
    "/nologo",
    "/target:exe",
    "/platform:x86",
    "/warn:4",
    "/out:$outputRoot\mmwcli.exe"
)

if ($Configuration -eq "Release") {
    $options += "/optimize+"
    $options += "/debug-"
} else {
    $options += "/optimize-"
    $options += "/debug+"
}

& $compiler $options $sources
if ($LASTEXITCODE -ne 0) {
    throw "编译失败，csc 退出码: $LASTEXITCODE"
}

Copy-Item -LiteralPath (Join-Path $repoRoot "mmwcli.exe.config") -Destination (Join-Path $outputRoot "mmwcli.exe.config") -Force
Write-Output ("已生成 " + (Join-Path $outputRoot "mmwcli.exe"))
