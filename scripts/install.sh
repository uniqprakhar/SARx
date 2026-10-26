#!/bin/sh
# Installs sarx on Linux or macOS.
#   curl -fsSL https://raw.githubusercontent.com/uniqprakhar/sarx/main/scripts/install.sh | sh
# Set SARX_REPO to your fork/owner (or SARX_BASE_URL to a mirror), SARX_VERSION to pin a release, SARX_BIN_DIR to choose the target.
set -eu

REPO="${SARX_REPO:-uniqprakhar/sarx}"
VERSION="${SARX_VERSION:-latest}"
BIN_DIR="${SARX_BIN_DIR:-$HOME/.local/bin}"

case "$(uname -s)" in
  Linux)  os=linux ;;
  Darwin) os=darwin ;;
  *) echo "sarx: unsupported OS $(uname -s). On Windows use install.ps1." >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64)  arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "sarx: unsupported CPU $(uname -m)" >&2; exit 1 ;;
esac

if [ -n "${SARX_BASE_URL:-}" ]; then
  base="$SARX_BASE_URL"   # a mirror or local server holding the release files
elif [ "$VERSION" = latest ]; then
  base="https://github.com/$REPO/releases/latest/download"
else
  base="https://github.com/$REPO/releases/download/v${VERSION#v}"
fi
file="sarx_${os}_${arch}.tar.gz"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "downloading $base/$file"
if command -v curl >/dev/null 2>&1; then
  curl -fsSL "$base/$file" -o "$tmp/$file" && curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt"
elif command -v wget >/dev/null 2>&1; then
  wget -q "$base/$file" -O "$tmp/$file" && wget -q "$base/checksums.txt" -O "$tmp/checksums.txt"
else
  echo "sarx: need curl or wget" >&2; exit 1
fi

want="$(grep " $file\$" "$tmp/checksums.txt" | cut -d' ' -f1)"
if command -v sha256sum >/dev/null 2>&1; then got="$(sha256sum "$tmp/$file" | cut -d' ' -f1)"; else got="$(shasum -a 256 "$tmp/$file" | cut -d' ' -f1)"; fi
if [ -z "$want" ] || [ "$want" != "$got" ]; then echo "sarx: checksum mismatch, not installing" >&2; exit 1; fi

tar -xzf "$tmp/$file" -C "$tmp"
mkdir -p "$BIN_DIR"
mv "$tmp/sarx" "$BIN_DIR/sarx"
chmod +x "$BIN_DIR/sarx"
echo "installed $BIN_DIR/sarx"
case ":$PATH:" in *":$BIN_DIR:"*) ;; *) echo "note: add $BIN_DIR to your PATH" ;; esac
echo "next: cd your-project && sarx init"
