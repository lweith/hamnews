#!/bin/bash
# Move a station from the old install.sh layout (/opt/hamnews) to the packages.
#   sudo packaging/migrate-from-opt.sh PKGDIR        (PKGDIR has the .pkg.tar.zst files; Arch)
#
# Keeps: pathhost, domain, bindaddress from the old inn.conf; readers.conf and
# incoming.conf verbatim; the group list; every article, the history and overview.
# Leaves /opt/hamnews in place (delete it yourself once satisfied).
# Backup of the old unit files and a tarball of /opt/hamnews: /var/tmp/hn-migrate/.
set -euo pipefail
pkgs=${1:?usage: migrate-from-opt.sh PKGDIR}
OLD=/opt/hamnews
B=/var/tmp/hn-migrate
[[ $(id -u) = 0 ]] || { echo "run as root" >&2; exit 1; }
[[ -x $OLD/bin/innd ]] || { echo "no $OLD install" >&2; exit 1; }
[[ ! -e /var/lib/hamnews/active ]] || { echo "/var/lib/hamnews/active exists: already migrated?" >&2; exit 1; }

val() { sed -n "s/^$1:[[:space:]]*//p" "$OLD/etc/inn.conf" | head -n1; }
host=$(val pathhost) dom=$(val domain) bind=$(val bindaddress)
echo "old: pathhost=$host domain=$dom bindaddress=$bind"

# count GROUP on ADDR: article count per group, as the server reports it
counts() {
    local addr=$1 out="" l g
    exec 3<>"/dev/tcp/$addr/119"
    read -r l <&3
    printf 'MODE READER\r\n' >&3; read -r l <&3
    for g in "${groups[@]}"; do
        printf 'GROUP %s\r\n' "$g" >&3; read -r l <&3
        out+="$g:$(echo "$l" | cut -d' ' -f2) "
    done
    printf 'QUIT\r\n' >&3; exec 3>&-
    echo "$out"
}
mapfile -t groups < <(awk '$1 !~ /^(control|junk)/ {print $1}' "$OLD/db/active")
before=$(counts "$bind")
echo "before: $before"

echo "== stopping the old server and backing up"
systemctl stop hamnews
systemctl disable hamnews 2>/dev/null || true
mkdir -p "$B"
stamp=$(date +%Y%m%d-%H%M%S)
cp -a /etc/systemd/system/hamnews.service "$B/hamnews.service.$stamp" 2>/dev/null || true
cp -a /etc/sysusers.d/hamnews.conf "$B/sysusers-hamnews.conf.$stamp" 2>/dev/null || true
tar -C /opt --sparse -czf "$B/opt-hamnews-$stamp.tar.gz" hamnews
echo "backup: $B/*.$stamp, $B/opt-hamnews-$stamp.tar.gz"
# These would shadow the package's unit and sysusers file of the same name.
rm -f /etc/systemd/system/hamnews.service /etc/sysusers.d/hamnews.conf
systemctl daemon-reload

echo "== installing packages"
pacman -U --noconfirm "$pkgs"/hamnews-*.pkg.tar.zst "$pkgs"/hnlink-*.pkg.tar.zst "$pkgs"/itsjustnews-*.pkg.tar.zst
# The pacman hooks normally do this at the end of the transaction; be explicit.
systemd-sysusers hamnews.conf
systemd-tmpfiles --create hamnews.conf

echo "== configuration"
sed -i -e "s/^pathhost:.*/pathhost:       $host/" -e "s/^domain:.*/domain:         $dom/" \
       -e "s/^bindaddress:.*/bindaddress:    $bind/" /etc/hamnews/inn.conf
install -m 0644 -o root -g root "$OLD/etc/readers.conf" /etc/hamnews/readers.conf
install -m 0640 -o root -g news "$OLD/etc/incoming.conf" /etc/hamnews/incoming.conf
{ echo "# Migrated from $OLD/db/active on $stamp. One group per line: NAME [FLAG]"
  awk '$1 !~ /^(control|junk)/ {print $1, $4}' "$OLD/db/active"; } > /etc/hamnews/groups
grep -E '^(pathhost|domain|bindaddress):' /etc/hamnews/inn.conf

echo "== data"
cp -a --sparse=always "$OLD/db/." /var/lib/hamnews/
cp -a "$OLD/spool/." /var/spool/hamnews/
chown -R news:news /var/lib/hamnews /var/spool/hamnews

echo "== starting"
systemctl enable --now hamnews
sleep 3
systemctl is-active hamnews
after=$(counts "$bind")
echo "after:  $after"
if [[ $before == "$after" ]]; then
    echo "MIGRATION OK: same groups and article counts on $bind"
else
    echo "MIGRATION MISMATCH. To go back:" >&2
    echo "  systemctl disable --now hamnews; pacman -R hamnews hnlink itsjustnews" >&2
    echo "  cp $B/hamnews.service.$stamp /etc/systemd/system/hamnews.service" >&2
    echo "  cp $B/sysusers-hamnews.conf.$stamp /etc/sysusers.d/hamnews.conf" >&2
    echo "  systemctl daemon-reload; systemctl enable --now hamnews" >&2
    exit 1
fi
