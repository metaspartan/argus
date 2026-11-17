#!/bin/sh
# Cross-compile static, dependency-free binaries for every supported target into dist/.
set -eu
cd "$(dirname "$0")"
VERSION=${VERSION:-$(git describe --tags --always 2>/dev/null || echo 0.2.0)}
mkdir -p dist
for t in linux/amd64 linux/arm64 linux/riscv64 linux/arm darwin/arm64 darwin/amd64 windows/amd64 windows/arm64 freebsd/amd64; do
  os=${t%/*}; arch=${t#*/}; ext=""
  [ "$os" = windows ] && ext=".exe"
  out="dist/argus-$os-$arch$ext"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch GOARM=7 go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$out" .
  printf '  %-34s %s\n' "$out" "$(du -h "$out" | cut -f1)"
done
