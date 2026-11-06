#!/bin/bash
# What does direwolf tell its clients about transmitted and received frames?
# Two direwolf modems joined by PipeWire null sinks (no radio, default audio untouched).
#   run 1: send on A's KISS, listen on A's KISS and AGW   (own transmissions)
#   run 2: send on A's KISS, listen on B's KISS and AGW   (received frames)
set -euo pipefail
T=/var/tmp/hn-probe
rm -rf $T; mkdir -p $T
mods=() pids=()
cleanup() { for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done; for m in "${mods[@]}"; do pactl unload-module "$m"; done; }
trap cleanup EXIT
mods+=("$(pactl load-module module-null-sink sink_name=hnPA sink_properties=device.description=hn-probe-A)")
mods+=("$(pactl load-module module-null-sink sink_name=hnPB sink_properties=device.description=hn-probe-B)")
for s in A B; do
    k=$([ $s = A ] && echo 8201 || echo 8211); g=$([ $s = A ] && echo 8200 || echo 8210)
    o=$([ $s = A ] && echo B || echo A)
    printf 'ADEVICE pulse pulse\nARATE 48000\nACHANNELS 1\nCHANNEL 0\nMYCALL PROBE%s\nMODEM 1200\nKISSPORT 0\nKISSPORT %s\nAGWPORT %s\nDNSSD 0\n' $s $k $g > $T/dw-$s.conf
    PULSE_SINK=hnP$s PULSE_SOURCE=hnP$o.monitor direwolf -t 0 -q d -c $T/dw-$s.conf > $T/dw-$s.log 2>&1 &
    pids+=($!)
done
sleep 3
echo "=== run 1: own transmissions (listening on A)"
python3 -I /home/lweith/build/hamnews/test/echo-probe.py 8201 8201 8200
echo "=== run 2: received (listening on B)"
python3 -I /home/lweith/build/hamnews/test/echo-probe.py 8201 8211 8210
echo "--- direwolf B log"
grep -v '^\s*$' $T/dw-B.log | grep -v -E 'Ready to accept|Attached to|gone away|AGW client|Tried to read|Closing' | tail -8
