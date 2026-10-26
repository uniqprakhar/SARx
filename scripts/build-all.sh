#!/usr/bin/env bash
# Cross-compiles sarx for every supported platform into dist/release/ with checksums.
# Usage: VERSION=0.1.0 ./scripts/build-all.sh
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION="${VERSION:-dev}"
OUT=dist/release
rm -rf "$OUT" && mkdir -p "$OUT"
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
  os="${target%/*}"; arch="${target#*/}"
  name="sarx_${os}_${arch}"
  stage="$(mktemp -d)"
  bin=sarx; [ "$os" = windows ] && bin=sarx.exe
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o "$stage/$bin" ./cmd/sarx
  if [ "$os" = windows ]; then
    (cd "$stage" && python3 -c "import zipfile,sys; z=zipfile.ZipFile(sys.argv[1],'w',zipfile.ZIP_DEFLATED); z.write('$bin'); z.close()" "$PWD/$OUT/$name.zip") 2>/dev/null \
      || (cd "$stage" && zip -q "$OLDPWD/$OUT/$name.zip" "$bin")
  else
    tar -C "$stage" -czf "$OUT/$name.tar.gz" "$bin"
  fi
  rm -rf "$stage"
  echo "built $name"
done
(cd "$OUT" && { sha256sum sarx_* 2>/dev/null || shasum -a 256 sarx_*; } > checksums.txt)
echo "done: $OUT"
