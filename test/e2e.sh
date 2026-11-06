#!/bin/bash
# End-to-end over real AFSK: two throwaway innd instances, two direwolf modems
# joined by PipeWire null sinks (A plays into hnA and hears hnB.monitor, and the
# reverse), two hnlink daemons. Nothing touches /opt/hamnews or the default audio
# devices. Run as the desktop user; uses /usr/bin/sudo for the innd parts.
set -euo pipefail
T=/var/tmp/hn-e2e
HN=/home/lweith/build/hamnews/hnlink/hnlink
SUDO=/usr/bin/sudo
pids=() mods=()
def_sink=$(pactl get-default-sink) def_src=$(pactl get-default-source)

cleanup() {
    set +e
    for p in "${pids[@]}"; do kill "$p" 2>/dev/null; done
    for s in a b; do
        $SUDO env INNCONF=$T/$s/etc/inn.conf $T/$s/bin/ctlinnd -t 5 shutdown e2e >/dev/null 2>&1
    done
    sleep 2
    for m in "${mods[@]}"; do pactl unload-module "$m"; done
    $SUDO pkill -f "$T/[ab]/bin/innd" 2>/dev/null
    [ "$(pactl get-default-sink)" = "$def_sink" ] && [ "$(pactl get-default-source)" = "$def_src" ] \
        && echo "audio defaults unchanged" || echo "WARNING: audio defaults changed"
    [ -n "${KEEP:-}" ] || $SUDO rm -rf $T
}
trap cleanup EXIT

(cd /home/lweith/build/hamnews/hnlink && go build -o hnlink . && go build -o itsjustnews ./cmd/itsjustnews)
MON=/home/lweith/build/hamnews/hnlink/itsjustnews
$SUDO rm -rf $T
mkdir -p $T/log
for s in a b; do
    port=$([ $s = a ] && echo 1119 || echo 1120)
    $SUDO cp -a /opt/hamnews $T/$s
    $SUDO find $T/$s/run -mindepth 1 -delete   # as root: the glob would expand unprivileged
    $SUDO sed -i -e "s|^pathnews:.*|pathnews: $T/$s|" -e "s|^pathhost:.*|pathhost: test$s|" \
        -e "s|^bindaddress:.*|bindaddress: 127.0.0.1|" -e "s|^bindaddress6:.*|bindaddress6: \"::1\"|" \
        $T/$s/etc/inn.conf
    echo "port: $port" | $SUDO tee -a $T/$s/etc/inn.conf >/dev/null
    $SUDO -u news env INNCONF=$T/$s/etc/inn.conf $T/$s/bin/innd -f >$T/log/innd-$s.log 2>&1 &
    pids+=($!)
done
for s in a b; do
    for _ in $(seq 30); do
        $SUDO -u news env INNCONF=$T/$s/etc/inn.conf $T/$s/bin/ctlinnd -t 2 mode 2>/dev/null | grep -q 'Server running' && break
        sleep 0.5
    done
    $SUDO -u news env INNCONF=$T/$s/etc/inn.conf $T/$s/bin/innconfval pathhost port
done

# Articles: 3 originate at A, 2 at B; one big one (several hundred symbols' worth
# spread over objects) to exercise multi-object sends.
mk() { # call n bodysize
    local id="<e2e.$1.$2.$(date +%s)@hamnews.test>" body
    body=$(head -c "$3" /dev/urandom | base64 -w 72)
    local a
    a=$(printf 'Path: %s!not-for-mail\nFrom: %s <nobody@example.invalid>\nNewsgroups: ham.test\nSubject: e2e %s %s\nMessage-ID: %s\nDate: %s\n\n%s\n' \
        "$1" "$1" "$1" "$2" "$id" "$(date -R)" "$body")
    printf '#! rnews %d\n%s\n' "$(( ${#a} + 1 ))" "$a"
}
{ mk testa 1 200; mk testa 2 900; mk testa 3 3000; } > $T/a.rnews
{ mk testb 1 400; mk testb 2 1500; } > $T/b.rnews
count() { $HN batch --nntp "$1" $T/count.rnews | sed -E 's/.*: ([0-9]+) articles.*/\1/'; }
base=$(count 127.0.0.1:1119)   # the copy carries the live server's articles
want=$(( base + 5 ))
echo "baseline $base articles (copied from /opt/hamnews); expecting $want on each side"
$HN load --nntp 127.0.0.1:1119 $T/a.rnews
$HN load --nntp 127.0.0.1:1120 $T/b.rnews
$HN load --nntp 127.0.0.1:1119 $T/a.rnews   # repeat: all duplicates

mods+=("$(pactl load-module module-null-sink sink_name=hnA sink_properties=device.description=hnlink-e2e-A)")
mods+=("$(pactl load-module module-null-sink sink_name=hnB sink_properties=device.description=hnlink-e2e-B)")
for s in a b; do
    S=$(echo $s | tr a-z A-Z); O=$([ $s = a ] && echo B || echo A)
    cat > $T/dw-$s.conf <<EOF
ADEVICE pulse pulse
ARATE 48000
ACHANNELS 1
CHANNEL 0
MYCALL TEST$S
MODEM 1200
KISSPORT 0
KISSPORT 81$([ $s = a ] && echo 01 || echo 02)
AGWPORT 0
EOF
    PULSE_SINK=hn$S PULSE_SOURCE=hn$O.monitor direwolf -t 0 -q d -c $T/dw-$s.conf >$T/log/dw-$s.log 2>&1 &
    pids+=($!)
done
sleep 3
# A bystander on B's modem: hears everything A transmits, never transmits itself.
$MON -kiss 127.0.0.1:8102 -record $T/air.kiss >$T/log/monitor.log 2>&1 &
pids+=($!)
for s in a b; do
    S=$(echo $s | tr a-z A-Z)
    $HN run --call TEST$S-5 --kiss 127.0.0.1:81$([ $s = a ] && echo 01 || echo 02) \
        --nntp 127.0.0.1:11$([ $s = a ] && echo 19 || echo 20) --inv 60s --quiet 1s -v \
        --http 127.0.0.1:80$([ $s = a ] && echo 73 || echo 74) --from "TEST$S <test$s@example.invalid>" >$T/log/hn-$s.log 2>&1 &
    pids+=($!)
done

start=$(date +%s); ok=0
while [ $(( $(date +%s) - start )) -lt 400 ]; do
    ca=$(count 127.0.0.1:1119); cb=$(count 127.0.0.1:1120)
    echo "t+$(( $(date +%s) - start ))s  A has $ca  B has $cb"
    if [ "$ca" = "$want" ] && [ "$cb" = "$want" ]; then ok=1; break; fi
    sleep 10
done
if [ $ok = 1 ]; then
    echo "--- GUI/API on A (http://localhost:8073)"
    U=http://localhost:8073
    curl -fsS $U/ | grep -o '<title>[^<]*</title>'
    curl -fsS $U/api/status | python3 -c 'import json,sys; s=json.load(sys.stdin); L=s["link"]; print("status: connected", s["connected"], "| local", L["local_articles"], "| peers", [(p["call"], p["articles"], p["they_lack"], p["we_lack"], p["heard"], p["hears_us"]) for p in L["peers"]])'
    curl -fsS $U/api/groups; echo
    curl -fsS $U/api/articles | python3 -c 'import json,sys; a=json.load(sys.stdin); print("articles:", len(a), "| newest:", a[0]["subject"], "|", a[0]["from"])'
    echo -n "guard, POST without X-Hnlink: "; curl -s -o /dev/null -w '%{http_code}\n' -X POST -H 'Content-Type: application/json' -d '{}' $U/api/post
    echo -n "guard, foreign Host header:   "; curl -s -o /dev/null -w '%{http_code}\n' -H 'Host: evil.example' $U/api/status
    echo -n "post via page: "
    curl -fsS -X POST -H 'Content-Type: application/json' -H 'X-Hnlink: 1' \
        -d '{"Group":"ham.test","Subject":"posted from the hnlink page","Body":"Typed into the web page at A; should arrive at B over AFSK."}' $U/api/post; echo
    want2=$(( want + 1 )); ok2=0; t0=$(date +%s)
    while [ $(( $(date +%s) - t0 )) -lt 300 ]; do
        cb=$(count 127.0.0.1:1120)
        [ "$cb" = "$want2" ] && { ok2=1; echo "page post reached B after $(( $(date +%s) - t0 ))s"; break; }
        sleep 5
    done
    [ $ok2 = 1 ] || { echo "page post did NOT reach B"; ok=0; }
    curl -fsS http://localhost:8074/api/articles | python3 -c 'import json,sys; a=json.load(sys.stdin); print("B newest:", a[0]["subject"], "|", a[0]["from"])'
    if [ -n "${SHOT:-}" ]; then
        chromium --headless=new --disable-gpu --hide-scrollbars --window-size=1400,1050 \
            --user-data-dir=$T/chrome --virtual-time-budget=4000 \
            --screenshot=/home/lweith/build/hamnews/docs/hnlink-page.png $U/ >/dev/null 2>&1 \
            && echo "screenshot: /home/lweith/build/hamnews/docs/hnlink-page.png"
    fi
fi
# Same articles, byte for byte (bodies; Path differs by design)
for s in 1119 1120; do $HN batch --nntp 127.0.0.1:$s $T/final-$s.rnews >/dev/null; done
sum() { grep -a -v -E '^(#! rnews|Path:|Xref:|Lines:|Injection-Info:)' "$1" | sort | sha256sum | cut -c1-16; }
echo "content A $(sum $T/final-1119.rnews)  B $(sum $T/final-1120.rnews)"
echo "--- hnlink A"; grep -E 'elected|object|NEED|accepted|peer' $T/log/hn-a.log | cut -c21- | head -20
echo "--- hnlink B"; grep -E 'elected|object|NEED|accepted|peer' $T/log/hn-b.log | cut -c21- | head -20
echo "--- direwolf: frames decoded A=$(grep -c 'audio level' $T/log/dw-a.log) B=$(grep -c 'audio level' $T/log/dw-b.log)"
grep -i -B3 -E 'error|could not|fail' $T/log/dw-*.log | head -20 || true
echo "--- itsjustnews (bystander on B's modem, so it hears A's sends)"
grep -E 'DECODED|MANIFEST|NEED' $T/log/monitor.log | cut -c11- | head -8
for n in 1 2 3; do
    if grep -q "^Subject: e2e testa $n\$" $T/log/monitor.log; then echo "monitor printed testa $n in plain text"
    else echo "monitor did NOT print testa $n"; ok=0; fi
done
echo -n "replay of the recorded air: "; $MON -q -file $T/air.kiss | grep -c '^Subject: e2e testa' || true
$MON -file $T/air.kiss | tail -1
[ $ok = 1 ] && echo "E2E OK" || { echo "E2E FAILED"; exit 1; }
