[CmdletBinding()]
param(
    [string]$Version = "0.1.0",
    [string]$OutputDirectory = "dist",
    [string]$GoExecutable = ""
)

$ErrorActionPreference = "Stop"
$repoRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))

if ($Version -notmatch '^[0-9A-Za-z._-]+$') {
    throw "Version contains unsupported characters."
}
if (-not $GoExecutable) {
    $goCommand = Get-Command go -ErrorAction SilentlyContinue
    if ($goCommand) {
        $GoExecutable = $goCommand.Source
    }
    else {
        $portableGo = Join-Path $env:LOCALAPPDATA "codex-tools\go1.27.1\go\bin\go.exe"
        if (Test-Path -LiteralPath $portableGo) {
            $GoExecutable = $portableGo
        }
    }
}
if (-not $GoExecutable -or -not (Test-Path -LiteralPath $GoExecutable)) {
    throw "Go 1.27 was not found. Pass -GoExecutable with the path to go.exe."
}
if (-not (Get-Command tar.exe -ErrorAction SilentlyContinue)) {
    throw "Windows tar.exe is required."
}
$pnpmCommand = Get-Command pnpm -ErrorAction SilentlyContinue
if (-not $pnpmCommand) {
    throw "pnpm is required to build the Astro frontend."
}

function New-HexSecret([int]$ByteCount) {
    $bytes = New-Object byte[] $ByteCount
    $rng = [Security.Cryptography.RandomNumberGenerator]::Create()
    try { $rng.GetBytes($bytes) } finally { $rng.Dispose()
    }
    return -join ($bytes | ForEach-Object { $_.ToString("x2") })
}

function Write-Utf8File([string]$Path, [string]$Content) {
    [IO.File]::WriteAllText($Path, $Content.Replace("`r`n", "`n"), [Text.UTF8Encoding]::new($false))
}

$outputRoot = if ([IO.Path]::IsPathRooted($OutputDirectory)) {
    [IO.Path]::GetFullPath($OutputDirectory)
}
else {
    [IO.Path]::GetFullPath((Join-Path $repoRoot $OutputDirectory))
}
$archivePath = Join-Path $outputRoot "nodelane-tunnel-$Version-linux.tar.gz"
if (Test-Path -LiteralPath $archivePath) {
    throw "Deployment archive already exists: $archivePath"
}

$tempRoot = Join-Path ([IO.Path]::GetTempPath()) ("nodelane-tunnel-bundle-" + [Guid]::NewGuid().ToString("N"))
$bundleParent = Join-Path $tempRoot "bundle"
$bundleRoot = Join-Path $bundleParent "tunnel"
$releaseRoot = Join-Path $bundleRoot "releases\$Version"
New-Item -ItemType Directory -Path $releaseRoot -Force | Out-Null
New-Item -ItemType Directory -Path (Join-Path $bundleRoot "bin") -Force | Out-Null

$oldGoos = $env:GOOS
$oldGoarch = $env:GOARCH
$oldCgo = $env:CGO_ENABLED

try {
    & $pnpmCommand.Source --dir (Join-Path $repoRoot "web") install --frozen-lockfile
    if ($LASTEXITCODE -ne 0) { throw "Could not install frontend dependencies." }
    & $pnpmCommand.Source --dir (Join-Path $repoRoot "web") build
    if ($LASTEXITCODE -ne 0) { throw "Could not build the Astro frontend." }

    $frontendSource = [IO.Path]::GetFullPath((Join-Path $repoRoot "web\dist"))
    $frontendTarget = [IO.Path]::GetFullPath((Join-Path $repoRoot "internal\server\assets\web"))
    if (-not $frontendTarget.StartsWith($repoRoot, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Frontend target escaped the repository: $frontendTarget"
    }
    if (Test-Path -LiteralPath $frontendTarget) {
        Remove-Item -LiteralPath $frontendTarget -Recurse -Force
    }
    Copy-Item -LiteralPath $frontendSource -Destination $frontendTarget -Recurse

    Copy-Item -Path (Join-Path $repoRoot "deploy\bundle\*") -Destination $bundleRoot -Recurse

    $frpToken = New-HexSecret 32
    $tokenPepper = New-HexSecret 32
    $jwtSecret = New-HexSecret 32
    $adminToken = New-HexSecret 32

    $environment = @(
        "DEV_MODE=false"
        "LISTEN_ADDR=127.0.0.1:9000"
        "RELEASE_DIR=/releases"
        "LOG_LEVEL=info"
        "PUBLIC_SCHEME=http"
        "PUBLIC_DOMAIN=tunnel.nodelane.net"
        "NODE_ID=primary"
        "FRP_SERVER_ADDR=tunnel.nodelane.net"
        "FRP_SERVER_PORT=7000"
        "FRP_AUTH_TOKEN=$frpToken"
        "FRP_TLS_SERVER_NAME=tunnel.nodelane.net"
        "FRP_BANDWIDTH_LIMIT=5MB"
        "DATABASE_URL=REPLACE_WITH_1PANEL_POSTGRES_URL"
        "REDIS_ADDR=127.0.0.1:6379"
        "REDIS_PASSWORD=REPLACE_WITH_1PANEL_REDIS_PASSWORD"
        "REDIS_PREFIX=nodelane:tunnel"
        "TOKEN_PEPPER=$tokenPepper"
        "TUNNEL_JWT_SECRET=$jwtSecret"
        "ADMIN_TOKEN=$adminToken"
        "TUNNEL_TTL=1h"
        "MAX_TUNNELS_PER_CLIENT=1"
        "MAX_TUNNELS_PER_IP=2"
        "TCP_PORT_START=20000"
        "TCP_PORT_END=29999"
        "UDP_PORT_START=30000"
        "UDP_PORT_END=39999"
        "TRUSTED_PROXY_CIDRS=127.0.0.0/8,::1/128"
        ""
    ) -join "`n"
    Write-Utf8File (Join-Path $bundleRoot ".env") $environment

    $frpsConfig = (Get-Content -LiteralPath (Join-Path $repoRoot "deploy\frps.toml") -Raw).Replace("REPLACE_WITH_64_HEX_CHARACTERS", $frpToken)
    Write-Utf8File (Join-Path $bundleRoot "frps.toml") $frpsConfig

    foreach ($arch in @("amd64", "arm64")) {
        $env:GOOS = "linux"
        $env:GOARCH = $arch
        $env:CGO_ENABLED = "0"
        $serverOutput = Join-Path $bundleRoot "bin\tunneld-linux-$arch"
        & $GoExecutable build -trimpath -ldflags="-s -w" -o $serverOutput ./cmd/tunneld
        if ($LASTEXITCODE -ne 0) { throw "Could not build tunneld linux/$arch." }
    }

    $frpModuleDirectory = (& $GoExecutable list -m -f '{{.Dir}}' github.com/fatedier/frp).Trim()
    if ($LASTEXITCODE -ne 0 -or -not $frpModuleDirectory) {
        throw "Could not locate the embedded frp module."
    }

    $targets = @(
        @{ OS = "linux"; Arch = "amd64" },
        @{ OS = "linux"; Arch = "arm64" },
        @{ OS = "windows"; Arch = "amd64" },
        @{ OS = "windows"; Arch = "arm64" }
    )
    foreach ($target in $targets) {
        $os = $target.OS
        $arch = $target.Arch
        $packageDir = Join-Path $tempRoot "package-$os-$arch"
        New-Item -ItemType Directory -Path $packageDir | Out-Null
        $suffix = if ($os -eq "windows") { ".exe" } else { "" }

        $env:GOOS = $os
        $env:GOARCH = $arch
        $env:CGO_ENABLED = "0"
        & $GoExecutable build -trimpath "-ldflags=-s -w -X github.com/Wy2926/nodelane-tunneld/internal/client.Version=$Version" -o (Join-Path $packageDir "nt$suffix") ./cmd/nt
        if ($LASTEXITCODE -ne 0) { throw "Could not build nt $os/$arch." }

        Copy-Item -LiteralPath (Join-Path $frpModuleDirectory "LICENSE") -Destination (Join-Path $packageDir "LICENSE.frp")

        if ($os -eq "windows") {
            $assetName = "nt_${Version}_${os}_${arch}.zip"
            Compress-Archive -Path (Join-Path $packageDir "*") -DestinationPath (Join-Path $releaseRoot $assetName)
        }
        else {
            $assetName = "nt_${Version}_${os}_${arch}.tar.gz"
            & tar.exe -C $packageDir -czf (Join-Path $releaseRoot $assetName) .
            if ($LASTEXITCODE -ne 0) { throw "Could not package $assetName." }
        }

        $assetPath = Join-Path $releaseRoot $assetName
        $hash = (Get-FileHash -LiteralPath $assetPath -Algorithm SHA256).Hash.ToLowerInvariant()
        Write-Utf8File "$assetPath.sha256" "$hash  $assetName`n"
    }

    Write-Utf8File (Join-Path $bundleRoot "releases\stable.txt") "$Version`n"
    Write-Utf8File (Join-Path $bundleRoot "DEPLOY.txt") @"
This archive already contains binaries, client releases, configuration and secrets.

Server installation:
  1. Extract under /opt/nodelane.
  2. cd /opt/nodelane/tunnel
  3. Set DATABASE_URL in .env to the existing 1Panel PostgreSQL database.
  4. Set REDIS_PASSWORD in .env to the existing 1Panel Redis password.
     Use REDIS_PASSWORD= only when Redis authentication is disabled.
  5. sh install.sh
  6. Review and apply frps.toml manually.
  7. Configure these two 1Panel reverse proxies manually:
       tunnel.nodelane.net   -> http://127.0.0.1:9000
       *.tunnel.nodelane.net -> http://127.0.0.1:8080 (public port 80,
                                  preserve Host, no HTTPS redirect)

install.sh only manages the Docker Compose project "nodelane-tunnel" and files
inside this extracted directory. It does not inspect or change frps, 1Panel,
OpenResty, PostgreSQL, Redis, firewall rules, DNS, systemd, or unrelated Docker
resources. Its Compose file contains only tunneld. On startup, tunneld applies
its embedded tables and indexes to the database selected by DATABASE_URL. It
does not create a PostgreSQL role or database.

The archive and .env contain production secrets. Keep them private.
"@

    New-Item -ItemType Directory -Path $outputRoot -Force | Out-Null
    & tar.exe -C $bundleParent -czf $archivePath "tunnel"
    if ($LASTEXITCODE -ne 0) { throw "Could not create deployment archive." }
    $archiveHash = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash.ToLowerInvariant()
    Write-Utf8File "$archivePath.sha256" "$archiveHash  $([IO.Path]::GetFileName($archivePath))`n"

    Write-Host "Deployment bundle created:"
    Write-Host "  $archivePath"
    Write-Host "  $archivePath.sha256"
    Write-Warning "The archive contains production secrets. Store and transfer it securely."
}
finally {
    $env:GOOS = $oldGoos
    $env:GOARCH = $oldGoarch
    $env:CGO_ENABLED = $oldCgo
    $resolvedTemp = [IO.Path]::GetFullPath($tempRoot)
    $systemTemp = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
    if ($resolvedTemp.StartsWith($systemTemp, [StringComparison]::OrdinalIgnoreCase) -and
        (Split-Path -Leaf $resolvedTemp).StartsWith("nodelane-tunnel-bundle-")) {
        Remove-Item -LiteralPath $resolvedTemp -Recurse -Force -ErrorAction SilentlyContinue
    }
}
