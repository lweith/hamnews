#!/bin/bash
# Runs inside a booted Arch container (root). Packages are bind-mounted at /pkgs.
set -euo pipefail
say() { printf '\n### %s\n' "$*"; }
nntp() { # nntp CMD... : send commands to localhost:119, print replies
    exec 3<>/dev/tcp/127.0.0.1/119
    read -r l <&3; echo "$l"
    for c in "$@"; do printf '%s\r\n' "$c" >&3; read -r l <&3; echo "$l"; done
    printf 'QUIT\r\n' >&3; exec 3>&-
}

say "install"
pacman -U --noconfirm /pkgs/hamnews-*.pkg.tar.zst /pkgs/hnlink-*.pkg.tar.zst /pkgs/itsjustnews-*.pkg.tar.zst 2>&1 | grep -E '==>|error|warning|installing' || true
id news
stat -c '%A %U:%G %n' /usr/lib/hamnews/bin/innbind /etc/hamnews/incoming.conf /var/lib/hamnews /var/spool/hamnews

say "unconfigured: hamnews refuses, hnlink skips"
systemctl start hamnews || true
systemctl is-active hamnews || true
journalctl -u hamnews -n 3 --no-pager -o cat | grep -E 'CHANGEME|callsign|pathhost' | tail -1
systemctl start hnlink || true
systemctl show hnlink -p ActiveState -p Result -p ConditionResult --value | tr '\n' ' '; echo
journalctl -u hnlink -n 5 --no-pager -o cat | grep -E 'set CALL' | tail -1

say "configure and start"
sed -i 's/^pathhost:.*/pathhost:       w1tst/; s/^domain:.*/domain:         w1tst/' /etc/hamnews/inn.conf
printf 'ham.test\nham.general\nham.swap n\n' >> /etc/hamnews/groups
systemctl reset-failed hamnews 2>/dev/null || true
systemctl enable --now hamnews
sleep 3
systemctl is-active hamnews
journalctl -u hamnews --no-pager -o cat | grep -E 'creating news databases|created group' || true
grep -E '^ham\.' /var/lib/hamnews/active
nntp 'MODE READER' 'GROUP ham.general'

say "hnlink load / batch (as an ordinary user, over NNTP)"
useradd -m op
a=$'Path: k6zzz!not-for-mail\nFrom: K6ZZZ <k6zzz@example.invalid>\nNewsgroups: ham.general\nSubject: pkg test\nMessage-ID: <pkgtest.1@k6zzz.invalid>\nDate: '"$(date -R)"$'\n\nhello\n'
printf '#! rnews %d\n%s' "${#a}" "$a" > /tmp/t.rnews
runuser -u op -- hnlink load /tmp/t.rnews
runuser -u op -- hnlink load /tmp/t.rnews
runuser -u op -- hnlink batch /tmp/out.rnews

say "hnlink service once CALL is set (no TNC here: it should run and retry)"
sed -i 's/^CALL=.*/CALL=W1TST-5/; s/^FROM=.*/FROM=W1TST <w1tst@example.invalid>/' /etc/hnlink.conf
systemctl enable --now hnlink
sleep 4
systemctl is-active hnlink
journalctl -u hnlink --no-pager -o cat | grep -E 'hnlink 0|page:|KISS' | head -3
exec 3<>/dev/tcp/127.0.0.1/8073
printf 'GET /api/status HTTP/1.0\r\nHost: localhost\r\n\r\n' >&3
head -1 <&3; exec 3>&-
systemctl show hnlink -p User --value; ps -o user= -C hnlink

say "upgrade keeps local edits"
echo '# local edit' >> /etc/hamnews/readers.conf
pacman -U --noconfirm /pkgs/hamnews-*.pkg.tar.zst 2>&1 | grep -E 'warning|pacnew|reinstalling' || true
grep -q '# local edit' /etc/hamnews/readers.conf && echo "readers.conf edit kept"
grep -q '^pathhost: *w1tst' /etc/hamnews/inn.conf && echo "inn.conf edit kept"
sleep 3
systemctl is-active hamnews

say "restart is clean; groups idempotent"
systemctl restart hamnews; sleep 2; systemctl is-active hamnews
nntp 'MODE READER' 'GROUP ham.general'
itsjustnews -version
echo "ALL OK"
