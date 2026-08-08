[CmdletBinding()]
param(
    [string] $Version,
    [string] $InstallDir,
    [switch] $Ftd2xx
)

$ErrorActionPreference = 'Stop'
$repository = 'AIoT-Laboratory/mmwcli'
$apiBase = if ($env:MMWCLI_RELEASE_API_BASE) {
    $env:MMWCLI_RELEASE_API_BASE.TrimEnd('/')
} else {
    "https://api.github.com/repos/$repository"
}

if (-not [Runtime.InteropServices.RuntimeInformation]::IsOSPlatform(
        [Runtime.InteropServices.OSPlatform]::Windows
    )) {
    throw 'mmwcli downloader: this script supports Windows only'
}
$architecture = switch ([Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()) {
    'X64' { 'amd64' }
    'Arm64' { 'arm64' }
    default { throw "mmwcli downloader: unsupported Windows architecture: $_" }
}
if ($Ftd2xx -and $architecture -ne 'amd64') {
    throw 'mmwcli downloader: the release has an ftd2xx binary only for Windows amd64'
}
if ([string]::IsNullOrWhiteSpace($InstallDir)) {
    $localAppData = [Environment]::GetFolderPath([Environment+SpecialFolder]::LocalApplicationData)
    if ([string]::IsNullOrWhiteSpace($localAppData)) {
        throw 'mmwcli downloader: LocalApplicationData is unavailable; pass -InstallDir DIRECTORY'
    }
    $InstallDir = Join-Path $localAppData 'Programs\mmwcli'
}
if ($Version -and $Version -cnotmatch '^[A-Za-z0-9][A-Za-z0-9._-]*$') {
    throw "mmwcli downloader: invalid release tag: $Version"
}

$headers = @{ Accept = 'application/vnd.github+json' }
if ($env:GITHUB_TOKEN) { $headers.Authorization = "Bearer $($env:GITHUB_TOKEN)" }
$releaseUri = if ($Version) { "$apiBase/releases/tags/$Version" } else { "$apiBase/releases/latest" }
try {
    $release = Invoke-RestMethod -Uri $releaseUri -Headers $headers
} catch {
    throw "mmwcli downloader: could not read GitHub release metadata from $releaseUri`: $($_.Exception.Message)"
}
$tag = [string] $release.tag_name
if ($tag -cnotmatch '^[A-Za-z0-9][A-Za-z0-9._-]*$') {
    throw "mmwcli downloader: GitHub returned an invalid release tag: $tag"
}
if ($Version -and $tag -cne $Version) {
    throw "mmwcli downloader: GitHub returned release tag $tag for requested tag $Version"
}

$suffix = if ($Ftd2xx) { '-ftd2xx' } else { '' }
$assetName = "mmwcli-$tag-windows-$architecture$suffix.exe"
function Find-ExactAsset([string] $Name) {
    $found = @($release.assets | Where-Object { ([string] $_.name) -ceq $Name })
    if ($found.Count -ne 1) {
        throw "mmwcli downloader: release $tag does not contain exactly one asset named $Name"
    }
    $found[0]
}
$binaryAsset = Find-ExactAsset $assetName
$checksumsAsset = Find-ExactAsset 'SHA256SUMS'

$temporaryDir = Join-Path ([IO.Path]::GetTempPath()) ("mmwcli-download-" + [guid]::NewGuid().ToString('N'))
$stage = $null
New-Item -ItemType Directory -Path $temporaryDir | Out-Null
try {
    $binaryPath = Join-Path $temporaryDir $assetName
    $checksumsPath = Join-Path $temporaryDir 'SHA256SUMS'
    Invoke-WebRequest -Uri ([string] $binaryAsset.browser_download_url) -Headers $headers -OutFile $binaryPath
    Invoke-WebRequest -Uri ([string] $checksumsAsset.browser_download_url) -Headers $headers -OutFile $checksumsPath

    $expected = @()
    foreach ($line in [IO.File]::ReadAllLines($checksumsPath)) {
        if ($line -match '^([0-9A-Fa-f]{64})\s+\*?(.+)$' -and $Matches[2] -ceq $assetName) {
            $expected += $Matches[1]
        }
    }
    if ($expected.Count -ne 1) {
        throw "mmwcli downloader: SHA256SUMS does not contain exactly one checksum for $assetName"
    }
    $actual = (Get-FileHash -LiteralPath $binaryPath -Algorithm SHA256).Hash
    if ($actual -cne $expected[0].ToUpperInvariant()) {
        throw "mmwcli downloader: SHA-256 mismatch for $assetName"
    }

    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    $target = Join-Path $InstallDir 'mmwcli.exe'
    $stage = Join-Path $InstallDir ('.mmwcli.install.' + [guid]::NewGuid().ToString('N'))
    Copy-Item -LiteralPath $binaryPath -Destination $stage
    Move-Item -LiteralPath $stage -Destination $target -Force
    $stage = $null
    Write-Output "Installed mmwcli $tag (windows/$architecture) to $target"
    Write-Output "Add $InstallDir to PATH to run mmwcli directly."
} catch {
    if ($_.Exception.Message -like 'mmwcli downloader:*') { throw }
    throw "mmwcli downloader: $($_.Exception.Message)"
} finally {
    if ($stage -and (Test-Path -LiteralPath $stage)) { Remove-Item -LiteralPath $stage -Force }
    if (Test-Path -LiteralPath $temporaryDir) { Remove-Item -LiteralPath $temporaryDir -Recurse -Force }
}
