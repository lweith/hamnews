#!/bin/bash
# airtime against real direwolf: two modems on PipeWire null sinks. airtime
# watches A's AGW port; A transmits 4 frames (own TX), B transmits 2 (A hears).
set -euo pipefail
T=/var/tmp/hn-airtime
AT=/var/tmp/hn-airtime/airtime
rm -rf $T; mkdir -p $T
(cd /home/lweith/build/hamnews/hnlink && go build -o $AT ./cmd/airtime)
mods=() pids=()
cleanup() { for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done; for m in "${mods[@]}"; do pactl unload-module "$m"; done; }
trap cleanup EXIT
mods+=("$(pactl load-module module-null-sink sink_name=hnTA sink_properties=device.description=hn-airtime-A)")
mods+=("$(pactl load-module module-null-sink sink_name=hnTB sink_properties=device.description=hn-airtime-B)")
for s in A B; do
    k=$([ $s = A ] && echo 8301 || echo 8311); g=$([ $s = A ] && echo 8300 || echo 8310)
    o=$([ $s = A ] && echo B || echo A)
    printf 'ADEVICE pulse pulse\nARATE 48000\nACHANNELS 1\nCHANNEL 0\nMYCALL TEST%s\nMODEM 1200\nKISSPORT 0\nKISSPORT %s\nAGWPORT %s\nDNSSD 0\n' $s $k $g > $T/dw-$s.conf
    PULSE_SINK=hnT$s PULSE_SOURCE=hnT$o.monitor direwolf -t 0 -q d -c $T/dw-$s.conf > $T/dw-$s.log 2>&1 &
    pids+=($!)
done
sleep 3
$AT -agw 127.0.0.1:8300 -http 127.0.0.1:8375 -pcap $T/trace.pcap -v > $T/airtime.log 2>&1 &
pids+=($!)
sleep 1
python3 -I /home/lweith/build/hamnews/test/airtime-live.py 8301 8311
echo "--- airtime log"
cut -c21- $T/airtime.log
echo "--- metrics"
curl -fsS http://127.0.0.1:8375/metrics | grep -E '^airtime_(frames_total|seconds_total|channel_busy_ratio|agw_up)'
echo "--- api/stats channels"
curl -fsS http://127.0.0.1:8375/api/stats | python3 -I -c 'import json,sys; d=json.load(sys.stdin); print(d["channels"])'
echo "--- pcap"
ls -l $T/trace.pcap
if command -v tshark >/dev/null; then tshark -r $T/trace.pcap 2>&1 | head -12; else echo "(no tshark)"; fi
fails=0
curl -fsS http://127.0.0.1:8375/metrics | grep -q 'airtime_frames_total{port="0",call="TESTA-5",dir="tx"} 4' || { echo "FAIL: A tx != 4"; fails=1; }
curl -fsS http://127.0.0.1:8375/metrics | grep -q 'airtime_frames_total{port="0",call="TESTB-5",dir="rx"} 2' || { echo "FAIL: B rx != 2"; fails=1; }
[ $fails = 0 ] && echo "AIRTIME LIVE OK" || { echo "AIRTIME LIVE FAILED"; exit 1; }
