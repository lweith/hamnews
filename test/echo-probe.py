#!/usr/bin/env python3
"""Does direwolf show a frame transmitted by one client to its other clients?

  echo-probe.py SENDKISS LISTENKISS AGWPORT

Client A sends one AX.25 UI frame on KISS. Client B (same KISS port) and an AGW
client (raw 'k' and monitor 'm' enabled) listen for 8 s and report what they got.
"""
import socket, struct, sys, threading, time

sport, kport, aport = int(sys.argv[1]), int(sys.argv[2]), int(sys.argv[3])
FEND = 0xC0

def addr(call, ssid, last):
    b = bytes((ord(c) << 1) for c in call.ljust(6))
    return b + bytes([0x60 | (ssid << 1) | (1 if last else 0)])

info = b"echo-probe %d" % int(time.time())
frame = addr("PROBE", 0, False) + addr("TEST", 7, True) + b"\x03\xf0" + info
kiss = bytes([FEND, 0x00]) + frame.replace(b"\xdb", b"\xdb\xdd").replace(b"\xc0", b"\xdb\xdc") + bytes([FEND])

got = {"kissB": [], "agw": []}

def kiss_listener():
    s = socket.create_connection(("127.0.0.1", kport)); s.settimeout(8)
    buf = b""
    try:
        while True:
            d = s.recv(4096)
            if not d: break
            buf += d
    except socket.timeout:
        pass
    got["kissB"].append(buf)

def agw_listener():
    s = socket.create_connection(("127.0.0.1", aport)); s.settimeout(8)
    for kind in (b"k", b"m"):  # raw frames on, monitoring on
        s.sendall(struct.pack("<B3xc3x10s10sII", 0, kind, b"", b"", 0, 0))
    buf = b""
    try:
        while True:
            d = s.recv(4096)
            if not d: break
            buf += d
    except socket.timeout:
        pass
    i = 0
    while i + 36 <= len(buf):
        port, kind, frm, to, dlen = buf[i], buf[i+4:i+5], buf[i+8:i+18], buf[i+18:i+28], struct.unpack("<I", buf[i+28:i+32])[0]
        data = buf[i+36:i+36+dlen]
        got["agw"].append((kind.decode(), frm.rstrip(b"\0").decode(errors="replace"), to.rstrip(b"\0").decode(errors="replace"), data))
        i += 36 + dlen

ts = [threading.Thread(target=kiss_listener), threading.Thread(target=agw_listener)]
for t in ts: t.start()
time.sleep(1)
a = socket.create_connection(("127.0.0.1", sport)); a.sendall(kiss); print("A sent:", info.decode())
for t in ts: t.join()
a.close()

kb = got["kissB"][0]
print("KISS client B saw the frame:", info in kb, f"({len(kb)} bytes total)")
for kind, frm, to, data in got["agw"]:
    print(f"AGW '{kind}' from={frm!r} to={to!r} has-frame={info in data} len={len(data)} {data[:80]!r}")
    if kind == "K":
        print("   raw K matches the sent AX.25 frame after 1 leading byte:", data[1:] == frame, "leading byte:", data[:1].hex())
if not got["agw"]:
    print("AGW: nothing")
