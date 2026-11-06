#!/bin/bash
# Assemble a release from built packages (run build-deb.sh and build-arch.sh first).
#   packaging/release.sh   -> dist/release/hamnews-<ver>/ and dist/release/hamnews-<ver>.tar.gz
#
#   arch/      pacman packages + PKGBUILD
#   deb/       .debs for amd64, arm64, armhf (Raspbian, ARMv6: every Pi)
#   windows/   installer: hamnews .deb for WSL, hnlink.exe, itsjustnews.exe, airtime.exe, scripts
#   SHA256SUMS over everything
set -euo pipefail
top=/home/lweith/build/hamnews
ver=$(sed -n 's/^pkgver=//p' "$top/packaging/arch/PKGBUILD")
rel=$top/dist/release/hamnews-$ver
rm -rf "$rel"
mkdir -p "$rel/arch" "$rel/deb" "$rel/windows"

cp "$top/dist/arch/"*.pkg.tar.zst "$top/dist/arch/PKGBUILD" "$rel/arch/"
cp "$top/dist/deb/"*_"$ver"-*_*.deb "$rel/deb/"

w=$rel/windows
cp "$top/windows/"*.ps1 "$top/windows/"*.cmd "$top/windows/groups.txt" "$top/windows/README.md" \
   "$top/OPERATIONS.md" "$w/"
cp "$top/dist/deb/hamnews_$ver-"*_amd64.deb "$w/"
(cd "$top/hnlink" &&
    CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -mod=vendor -trimpath -ldflags='-s -w' -o "$w/hnlink.exe" . &&
    CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -mod=vendor -trimpath -ldflags='-s -w' -o "$w/itsjustnews.exe" ./cmd/itsjustnews &&
    CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -mod=vendor -trimpath -ldflags='-s -w' -o "$w/airtime.exe" ./cmd/airtime)
# Windows scripts want CRLF.
for f in "$w/"*.ps1 "$w/"*.cmd "$w/groups.txt"; do sed -i 's/\r*$/\r/' "$f"; done
(cd "$w" && sha256sum -- *.exe *.deb > SHA256SUMS)

cp "$top/OPERATIONS.md" "$rel/"
(cd "$rel" && find . -type f ! -name SHA256SUMS -printf '%P\n' | sort | xargs sha256sum > SHA256SUMS)
tar -C "$top/dist/release" -czf "$top/dist/release/hamnews-$ver.tar.gz" "hamnews-$ver"
ls -lR "$rel"
