#!/bin/bash
# Build every package and run the install tests in clean booted containers.
#   test/run-all.sh            -> logs in dist/test-*.log, summary on stdout
# Clean roots: packaging/mkroots.sh (Debian/Raspbian) and /var/tmp/hn-arch-base (pacstrap).
set -uo pipefail
top=/home/lweith/build/hamnews
cd "$top"
mkdir -p dist
rm -f dist/test-*.log dist/deb/*.deb dist/arch/*.pkg.tar.zst   # no stale results
rc=0
step() { # step NAME CMD...
    local name=$1; shift
    if "$@" > "dist/test-$name.log" 2>&1; then echo "PASS $name"; else echo "FAIL $name (dist/test-$name.log)"; rc=1; fi
}
step build-deb  packaging/build-deb.sh amd64 arm64 armhf
step build-arch packaging/build-arch.sh
step deb-amd64  test/nspawn-test.sh /var/tmp/hn-deb/bookworm-amd64-clean dist/deb test/deb-pkg-test.sh amd64
step deb-arm64  test/nspawn-test.sh /var/tmp/hn-deb/bookworm-arm64-clean dist/deb test/deb-pkg-test.sh arm64 qemu
step deb-armhf  test/nspawn-test.sh /var/tmp/hn-deb/raspbian-armhf-clean dist/deb test/deb-pkg-test.sh armhf qemu
step arch       test/nspawn-test.sh /var/tmp/hn-arch-base dist/arch test/arch-pkg-test.sh
exit $rc
