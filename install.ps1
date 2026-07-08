param(
    [string]$InstallDir = "$env:USERPROFILE\.cc_proxy\bin"
)

$ErrorActionPreference = "Stop"

$Repo = "cnstark/cc-proxy"
$EnvFile = "$env:USERPROFILE\.cc_proxy\env.ps1"

# --- Detect arch ---
$Arch = if ([Environment]::Is64BitOperatingSystem) { "amd64" } else { "x86" }
if ($Arch -ne "amd64") {
    Write-Host "Unsupported architecture: $Arch (64-bit only)"
    exit 1
}

$OS = "windows"

# --- Get latest version ---
Write-Host "==> Fetching latest version..."
$ReleaseInfo = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest"
$Version = $ReleaseInfo.tag_name
Write-Host "==> Latest version: $Version"

# --- Download and extract ---
$PkgBase = "cc-proxy_${Version}_${OS}_${Arch}"
$PkgFile = "${PkgBase}.zip"
$DownloadUrl = "https://github.com/$Repo/releases/download/$Version/$PkgFile"

Write-Host "==> Downloading $PkgFile ..."
$TmpDir = Join-Path $env:TEMP "cc-proxy-$(Get-Random)"
New-Item -ItemType Directory -Force -Path $TmpDir | Out-Null
$ZipPath = Join-Path $TmpDir $PkgFile

Invoke-WebRequest -Uri $DownloadUrl -OutFile $ZipPath

Write-Host "==> Installing to $InstallDir ..."
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
Expand-Archive -Path $ZipPath -DestinationPath $TmpDir -Force

Copy-Item -Path "$TmpDir\$PkgBase\ccp.exe" -Destination $InstallDir -Force
Copy-Item -Path "$TmpDir\$PkgBase\ccp-proxy.exe" -Destination $InstallDir -Force

Remove-Item -Recurse -Force $TmpDir

# --- Generate env.ps1 ---
@"
# cc-proxy environment - dot-source this file to add binaries to PATH
`$env:PATH = "$InstallDir;`$env:PATH"
"@ | Out-File -FilePath $EnvFile -Encoding utf8

# --- Verify ---
$CcpPath = Join-Path $InstallDir "ccp.exe"
if (Test-Path $CcpPath) {
    $InstalledVer = (& $CcpPath version | Select-Object -Last 1) -replace "ccp version ", ""
    Write-Host ""
    Write-Host "✅ Installation successful! Version: $InstalledVer"
} else {
    Write-Host ""
    Write-Host "✅ Installation successful!"
}

Write-Host ""
Write-Host "To use now (current PowerShell):"
Write-Host "  & `$env:USERPROFILE\.cc_proxy\env.ps1"
Write-Host ""
Write-Host "To make permanent (add to PowerShell Profile):"
Write-Host "  echo '& `$env:USERPROFILE\.cc_proxy\env.ps1' >> `$PROFILE"
Write-Host ""
Write-Host "Quick start:"
Write-Host "  ccp help"
