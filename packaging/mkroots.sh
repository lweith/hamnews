#!/bin/bash
# Create Debian-family build roots for the hamnews .debs (run once; as root).
#   /var/tmp/hn-deb/bookworm-amd64   Debian 12, x86_64
#   /var/tmp/hn-deb/bookworm-arm64   Debian 12, aarch64 (qemu binfmt)
#   /var/tmp/hn-deb/raspbian-armhf   Raspbian 12, ARMv6 hard-float (Pi Zero/1 and up)
set -euo pipefail
base=/var/tmp/hn-deb
inc=build-essential,debhelper,fakeroot,ca-certificates,systemd,systemd-sysv,dbus
mkdir -p "$base"
mk() { # name arch mirror keyring
    local d=$base/$1
    [[ -e $d/etc/os-release ]] && { echo "$1: exists"; return; }
    debootstrap --arch="$2" --variant=minbase --include="$inc" --keyring="$4" \
        bookworm "$d" "$3" > "$base/$1.log" 2>&1 || { echo "$1: FAILED (see $base/$1.log)"; return 1; }
    echo "$1: ok"
}
mk bookworm-amd64 amd64 http://deb.debian.org/debian /usr/share/keyrings/debian-archive-keyring.gpg &
mk bookworm-arm64 arm64 http://deb.debian.org/debian /usr/share/keyrings/debian-archive-keyring.gpg &
mk raspbian-armhf armhf http://archive.raspbian.org/raspbian /var/tmp/raspbian-archive-keyring.gpg &
wait
