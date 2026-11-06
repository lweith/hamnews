#!/usr/bin/env python3
"""Send frames for the airtime live test.  airtime-live.py A_KISS B_KISS"""
import socket, sys, time

a_port, b_port = int(sys.argv[1]), int(sys.argv[2])

def addr(call, ssid, last, c=False):
    b = bytes((ord(ch) << 1) for ch in call.ljust(6))
    return b + bytes([0x60 | (0x80 if c else 0) | (ssid << 1) | (1 if last else 0)])

def ui(src, ssid, info):
    f = addr("HNLINK", 0, False, c=True) + addr(src, ssid, True) + b"\x03\xf0" + info
    return b"\xc0\x00" + f.replace(b"\xdb", b"\xdb\xdd").replace(b"\xc0", b"\xdb\xdc") + b"\xc0"

ka = socket.create_connection(("127.0.0.1", a_port))
kb = socket.create_connection(("127.0.0.1", b_port))
for i in range(3):
    ka.sendall(ui("TESTA", 5, b"text from A %d" % i)); time.sleep(0.3)
ka.sendall(ui("TESTA", 5, bytes(range(256))[:120]))       # binary: how does 'T' render it?
time.sleep(3)
for i in range(2):
    kb.sendall(ui("TESTB", 5, b"text from B %d" % i)); time.sleep(0.3)
time.sleep(4)
ka.close(); kb.close()
print("sent: A 3 text + 1 binary, B 2 text")
