#!/bin/bash
# Runs inside a booted Debian container (root). .debs are bind-mounted at /pkgs.
#   deb-pkg-test.sh ARCH [qemu]
# qemu: the arch is emulated by qemu-user, whose binfmt entry lacks the C flag,
# so setuid innbind runs unprivileged and can't bind port 119. Give innd the
# capability with a drop-in instead. Test-only; on real hardware setuid works.
set -euo pipefail
a=${1:-amd64}
if [ "${2:-}" = qemu ]; then
    mkdir -p /etc/systemd/system/hamnews.service.d
    printf '[Service]\n# test only: qemu-user ignores setuid\nAmbientCapabilities=CAP_NET_BIND_SERVICE\n' \
        > /etc/systemd/system/hamnews.service.d/qemu-test.conf
    echo "qemu-user: innbind setuid replaced by a test-only CAP_NET_BIND_SERVICE drop-in"
fi
export DEBIAN_FRONTEND=noninteractive
say() { printf '\n### %s\n' "$*"; }
nntp() {
    exec 3<>/dev/tcp/127.0.0.1/119
    read -r l <&3; echo "$l"
    for c in "$@"; do printf '%s\r\n' "$c" >&3; read -r l <&3; echo "$l"; done
    printf 'QUIT\r\n' >&3; exec 3>&-
}
settle() { for _ in $(seq 40); do systemctl is-active -q "$1" && return; sleep 0.5; done; systemctl is-active "$1"; }

say "install with no answers: nothing runs, nothing fails"
dpkg -i /pkgs/hamnews_*_"$a".deb /pkgs/hnlink_*_"$a".deb 2>&1 | grep -Ev '^(\(Reading|Selecting|Preparing|Unpacking)' || true
dpkg -s hamnews hnlink | grep -E '^(Package|Status)'
id news
stat -c '%A %U:%G %n' /usr/lib/hamnews/bin/innbind /etc/hamnews/incoming.conf /var/lib/hamnews
grep -E '^(pathhost|domain):' /etc/hamnews/inn.conf
systemctl is-enabled hamnews hnlink
systemctl is-active hamnews hnlink || true
grep -E '^CALL=' /etc/hnlink.conf
dpkg-query -W -f='${Conffiles}\n' hamnews | grep -c etc/hamnews
dpkg-query -W -f='${Conffiles}\n' hamnews hnlink | grep -E 'inn.conf|hnlink.conf' && { echo "generated file listed as conffile"; exit 1; } || true

say "invalid preseed is refused"
echo 'hamnews hamnews/callsign string w1-tst' | debconf-set-selections
dpkg-reconfigure hamnews 2>&1 | tail -2
grep -E '^pathhost:' /etc/hamnews/inn.conf

say "preseed callsign, reconfigure: server comes up"
echo 'hamnews hamnews/callsign string W1TST' | debconf-set-selections
printf 'ham.general\nham.swap n\n' >> /etc/hamnews/groups
dpkg-reconfigure hamnews
grep -E '^(pathhost|domain):' /etc/hamnews/inn.conf
settle hamnews
sleep 2
journalctl -u hamnews --no-pager -o cat | grep -E 'creating news|created group'
nntp 'MODE READER' 'GROUP ham.general'

say "hnlink: preseed with awkward From:"
printf '%s\n' 'hnlink hnlink/call string w1tst-5' 'hnlink hnlink/from string W1TST & co <w1/tst@example.invalid>' | debconf-set-selections
dpkg-reconfigure hnlink
grep -E '^(CALL|FROM)=' /etc/hnlink.conf
settle hnlink
sleep 2
journalctl -u hnlink --no-pager -o cat | grep -E 'hnlink 0|KISS' | head -2
exec 3<>/dev/tcp/127.0.0.1/8073
printf 'GET /api/status HTTP/1.0\r\nHost: localhost\r\n\r\n' >&3
head -1 <&3; exec 3>&-

say "batch/load as a user"
useradd -m op
art=$'Path: k6zzz!not-for-mail\nFrom: K6ZZZ <k6zzz@example.invalid>\nNewsgroups: ham.general\nSubject: deb test\nMessage-ID: <debtest.1@k6zzz.invalid>\nDate: '"$(date -R)"$'\n\nhello\n'
printf '#! rnews %d\n%s' "${#art}" "$art" > /tmp/t.rnews
runuser -u op -- hnlink load /tmp/t.rnews
runuser -u op -- hnlink batch /tmp/out.rnews

say "hand edits survive reinstall; debconf follows the files"
echo '# local edit' >> /etc/hamnews/readers.conf
sed -i 's/^pathhost:.*/pathhost:       w1zzz/' /etc/hamnews/inn.conf
sed -i 's/^CALL=.*/CALL=W1ZZZ-7/' /etc/hnlink.conf
dpkg -i --force-confold /pkgs/hamnews_*_"$a".deb /pkgs/hnlink_*_"$a".deb > /tmp/reinst.log 2>&1 || { cat /tmp/reinst.log; exit 1; }
grep -q '# local edit' /etc/hamnews/readers.conf && echo "readers.conf edit kept"
grep -E '^(pathhost|domain):' /etc/hamnews/inn.conf
grep -E '^CALL=' /etc/hnlink.conf
stat -c '%A %U:%G %n' /usr/lib/hamnews/bin/innbind
settle hamnews; settle hnlink
nntp 'MODE READER' 'GROUP ham.general'

say "itsjustnews"
dpkg -i /pkgs/itsjustnews_*_"$a".deb > /dev/null
itsjustnews -version
: > /tmp/empty.kiss
itsjustnews -file /tmp/empty.kiss

if ls /pkgs/airtime_*_"$a".deb >/dev/null 2>&1; then
    say "airtime (no direwolf here: it serves and retries)"
    dpkg -i /pkgs/airtime_*_"$a".deb > /dev/null
    airtime -version
    settle airtime
    exec 3<>/dev/tcp/127.0.0.1/8075
    printf 'GET /metrics HTTP/1.0\r\nHost: localhost\r\n\r\n' >&3
    grep -a '^airtime_agw_up' <&3; exec 3>&-
    dpkg --purge airtime > /dev/null
fi

say "purge"
dpkg --purge hnlink hamnews > /dev/null
for f in /etc/hamnews /etc/hnlink.conf /var/lib/hamnews /var/spool/hamnews; do
    [ -e "$f" ] && echo "  LEFT BEHIND: $f" || echo "  gone: $f"
done
echo "debconf entries left: $(debconf-show hamnews 2>/dev/null | wc -l)"
echo "ALL OK"
