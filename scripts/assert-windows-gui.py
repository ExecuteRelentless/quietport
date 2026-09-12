#!/usr/bin/env python3
"""Fail unless a Windows binary is a GUI-subsystem program.

The console window the Quietport agent used to open at every logon is decided by one field in the PE header, before
any of the program's own code runs (docs/adr/0017). Hiding the window from inside the process was tried in 0.1.22
and did not work, so this asserts the header itself on the file that ships.
"""
import struct
import sys

WINDOWS_GUI = 2

path = sys.argv[1]
with open(path, "rb") as fh:
    b = fh.read()
pe = struct.unpack_from("<I", b, 0x3C)[0]          # e_lfanew
if b[pe:pe + 4] != b"PE\0\0":
    sys.exit("%s: not a PE file" % path)
sub = struct.unpack_from("<H", b, pe + 24 + 68)[0]  # 4 signature + 20 COFF header, Subsystem at 68 in both PE32/PE32+
if sub != WINDOWS_GUI:
    sys.exit("%s: subsystem %d, want %d (WINDOWS_GUI): Windows would give this program a console window"
             % (path, sub, WINDOWS_GUI))
print("%s: subsystem %d (WINDOWS_GUI)" % (path, sub))
