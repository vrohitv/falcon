# Builds Falcon from source on Windows. The UI is prebuilt in web\dist, so only Go and Git are needed.
#
#   .\build.ps1            # builds falcon.exe here
#   .\build.ps1 -Web       # also rebuilds the UI first (needs Node.js)
#   .\build.ps1 -Install   # and copies falcon.exe to %LOCALAPPDATA%\Programs\Falcon (added to PATH)
param([switch]$Web, [switch]$Install)
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
  throw "Go is not installed. Install it with:  winget install --id GoLang.Go -e   (then open a new terminal)"
}
if (-not (Get-Command git -ErrorAction SilentlyContinue)) {
  Write-Warning "Falcon needs Git at run time. Install it with:  winget install --id Git.Git -e"
}
if ($Web) {
  if (-not (Get-Command npm -ErrorAction SilentlyContinue)) { throw "Node.js is needed for -Web:  winget install --id OpenJS.NodeJS.LTS -e" }
  Push-Location web; npm ci --no-audit --no-fund; npm run build; Pop-Location
}
Write-Host "Building falcon.exe..."
$env:CGO_ENABLED = "0"
go build -trimpath -ldflags "-s -w" -o falcon.exe ./cmd/falcon
if ($LASTEXITCODE -ne 0) { throw "go build failed" }
Write-Host "Built $(Join-Path $PSScriptRoot 'falcon.exe')"
if ($Install) {
  $dest = Join-Path $env:LOCALAPPDATA "Programs\Falcon"
  New-Item -ItemType Directory -Force -Path $dest | Out-Null
  Copy-Item falcon.exe (Join-Path $dest "falcon.exe") -Force
  $path = [Environment]::GetEnvironmentVariable("Path", "User")
  if (($path -split ";") -notcontains $dest) {
    [Environment]::SetEnvironmentVariable("Path", ($path.TrimEnd(";") + ";" + $dest).TrimStart(";"), "User")
    Write-Host "Added $dest to your PATH (open a new terminal)."
  }
  Write-Host "Installed to $dest"
}
Write-Host "Run:  .\falcon.exe -open C:\path\to\your\repo"
