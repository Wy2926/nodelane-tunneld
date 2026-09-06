param(
    [Parameter(Position = 0, ValueFromRemainingArguments = $true)]
    [string[]]$TunnelArguments = @()
)

$ErrorActionPreference = "Stop"
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

function Write-Step([string]$Message) {
    if ($env:NO_COLOR) {
        Write-Host "==> $Message"
    }
    else {
        Write-Host "==>" -ForegroundColor Cyan -NoNewline
        Write-Host " $Message"
    }
}

function Write-Success([string]$Message) {
    if ($env:NO_COLOR) {
        Write-Host "OK $Message"
    }
    else {
        Write-Host "OK" -ForegroundColor Green -NoNewline
        Write-Host " $Message"
    }
}

function Invoke-DownloadWithProgress([string]$Uri, [string]$Destination, [string]$Name) {
    Add-Type -AssemblyName System.Net.Http
    $client = [System.Net.Http.HttpClient]::new()
    $response = $null
    $source = $null
    $destinationStream = $null
    try {
        $response = $client.GetAsync($Uri, [System.Net.Http.HttpCompletionOption]::ResponseHeadersRead).GetAwaiter().GetResult()
        $response.EnsureSuccessStatusCode() | Out-Null
        $length = $response.Content.Headers.ContentLength
        $source = $response.Content.ReadAsStreamAsync().GetAwaiter().GetResult()
        $destinationStream = [System.IO.File]::Open($Destination, [System.IO.FileMode]::Create, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
        $buffer = New-Object byte[] (1024 * 1024)
        [long]$received = 0
        while (($read = $source.Read($buffer, 0, $buffer.Length)) -gt 0) {
            $destinationStream.Write($buffer, 0, $read)
            $received += $read
            if ($length -and $length -gt 0) {
                $percent = [Math]::Min(100, [int](($received * 100) / $length))
                $status = "{0:N1} / {1:N1} MiB" -f ($received / 1MB), ($length / 1MB)
                Write-Progress -Activity "Downloading $Name" -Status $status -PercentComplete $percent
            }
            else {
                Write-Progress -Activity "Downloading $Name" -Status ("{0:N1} MiB" -f ($received / 1MB))
            }
        }
        Write-Progress -Activity "Downloading $Name" -Completed
    }
    finally {
        if ($destinationStream) { $destinationStream.Dispose() }
        if ($source) { $source.Dispose() }
        if ($response) { $response.Dispose() }
        $client.Dispose()
    }
}

function Get-RequiredText([string]$Uri, [string]$Description) {
    try {
        $response = Invoke-WebRequest -UseBasicParsing -Uri $Uri
    }
    catch {
        throw "Failed to download $Description from $Uri. $($_.Exception.Message)"
    }
    $content = if ($null -eq $response) {
        ""
    }
    elseif ($response.Content -is [byte[]]) {
        [Text.Encoding]::UTF8.GetString($response.Content)
    }
    else {
        [string]$response.Content
    }
    if ([string]::IsNullOrWhiteSpace($content)) {
        throw "The server returned an empty $Description response from $Uri. Check your proxy, VPN, DNS, or HTTPS inspection settings."
    }
    return $content.Trim()
}

function Set-AtomicTextFile([string]$Path, [string]$Value) {
    $directory = Split-Path -Parent $Path
    $temporary = Join-Path $directory ("." + [IO.Path]::GetFileName($Path) + "." + [Guid]::NewGuid().ToString("N"))
    [IO.File]::WriteAllText($temporary, $Value, [Text.UTF8Encoding]::new($false))
    if (Test-Path -LiteralPath $Path -PathType Leaf) {
        $backup = Join-Path $directory ("." + [IO.Path]::GetFileName($Path) + ".backup." + [Guid]::NewGuid().ToString("N"))
        try {
            [IO.File]::Replace($temporary, $Path, $backup)
        }
        finally {
            Remove-Item -LiteralPath $backup -Force -ErrorAction SilentlyContinue
        }
    }
    else {
        [IO.File]::Move($temporary, $Path)
    }
}

if (-not [Environment]::Is64BitOperatingSystem) {
    throw "NodeLane Tunnel currently requires 64-bit Windows."
}

$architectureName = if (-not [string]::IsNullOrWhiteSpace($env:PROCESSOR_ARCHITEW6432)) {
    $env:PROCESSOR_ARCHITEW6432
}
else {
    $env:PROCESSOR_ARCHITECTURE
}
if ([string]::IsNullOrWhiteSpace($architectureName)) {
    $architectureName = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
}

switch ($architectureName.Trim().ToLowerInvariant()) {
    "amd64" { $arch = "amd64" }
    "x64" { $arch = "amd64" }
    "x86_64" { $arch = "amd64" }
    "arm64" { $arch = "arm64" }
    default { throw "Unsupported CPU architecture: $architectureName" }
}

$localAppData = $env:LOCALAPPDATA
if ([string]::IsNullOrWhiteSpace($localAppData)) {
    $localAppData = [Environment]::GetFolderPath([Environment+SpecialFolder]::LocalApplicationData)
}
if ([string]::IsNullOrWhiteSpace($localAppData)) {
    throw "Unable to locate the Windows local application data directory."
}
if ([string]::IsNullOrWhiteSpace($env:LOCALAPPDATA)) {
    $env:LOCALAPPDATA = $localAppData
}

$releaseBase = if ($env:NT_RELEASE_BASE) { $env:NT_RELEASE_BASE.TrimEnd('/') } else { "https://tunnel.nodelane.net/releases" }
$dataRoot = Join-Path $localAppData "nodelane\tunnel"
$versionsDir = Join-Path $dataRoot "versions"
$currentFile = Join-Path $dataRoot "current"
$customBin = -not [string]::IsNullOrWhiteSpace($env:NT_BIN_DIR)
$binDir = if ($customBin) { [IO.Path]::GetFullPath($env:NT_BIN_DIR) } else { Join-Path $localAppData "nodelane\bin" }
$launcher = Join-Path $binDir "nt.cmd"

Write-Step "Checking the latest NodeLane Tunnel client..."
$version = Get-RequiredText -Uri "$releaseBase/stable.txt" -Description "release version"
if ($version -notmatch '^[0-9][0-9A-Za-z._-]{0,63}$') {
    throw "Invalid release version returned by server."
}

$asset = "nt_${version}_windows_${arch}.zip"
$installDir = Join-Path $versionsDir "$version\windows-$arch"
$ntExe = Join-Path $installDir "nt.exe"
$previousClient = if (Test-Path -LiteralPath $currentFile -PathType Leaf) { (Get-Content -LiteralPath $currentFile -Raw).Trim() } else { "" }

New-Item -ItemType Directory -Force -Path $versionsDir, $binDir | Out-Null
$installedVersion = ""
if (Test-Path -LiteralPath $ntExe -PathType Leaf) {
    $installedVersion = (& $ntExe --version 2>$null | Out-String).Trim()
}
if ($installedVersion -ne $version) {
    $stagingDir = Join-Path $versionsDir (".install-$version-" + [Guid]::NewGuid().ToString("N"))
    New-Item -ItemType Directory -Path $stagingDir | Out-Null
    try {
        $archive = Join-Path $stagingDir $asset
        $packageDir = Join-Path $stagingDir "package"
        New-Item -ItemType Directory -Path $packageDir | Out-Null

        Write-Step "Downloading NodeLane Tunnel $version (windows/$arch)..."
        Invoke-DownloadWithProgress -Uri "$releaseBase/$version/$asset" -Destination $archive -Name $asset
        Write-Step "Verifying package integrity..."
        $checksum = Get-RequiredText -Uri "$releaseBase/$version/$asset.sha256" -Description "package checksum"
        $expected = ($checksum -split '\s+')[0].ToLowerInvariant()
        if ($expected -notmatch '^[0-9a-f]{64}$') {
            throw "Invalid SHA-256 checksum returned by server for $asset."
        }
        $actual = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($actual -ne $expected) {
            throw "NodeLane Tunnel package checksum verification failed."
        }

        Expand-Archive -LiteralPath $archive -DestinationPath $packageDir
        $stagedClient = Join-Path $packageDir "nt.exe"
        if (-not (Test-Path -LiteralPath $stagedClient -PathType Leaf)) {
            throw "The downloaded package did not contain nt.exe."
        }
        $downloadedVersion = (& $stagedClient --version | Out-String).Trim()
        if ($LASTEXITCODE -ne 0 -or $downloadedVersion -ne $version) {
            throw "Downloaded client version $downloadedVersion does not match $version."
        }

        if (Test-Path -LiteralPath $installDir) {
            Remove-Item -LiteralPath $installDir -Recurse -Force
        }
        New-Item -ItemType Directory -Force -Path (Split-Path -Parent $installDir) | Out-Null
        Move-Item -LiteralPath $packageDir -Destination $installDir
        Write-Success "NodeLane Tunnel $version is installed."
    }
    finally {
        if (Test-Path -LiteralPath $stagingDir) {
            Remove-Item -LiteralPath $stagingDir -Recurse -Force
        }
    }
}
else {
    Write-Step "Using installed client $version."
}

Set-AtomicTextFile -Path $currentFile -Value "$ntExe`n"
$launcherContent = @'
@echo off
setlocal
set "NT_CURRENT=%LOCALAPPDATA%\nodelane\tunnel\current"
if not exist "%NT_CURRENT%" (
  echo NodeLane Tunnel is not installed; run https://tunnel.nodelane.net/run.ps1 again. 1>&2
  exit /b 1
)
set /p "NT_CLIENT="<"%NT_CURRENT%"
if not exist "%NT_CLIENT%" (
  echo The installed NodeLane Tunnel client is unavailable; run the installer again. 1>&2
  exit /b 1
)
"%NT_CLIENT%" %*
exit /b %ERRORLEVEL%
'@
Set-AtomicTextFile -Path $launcher -Value $launcherContent

if (-not $customBin) {
    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    $userParts = @($userPath -split ';' | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
    if (-not ($userParts | Where-Object { $_.TrimEnd('\') -ieq $binDir.TrimEnd('\') })) {
        [Environment]::SetEnvironmentVariable("Path", ((@($binDir) + $userParts) -join ';'), "User")
        Write-Step "Added $binDir to the user PATH."
    }
}
$processParts = @($env:Path -split ';' | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
if (-not ($processParts | Where-Object { $_.TrimEnd('\') -ieq $binDir.TrimEnd('\') })) {
    $env:Path = (@($binDir) + $processParts) -join ';'
}

# Keep the immediately previous version for rollback. Locked older clients are
# removed on a later installer run after their tunnel process exits.
$previousDir = if ($previousClient) { Split-Path -Parent $previousClient } else { "" }
Get-ChildItem -LiteralPath $versionsDir -Directory | ForEach-Object {
    Get-ChildItem -LiteralPath $_.FullName -Directory | ForEach-Object {
        if ($_.FullName -ine $installDir -and $_.FullName -ine $previousDir) {
            Remove-Item -LiteralPath $_.FullName -Recurse -Force -ErrorAction SilentlyContinue
        }
    }
}

& $ntExe @TunnelArguments
$clientExitCode = $LASTEXITCODE
if ($MyInvocation.MyCommand.Path) {
    exit $clientExitCode
}
