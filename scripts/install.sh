#!/bin/sh
# gg installer for Linux and macOS.
#   curl -fsSL https://github.com/MarcZX10/gauge-and-grain/releases/latest/download/install.sh | sh
# Re-run it any time to update. It verifies the release signature with cosign
# (required; nothing is installed without it) and the release checksum
# (see docs/PRIVACY.md#verify-a-download).
# Env: GG_VERSION (default: latest)  GG_INSTALL_DIR (default: ~/.local/bin)
#      GG_ALLOW_UNVERIFIED=1 installs without cosign, checking only the checksum (not recommended)
set -eu

# Keep this whole file inside main(): a download cut short must not run a
# partial script.
main() {
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

  # Fail closed: without cosign the release signature cannot be checked, so
  # nothing is downloaded or installed unless the user opts out explicitly.
  if ! command -v cosign >/dev/null 2>&1 && [ "${GG_ALLOW_UNVERIFIED:-}" != 1 ]; then
    echo "gg: cosign is not installed, so the release signature cannot be checked; nothing installed." >&2
    echo "gg: install cosign (https://docs.sigstore.dev/cosign/system_config/installation/) and run this again." >&2
    echo "gg: to install without the signature check anyway, set GG_ALLOW_UNVERIFIED=1 (the checksum still applies)." >&2
    exit 1
  fi

  name="gg_${os}_${arch}.tar.gz"
  tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
  get() { curl --proto '=https' --tlsv1.2 -fsSL "$1" -o "$2"; }

  echo "Downloading $name ..."
  get "$BASE/$name" "$tmp/$name"
  get "$BASE/checksums.txt" "$tmp/checksums.txt"

  # Checksums only prove the file is intact. The signature proves the
  # checksums came from this repository's release workflow.
  if command -v cosign >/dev/null 2>&1; then
    get "$BASE/checksums.txt.pem" "$tmp/checksums.txt.pem" || { echo "gg: this release has no signature (checksums.txt.pem); nothing installed" >&2; exit 1; }
    get "$BASE/checksums.txt.sig" "$tmp/checksums.txt.sig" || { echo "gg: this release has no signature (checksums.txt.sig); nothing installed" >&2; exit 1; }
    cosign verify-blob \
      --certificate "$tmp/checksums.txt.pem" --signature "$tmp/checksums.txt.sig" \
      --certificate-identity-regexp "^https://github.com/$REPO/\.github/workflows/release\.yml@refs/tags/v.+\$" \
      --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
      "$tmp/checksums.txt" >/dev/null 2>&1 \
      || { echo "gg: the signature on checksums.txt does not match this repository's release workflow; nothing installed" >&2; exit 1; }
    echo "Signature verified (cosign, release workflow of $REPO)."
  else
    echo "gg: WARNING: GG_ALLOW_UNVERIFIED=1 and cosign is not installed, so the release signature was NOT checked." >&2
    echo "gg:          the checksum only shows the download is intact. See docs/PRIVACY.md#verify-a-download." >&2
  fi

  want=$(grep " $name\$" "$tmp/checksums.txt" | cut -d' ' -f1)
  [ -n "$want" ] || { echo "gg: $name not listed in checksums.txt" >&2; exit 1; }
  got=$(sum "$tmp/$name")
  [ "$want" = "$got" ] || { echo "gg: checksum mismatch (expected $want, got $got); nothing installed" >&2; exit 1; }

  tar -xzf "$tmp/$name" -C "$tmp" gg
  mkdir -p "$DIR"
  install -m 0755 "$tmp/gg" "$DIR/gg"
  echo "Installed $("$DIR/gg" version) to $DIR/gg"
  case ":$PATH:" in *":$DIR:"*) ;; *) echo "Note: $DIR is not on your PATH. Add:  export PATH=\"$DIR:\$PATH\"";; esac
}

main "$@"
