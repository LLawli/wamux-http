#!/usr/bin/env bash
# Builds the release binary once and packs it. Everything published for a
# version (the tarball, both container images) is made from dist/wamux-http.
#
# usage: scripts/release-build.sh X.Y.Z
#
# Leaves in dist/:
#   wamux-http                                  the binary the images copy
#   wamux-http-X.Y.Z-linux-amd64.tar.gz         binary, README, CHANGELOG, licenses
#   wamux-http-X.Y.Z-linux-amd64.tar.gz.sha256
set -euo pipefail
cd "$(dirname "$0")/.."

die() { echo "release-build: $*" >&2; exit 1; }

version=${1:?usage: scripts/release-build.sh X.Y.Z}
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "not a version: $version"
name="wamux-http-$version-linux-amd64"

mkdir -p dist
find dist/ -mindepth 1 -delete
mkdir -p "dist/$name"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
  -ldflags="-s -w -X github.com/LLawli/wamux-http/internal/config.Release=$version" \
  -o dist/wamux-http ./cmd/wamux-http

# The images are FROM scratch: no libc, no loader. A binary that needs
# either would not start there, so refuse it here.
if LC_ALL=C readelf -l dist/wamux-http | grep -q "Requesting program interpreter"; then
  die "dist/wamux-http is dynamically linked"
fi
if LC_ALL=C readelf -d dist/wamux-http 2>/dev/null | grep -q NEEDED; then
  die "dist/wamux-http needs shared libraries"
fi
go version -m dist/wamux-http | grep -q "CGO_ENABLED=0" || die "dist/wamux-http was built with cgo"
got=$(./dist/wamux-http --version)
[ "$got" = "wamux-http $version" ] || die "the binary reports \"$got\", not $version"
size=$(stat -c %s dist/wamux-http)
[ "$size" -gt $((10 << 20)) ] || die "dist/wamux-http is only $size bytes"

cp dist/wamux-http README.md CHANGELOG.md LICENSE-MIT LICENSE-APACHE "dist/$name/"
tar -czf "dist/$name.tar.gz" -C dist "$name"
rm -r "dist/$name"
(cd dist && sha256sum "$name.tar.gz" > "$name.tar.gz.sha256")
sha256sum dist/wamux-http
cat "dist/$name.tar.gz.sha256"
