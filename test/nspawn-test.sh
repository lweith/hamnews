#!/bin/bash
# Boot a throwaway copy of a clean root and run a package test inside it.
#   test/nspawn-test.sh CLEAN_ROOT PKG_DIR TEST_SCRIPT [ARGS...]
# e.g. test/nspawn-test.sh /var/tmp/hn-deb/bookworm-amd64-clean dist/deb test/deb-pkg-test.sh amd64
# The copy is /var/tmp/hn-test-<basename of CLEAN_ROOT>; it is left in place
# for inspection and replaced on the next run. Needs /usr/bin/sudo.
set -euo pipefail
S=/usr/bin/sudo
clean=$1 pkgs=$(realpath "$2") script=$(realpath "$3"); shift 3
name=hntest-$(basename "$clean" | tr -c 'a-z0-9\n' '-' | tr -s -)
name=${name%-}
root=/var/tmp/hn-test-$(basename "$clean")

$S machinectl terminate "$name" >/dev/null 2>&1 || true
# A terminated container can take a while to let go of its tree (qemu-emulated: longer).
for _ in $(seq 60); do $S machinectl show "$name" >/dev/null 2>&1 || break; sleep 1; done
$S rm -rf "$root"
$S cp -a "$clean" "$root"
$S systemd-nspawn -q -b -M "$name" -D "$root" --private-network \
    --bind-ro="$pkgs":/pkgs --bind-ro="$(dirname "$script")":/t \
    > "$root.console.log" 2>&1 &
for _ in $(seq 60); do
    $S systemd-run -M "$name" --wait -q -P /usr/bin/systemctl is-system-running --wait >/dev/null 2>&1 && break
    sleep 1
done
rc=0
$S systemd-run -M "$name" --wait -q -P /bin/bash "/t/$(basename "$script")" "$@" || rc=$?
$S machinectl poweroff "$name" >/dev/null 2>&1 || true
echo "test exit: $rc"
exit $rc
