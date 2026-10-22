<#
  Builds the Stockpile Print Helper's MSIX package (unsigned: the Microsoft
  Store signs it when it's published; a sideload test needs a signature of
  your own).

    pwsh packaging/build-msix.ps1 -Version 1.0.0.0 -Exe bin\StockpilePrintHelper.exe -Assets bin\assets -Out bin\StockpilePrintHelper.msix

  -Exe: the GUI build (go build -ldflags "-H=windowsgui -X main.Version=… -X main.Server=…").
  -Assets: a folder from `go run ./cmd/mkassets DIR`.
  Identity values come from the Store reservation on Nerdbucket's Microsoft
  Store company account (Partner Center › Product identity); the defaults
  below are placeholders until then.
#>
param(
  [Parameter(Mandatory = $true)][string]$Version,
  [Parameter(Mandatory = $true)][string]$Exe,
  [Parameter(Mandatory = $true)][string]$Assets,
  [Parameter(Mandatory = $true)][string]$Out,
  [string]$IdentityName = "Nerdbucket.StockpilePrintHelper",
  # Placeholder: the exact CN=… string is the one Partner Center shows for
  # Nerdbucket's account (Product identity › Package/Identity/Publisher).
  [string]$Publisher = "CN=Nerdbucket",
  [string]$PublisherDisplay = "Nerdbucket"
)
$ErrorActionPreference = "Stop"
if ($Version -notmatch '^\d+\.\d+\.\d+\.\d+$') { throw "Version must be four numbers, like 1.0.0.0" }

$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$stage = Join-Path ([System.IO.Path]::GetTempPath()) ("ph-msix-" + [guid]::NewGuid())
New-Item -ItemType Directory -Force -Path (Join-Path $stage "Assets") | Out-Null
try {
  Copy-Item $Exe (Join-Path $stage "StockpilePrintHelper.exe")
  Copy-Item (Join-Path $Assets "*.png") (Join-Path $stage "Assets")
  $manifest = (Get-Content -Raw (Join-Path $here "AppxManifest.xml")).
    Replace("{{IDENTITY_NAME}}", $IdentityName).
    Replace("{{PUBLISHER}}", $Publisher).
    Replace("{{PUBLISHER_DISPLAY}}", $PublisherDisplay).
    Replace("{{VERSION}}", $Version)
  Set-Content -Encoding utf8 -Path (Join-Path $stage "AppxManifest.xml") -Value $manifest

  $makeappx = Get-ChildItem "${env:ProgramFiles(x86)}\Windows Kits\10\bin\*\x64\makeappx.exe" | Sort-Object FullName | Select-Object -Last 1
  if (-not $makeappx) { throw "makeappx.exe not found (install the Windows SDK)" }
  & $makeappx.FullName pack /d $stage /p $Out /o
  if ($LASTEXITCODE -ne 0) { throw "makeappx failed ($LASTEXITCODE)" }
  $hash = (Get-FileHash -Algorithm SHA256 (Join-Path $stage "StockpilePrintHelper.exe")).Hash.ToLower()
  Write-Output "Built $Out"
  Write-Output "Program SHA-256: $hash (publish this with the release)"
} finally {
  Remove-Item -Recurse -Force $stage
}
