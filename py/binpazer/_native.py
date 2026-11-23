"""Loads the C reference's CRC-32C from a shared library that is built from c/binpazer.c.

`make -C py native` builds the library. An import builds it too when it is absent or older than its source.
"""

from __future__ import annotations

import ctypes
import os
import subprocess
import sys
from pathlib import Path

_SUFFIX = {"darwin": ".dylib", "win32": ".dll"}.get(sys.platform, ".so")
LIBRARY = Path(__file__).with_name("_binpazer_native" + _SUFFIX)
SOURCE = Path(__file__).resolve().parents[2] / "c" / "binpazer.c"


def build_command(out: Path) -> list[str]:
	return [os.environ.get("CC", "cc"), "-std=c11", "-O2", "-shared", "-fPIC", str(SOURCE), "-o", str(out)]


def _stale() -> bool:
	if not LIBRARY.exists():
		return True
	return SOURCE.exists() and SOURCE.stat().st_mtime > LIBRARY.stat().st_mtime


def _build() -> None:
	if not SOURCE.exists():
		raise ImportError(f"binpazer: {LIBRARY} is absent and its source {SOURCE} is not there to build it from")
	# The compiler writes a private file, and a rename puts it in place, so a second process never loads half a library.
	tmp = LIBRARY.with_name(f"{LIBRARY.name}.{os.getpid()}.tmp")
	cmd = build_command(tmp)
	try:
		done = subprocess.run(cmd, capture_output=True, text=True)
	except OSError as cause:
		raise ImportError(f"binpazer: cannot build {LIBRARY}: {cause}. The command is: {' '.join(cmd)}") from cause
	if done.returncode != 0:
		tmp.unlink(missing_ok=True)
		raise ImportError(f"binpazer: cannot build {LIBRARY}: {' '.join(cmd)} exited with status {done.returncode}\n{done.stderr}")
	os.replace(tmp, LIBRARY)


def _load():
	if _stale():
		_build()
	lib = ctypes.CDLL(str(LIBRARY))
	fn = lib.binpazer_crc32c
	fn.restype = ctypes.c_uint32
	fn.argtypes = [ctypes.c_uint32, ctypes.c_void_p, ctypes.c_size_t]
	return fn


_crc32c = _load()
_char = ctypes.c_char


def crc32c(crc: int, data) -> int:
	"""CRC-32C (Castagnoli) of data, continued from crc. Start with 0."""
	if type(data) is bytes:
		return _crc32c(crc, data, len(data))
	view = memoryview(data)
	n = view.nbytes
	if n == 0:
		return crc
	if view.readonly:
		return _crc32c(crc, view.tobytes(), n)
	return _crc32c(crc, (_char * n).from_buffer(view), n)
