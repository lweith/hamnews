#!/bin/bash
# Build hamnews + hnlink .debs for amd64, arm64 and armhf (ARMv6, Raspbian) in
# the roots made by packaging/mkroots.sh.
#   packaging/build-deb.sh [amd64|arm64|armhf ...]   -> dist/deb/*.deb
set -euo pipefail
top=$(cd "$(dirname "$0")/.." && pwd)
pk=$top/packaging
out=$top/dist/deb
roots=/var/tmp/hn-deb
SUDO=/usr/bin/sudo
ver=$(sed -n '1s/^hamnews (\([^-]*\)-.*/\1/p' "$pk/debian/changelog")
archs=("$@")
[[ $# -gt 0 ]] || archs=(amd64 arm64 armhf)
mkdir -p "$out"

for a in "${archs[@]}"; do
    case $a in
        amd64) root=$roots/bookworm-amd64 goenv=(GOARCH=amd64) ;;
        arm64) root=$roots/bookworm-arm64 goenv=(GOARCH=arm64) ;;
        armhf) root=$roots/raspbian-armhf goenv=(GOARCH=arm GOARM=6) ;;
        *) echo "unknown arch $a" >&2; exit 1 ;;
    esac
    echo "=== $a"
    stage=$(mktemp -d /var/tmp/hn-deb-stage.XXXXXX)
    src=$stage/hamnews-$ver
    tar -C "$stage" -xzf "$pk/upstream/inn-2.7.4.tar.gz"
    mv "$stage/inn-2.7.4" "$src"
    patch -d "$src" -Np1 -s -i "$pk/inn-2.7.4-disable-installing-files-using-the-owner-and-group-we-also-run-as.patch"
    cp -r "$pk/debian" "$src/debian"
    mkdir -p "$src/debian/common" "$src/debian/prebuilt"
    cp "$pk/common/"* "$top/OPERATIONS.md" "$src/debian/common/"
    cp "$pk/common/hamnews.service" "$pk/common/hnlink.service" "$pk/common/airtime.service" "$src/debian/"
    (cd "$top/hnlink" && export CGO_ENABLED=0 GOOS=linux "${goenv[@]}" &&
        go build -mod=vendor -trimpath -ldflags='-s -w' -o "$src/debian/prebuilt/hnlink" . &&
        go build -mod=vendor -trimpath -ldflags='-s -w' -o "$src/debian/prebuilt/itsjustnews" ./cmd/itsjustnews &&
        go build -mod=vendor -trimpath -ldflags='-s -w' -o "$src/debian/prebuilt/airtime" ./cmd/airtime)
    cp "$top/hnlink/cmd/airtime/README.md" "$src/debian/prebuilt/airtime-README.md"
    cp "$top/hnlink/README.md" "$top/hnlink/PROTOCOL.md" "$src/debian/prebuilt/"

    $SUDO rm -rf "$root/build"
    $SUDO mkdir -p "$root/build"
    $SUDO cp -a "$src" "$root/build/"
    rm -rf "$stage"
    $SUDO systemd-nspawn -q -D "$root" --chdir="/build/hamnews-$ver" \
        dpkg-buildpackage -b -us -uc > "$out/build-$a.log" 2>&1 \
        || { echo "$a: build FAILED, see $out/build-$a.log"; tail -20 "$out/build-$a.log"; exit 1; }
    grep -a 'building package' "$out/build-$a.log" | sed 's/\x1b\[[0-9;]*m//g' || true
    cp "$root/build/"hamnews_*.deb "$root/build/"hnlink_*.deb "$root/build/"itsjustnews_*.deb "$root/build/"airtime_*.deb "$out/"
done
ls -l "$out"/*.deb
