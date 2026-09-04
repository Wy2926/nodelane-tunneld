[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$Registry,

    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[0-9A-Za-z._-]+$')]
    [string]$Version,

    [string]$Repository = "nodelane/tunneld",
    [string]$Platforms = "linux/amd64,linux/arm64",
    [switch]$TagStable
)

$ErrorActionPreference = "Stop"
$repoRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
$registryName = $Registry.Trim().TrimEnd('/')
$repositoryName = $Repository.Trim().Trim('/')

if (-not $registryName -or $registryName -match '^https?://') {
    throw "Registry must be a Docker registry host, without http:// or https://."
}
if (-not $repositoryName) {
    throw "Repository cannot be empty."
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
    "--label", "org.opencontainers.image.revision=$Version",
    "--tag", "${image}:$Version"
)
if ($TagStable) {
    $dockerArguments += @("--tag", "${image}:stable")
}
$dockerArguments += @("--push", $repoRoot)

Write-Host "Publishing ${image}:$Version to $registryName"
& docker @dockerArguments
if ($LASTEXITCODE -ne 0) {
    throw "Docker image build or push failed."
}

Write-Host "Published image: ${image}:$Version"
if ($TagStable) {
    Write-Host "Published alias: ${image}:stable"
}
