# gg installer for Windows (PowerShell 5.1+).
#   irm https://raw.githubusercontent.com/MarcZX10/gauge-and-grain/main/scripts/install.ps1 | iex
# Re-run to update. Verifies the release checksum before installing.
# Env: GG_VERSION (default: latest)  GG_INSTALL_DIR (default: %LOCALAPPDATA%\gg\bin)
$ErrorActionPreference = 'Stop'
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
  $line = Get-Content "$tmp\checksums.txt" | Where-Object { $_ -match " $([regex]::Escape($name))$" } | Select-Object -First 1
  if (-not $line) { throw "$name not listed in checksums.txt" }
  $want = ($line -split '\s+')[0].ToLower()
  $got = (Get-FileHash "$tmp\$name" -Algorithm SHA256).Hash.ToLower()
  if ($want -ne $got) { throw "checksum mismatch (expected $want, got $got); nothing installed" }
  Expand-Archive "$tmp\$name" -DestinationPath $tmp -Force
  New-Item -ItemType Directory -Path $dir -Force | Out-Null
  Copy-Item "$tmp\gg.exe" (Join-Path $dir 'gg.exe') -Force
  Write-Host "Installed to $dir\gg.exe"
  if (($env:Path -split ';') -notcontains $dir) {
    Write-Host "Add it to your PATH:  setx PATH `"$env:Path;$dir`""
  }
} finally { Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue }
