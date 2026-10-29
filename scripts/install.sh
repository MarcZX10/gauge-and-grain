#!/bin/sh
# gg installer for Linux and macOS.
#   curl -fsSL https://raw.githubusercontent.com/MarcZX10/gauge-and-grain/main/scripts/install.sh | sh
# Re-run it any time to update. It verifies the release checksum before installing.
# Env: GG_VERSION (default: latest)  GG_INSTALL_DIR (default: ~/.local/bin)
set -eu

REPO="MarcZX10/gauge-and-grain"
BASE="${GG_RELEASE_BASE:-}"
if [ -z "$BASE" ]; then
  if [ -n "${GG_VERSION:-}" ]; then BASE="https://github.com/$REPO/releases/download/$GG_VERSION"
  else BASE="https://github.com/$REPO/releases/latest/download"; fi
fi
DIR="${GG_INSTALL_DIR:-$HOME/.local/bin}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in linux|darwin) ;; *) echo "gg: unsupported OS '$os' (Windows: use scripts/install.ps1)" >&2; exit 1;; esac
arch=$(uname -m)
case "$arch" in x86_64|amd64) arch=amd64;; aarch64|arm64) arch=arm64;; *) echo "gg: unsupported CPU '$arch'" >&2; exit 1;; esac

for tool in curl tar; do command -v "$tool" >/dev/null || { echo "gg: '$tool' is required" >&2; exit 1; }; done
if command -v sha256sum >/dev/null; then sum() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null; then sum() { shasum -a 256 "$1" | cut -d' ' -f1; }
else echo "gg: need sha256sum or shasum to verify the download" >&2; exit 1; fi

name="gg_${os}_${arch}.tar.gz"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
echo "Downloading $name ..."
curl -fsSL "$BASE/$name" -o "$tmp/$name"
curl -fsSL "$BASE/checksums.txt" -o "$tmp/checksums.txt"

want=$(grep " $name\$" "$tmp/checksums.txt" | cut -d' ' -f1)
[ -n "$want" ] || { echo "gg: $name not listed in checksums.txt" >&2; exit 1; }
got=$(sum "$tmp/$name")
[ "$want" = "$got" ] || { echo "gg: checksum mismatch (expected $want, got $got); nothing installed" >&2; exit 1; }

tar -xzf "$tmp/$name" -C "$tmp" gg
mkdir -p "$DIR"
install -m 0755 "$tmp/gg" "$DIR/gg"
echo "Installed $("$DIR/gg" version) to $DIR/gg"
case ":$PATH:" in *":$DIR:"*) ;; *) echo "Note: $DIR is not on your PATH. Add:  export PATH=\"$DIR:\$PATH\"";; esac
