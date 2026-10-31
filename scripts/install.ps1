# gg installer for Windows (PowerShell 5.1+).
#   irm https://github.com/MarcZX10/gauge-and-grain/releases/latest/download/install.ps1 | iex
# Re-run to update. Verifies the release signature with cosign (required; nothing
# is installed without it) and the release checksum (see docs/PRIVACY.md#verify-a-download).
# Env: GG_VERSION (default: latest)  GG_INSTALL_DIR (default: %LOCALAPPDATA%\gg\bin)
#      GG_ALLOW_UNVERIFIED=1 installs without cosign, checking only the checksum (not recommended)
#      GG_RELEASE_BASE=<https URL> downloads from there instead of GitHub (for testing and mirrors).
#        The signature and checksum checks still apply to the files it serves: they must be
#        the files of a release built by this repository's release workflow.
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
# Fail closed: without cosign the release signature cannot be checked, so
# nothing is downloaded or installed unless the user opts out explicitly.
$cosign = Get-Command cosign -ErrorAction SilentlyContinue
if (-not $cosign -and $env:GG_ALLOW_UNVERIFIED -ne '1') {
  throw 'cosign is not installed, so the release signature cannot be checked; nothing installed. Install cosign (https://docs.sigstore.dev/cosign/system_config/installation/) and run this again, or set GG_ALLOW_UNVERIFIED=1 to install with only the checksum check (not recommended).'
}
$tmp = Join-Path ([IO.Path]::GetTempPath()) ([Guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
  Write-Host "Downloading $name ..."
  Invoke-WebRequest "$base/$name" -OutFile "$tmp\$name" -UseBasicParsing
  Invoke-WebRequest "$base/checksums.txt" -OutFile "$tmp\checksums.txt" -UseBasicParsing

  # Checksums only prove the file is intact. The signature proves the
  # checksums came from this repository's release workflow.
  if ($cosign) {
    try {
      Invoke-WebRequest "$base/checksums.txt.pem" -OutFile "$tmp\checksums.txt.pem" -UseBasicParsing
      Invoke-WebRequest "$base/checksums.txt.sig" -OutFile "$tmp\checksums.txt.sig" -UseBasicParsing
    } catch { throw "this release has no signature (checksums.txt.pem/.sig); nothing installed" }
    # Windows PowerShell 5.1 turns cosign's stderr (it prints "Verified OK" there) into a
    # terminating error under 'Stop', so relax the preference for this one call only.
    # $LASTEXITCODE starts at 1, so anything but a clean cosign exit fails closed.
    $savedPreference = $ErrorActionPreference
    $LASTEXITCODE = 1
    try {
      $ErrorActionPreference = 'Continue'
      & $cosign.Source verify-blob --certificate "$tmp\checksums.txt.pem" --signature "$tmp\checksums.txt.sig" `
        --certificate-identity-regexp "^https://github.com/$([regex]::Escape($repo))/\.github/workflows/release\.yml@refs/tags/v.+$" `
        --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' "$tmp\checksums.txt" 2>&1 | Out-Null
    } catch {
      $LASTEXITCODE = 1
    } finally {
      $ErrorActionPreference = $savedPreference
    }
    if ($LASTEXITCODE -ne 0) { throw "the signature on checksums.txt does not match this repository's release workflow; nothing installed" }
    Write-Host "Signature verified (cosign, release workflow of $repo)."
  } else {
    Write-Warning 'GG_ALLOW_UNVERIFIED=1 and cosign is not installed, so the release signature was NOT checked. The checksum only shows the download is intact. See docs/PRIVACY.md#verify-a-download.'
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
