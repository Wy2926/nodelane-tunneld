[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$Registry,

    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[0-9A-Za-z._-]+$')]
    [string]$Version,

    [ValidatePattern('^[0-9A-Za-z._-]+$')]
    [string]$ClientVersion,

    [string]$Repository = "nodelane/tunneld",
    [string]$Platforms = "linux/amd64,linux/arm64",
    [switch]$TagStable
)

$ErrorActionPreference = "Stop"
$repoRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
$registryName = $Registry.Trim().TrimEnd('/')
$repositoryName = $Repository.Trim().Trim('/')
$clientVersionFile = Join-Path $repoRoot "client-version.txt"

if ([string]::IsNullOrWhiteSpace($ClientVersion)) {
    if (-not (Test-Path -LiteralPath $clientVersionFile -PathType Leaf)) {
        throw "Client version is required because client-version.txt was not found."
    }
    $ClientVersion = (Get-Content -LiteralPath $clientVersionFile -Raw).Trim()
}

if (-not $registryName -or $registryName -match '^https?://') {
    throw "Registry must be a Docker registry host, without http:// or https://."
}
if (-not $repositoryName) {
    throw "Repository cannot be empty."
}
if ($ClientVersion -notmatch '^[0-9A-Za-z._-]+$') {
    throw "ClientVersion may contain only letters, digits, dot, underscore, and dash."
}
if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
    throw "Docker is required."
}

& docker buildx version | Out-Null
if ($LASTEXITCODE -ne 0) {
    throw "Docker Buildx is required."
}

$image = "$registryName/$repositoryName"
$dockerArguments = @(
    "buildx", "build",
    "--platform", $Platforms,
    "--build-arg", "VERSION=$Version",
    "--build-arg", "NT_VERSION=$ClientVersion",
    "--label", "org.opencontainers.image.revision=$Version",
    "--tag", "${image}:$Version"
)
if ($TagStable) {
    $dockerArguments += @("--tag", "${image}:stable")
}
$dockerArguments += @("--push", $repoRoot)

Write-Host "Publishing ${image}:$Version with nt $ClientVersion to $registryName"
& docker @dockerArguments
if ($LASTEXITCODE -ne 0) {
    throw "Docker image build or push failed."
}

Write-Host "Published image: ${image}:$Version"
if ($TagStable) {
    Write-Host "Published alias: ${image}:stable"
}
