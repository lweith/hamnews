#!/bin/bash
# Assemble the Arch build directory and build hamnews + hnlink packages.
#   packaging/build-arch.sh            -> dist/arch/*.pkg.tar.zst
set -euo pipefail
top=$(cd "$(dirname "$0")/.." && pwd)
pk=$top/packaging
b=$top/dist/arch-build
out=$top/dist/arch
ver=$(sed -n 's/^pkgver=//p' "$pk/arch/PKGBUILD")

rm -rf "$b" && mkdir -p "$b" "$out"
cp "$pk/arch/PKGBUILD" "$pk/arch/"*.install "$pk/common/"* "$top/OPERATIONS.md" "$b/"
cp "$pk/inn-2.7.4-disable-installing-files-using-the-owner-and-group-we-also-run-as.patch" "$b/"
# INN tarball: reuse a verified local copy if there is one (makepkg still checks the signature).
for f in inn-2.7.4.tar.gz inn-2.7.4.tar.gz.asc; do
    [[ -e $pk/upstream/$f ]] && cp "$pk/upstream/$f" "$b/"
done
# hnlink source, vendored, without build outputs.
tar -C "$top" --exclude=hnlink/hnlink --exclude=hnlink/hnlink.exe -czf "$b/hnlink-$ver.tar.gz" hnlink

cd "$b"
makepkg -g >> PKGBUILD 2>/dev/null       # fill sha256sums (the empty array is overridden)
makepkg -f --noconfirm --cleanbuild
cp ./*.pkg.tar.zst "$out/"
# Keep the checksummed PKGBUILD next to the packages.
cp PKGBUILD "$out/PKGBUILD"
ls -l "$out"
