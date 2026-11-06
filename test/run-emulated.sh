#!/bin/bash
# Install tests for the qemu-emulated Debian arches only, against dist/deb as built.
set -uo pipefail
cd /home/lweith/build/hamnews
rc=0
for a in arm64 armhf; do
    root=/var/tmp/hn-deb/bookworm-arm64-clean
    [ $a = armhf ] && root=/var/tmp/hn-deb/raspbian-armhf-clean
    if test/nspawn-test.sh $root dist/deb test/deb-pkg-test.sh $a qemu > dist/test-deb-$a.log 2>&1
    then echo "PASS deb-$a"; else echo "FAIL deb-$a (dist/test-deb-$a.log)"; rc=1; fi
done
exit $rc
