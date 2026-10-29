# gg installer for Windows (PowerShell 5.1+).
#   irm https://raw.githubusercontent.com/MarcZX10/gauge-and-grain/main/scripts/install.ps1 | iex
# Re-run to update. Verifies the release checksum, and the release signature
# when cosign is installed (see docs/PRIVACY.md#verify-a-download).
# Env: GG_VERSION (default: latest)  GG_INSTALL_DIR (default: %LOCALAPPDATA%\gg\bin)
$ErrorActionPreference = 'Stop'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
$ProgressPreference = 'SilentlyContinue'
$repo = 'MarcZX10/gauge-and-grain'
$base = $env:GG_RELEASE_BASE
if (-not $base) {
  if ($env:GG_VERSION) { $base = "https://github.com/$repo/releases/download/$($env:GG_VERSION)" }
  else { $base = "https://github.com/$repo/releases/latest/download" }
}
$dir = if ($env:GG_INSTALL_DIR) { $env:GG_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'gg\bin' }
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$name = "gg_windows_$arch.zip"
$tmp = Join-Path ([IO.Path]::GetTempPath()) ([Guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
  Write-Host "Downloading $name ..."
  Invoke-WebRequest "$base/$name" -OutFile "$tmp\$name" -UseBasicParsing
  Invoke-WebRequest "$base/checksums.txt" -OutFile "$tmp\checksums.txt" -UseBasicParsing

  # Checksums only prove the file is intact. The signature proves the
  # checksums came from this repository's release workflow.
  $cosign = Get-Command cosign -ErrorAction SilentlyContinue
  if ($cosign) {
    try {
      Invoke-WebRequest "$base/checksums.txt.pem" -OutFile "$tmp\checksums.txt.pem" -UseBasicParsing
      Invoke-WebRequest "$base/checksums.txt.sig" -OutFile "$tmp\checksums.txt.sig" -UseBasicParsing
    } catch { throw "this release has no signature (checksums.txt.pem/.sig); nothing installed" }
    & $cosign.Source verify-blob --certificate "$tmp\checksums.txt.pem" --signature "$tmp\checksums.txt.sig" `
      --certificate-identity-regexp "^https://github.com/$([regex]::Escape($repo))/\.github/workflows/release\.yml@refs/tags/v.+$" `
      --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' "$tmp\checksums.txt" *> $null
    if ($LASTEXITCODE -ne 0) { throw "the signature on checksums.txt does not match this repository's release workflow; nothing installed" }
    Write-Host "Signature verified (cosign, release workflow of $repo)."
  } else {
    Write-Warning 'cosign is not installed, so the release signature was NOT checked. The checksum only shows the download is intact. See docs/PRIVACY.md#verify-a-download.'
  }

  $line = Get-Content "$tmp\checksums.txt" | Where-Object { $_ -match " $([regex]::Escape($name))$" } | Select-Object -First 1
  if (-not $line) { throw "$name not listed in checksums.txt" }
  $want = ($line -split '\s+')[0].ToLower()
  $got = (Get-FileHash "$tmp\$name" -Algorithm SHA256).Hash.ToLower()
  if ($want -ne $got) { throw "checksum mismatch (expected $want, got $got); nothing installed" }
  Expand-Archive "$tmp\$name" -DestinationPath $tmp -Force
  New-Item -ItemType Directory -Path $dir -Force | Out-Null
  Copy-Item "$tmp\gg.exe" (Join-Path $dir 'gg.exe') -Force
  Write-Host "Installed to $dir\gg.exe"
  $user = [Environment]::GetEnvironmentVariable('Path', 'User')
  if (($user -split ';') -notcontains $dir) {
    Write-Host "Add it to your user PATH (this only changes your own PATH):"
    Write-Host "  [Environment]::SetEnvironmentVariable('Path', ([Environment]::GetEnvironmentVariable('Path','User') + ';$dir'), 'User')"
  }
} finally { Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue }
