"""Codec layer: the registry, the caller-buffer API, and the block-level compress and decompress helpers.

deflate uses the zlib module. zstd and lz4 call the system libzstd and liblz4 through ctypes, the libraries the C implementation links.
"""

from __future__ import annotations

import ctypes
import ctypes.util
import zlib
from ctypes import c_char_p, c_size_t, c_uint, c_void_p

from . import format as fmt
from .envelope import Compression, parse_envelope
from .format import BinpazerError, check_u16, format_guid


class Codec:
	"""The interface of a codec. Subclass it, or pass any object with these three methods to register_codec."""

	def compress_into(self, src, dst) -> int:
		"""Encodes src into the writable buffer dst and returns the encoded length. Raises short_buffer when dst is too small."""
		raise NotImplementedError

	def decompress_into(self, src, dst) -> None:
		"""Decodes src into dst. It must produce exactly len(dst) bytes."""
		raise NotImplementedError

	def bound(self, src_len: int) -> int:
		"""Returns a size that the encoded form of src_len bytes never exceeds."""
		raise NotImplementedError


def _short_buffer(need: int, have: int) -> BinpazerError:
	return BinpazerError("short_buffer", f"encoded output needs {need} bytes, the destination holds {have}")


def _wrong_length(got: int, want: int) -> BinpazerError:
	return BinpazerError("format", f"decoded payload is {got} bytes, the envelope declares {want}")


def _copy_encoded(out, dst) -> int:
	if len(out) > len(dst):
		raise _short_buffer(len(out), len(dst))
	dst[: len(out)] = out
	return len(out)


def _copy_decoded(out, dst) -> None:
	if len(out) != len(dst):
		raise _wrong_length(len(out), len(dst))
	dst[:] = out


class _Stored(Codec):
	def compress_into(self, src, dst) -> int:
		return _copy_encoded(src, dst)

	def decompress_into(self, src, dst) -> None:
		_copy_decoded(src, dst)

	def bound(self, src_len: int) -> int:
		return src_len


class _Deflate(Codec):
	def compress_into(self, src, dst) -> int:
		enc = zlib.compressobj(zlib.Z_DEFAULT_COMPRESSION, zlib.DEFLATED, -zlib.MAX_WBITS)
		return _copy_encoded(enc.compress(src) + enc.flush(), dst)

	def decompress_into(self, src, dst) -> None:
		dec = zlib.decompressobj(-zlib.MAX_WBITS)
		try:
			# One byte over the declared length shows a stream that decodes past it, and bounds the allocation.
			out = dec.decompress(src, len(dst) + 1)
		except zlib.error as cause:
			raise BinpazerError("format", "deflate stream is corrupt") from cause
		if not dec.eof:
			raise BinpazerError("format", "deflate stream is cut short or decodes past the declared length")
		_copy_decoded(out, dst)

	def bound(self, n: int) -> int:
		return n + n // 4096 + n // 16384 + n // 33554432 + 13 + 64


def _load(name: str):
	path = ctypes.util.find_library(name)
	candidates = [path] if path else []
	candidates += [f"lib{name}.so.1", f"lib{name}.dylib", f"lib{name}.dll"]
	for c in candidates:
		try:
			return ctypes.CDLL(c)
		except OSError:
			continue
	return None


def _in_ptr(src):
	"""Returns an object that ctypes passes as a const pointer to the bytes of src, with no copy when it can."""
	if isinstance(src, bytes):
		return src
	view = memoryview(src)
	if view.readonly:
		return view.tobytes()
	return (ctypes.c_char * len(view)).from_buffer(view)


def _out_ptr(dst):
	return (ctypes.c_char * len(dst)).from_buffer(dst)


class _Zstd(Codec):
	def __init__(self, lib):
		self._compress = lib.ZSTD_compress
		self._compress.restype = c_size_t
		self._compress.argtypes = [c_void_p, c_size_t, c_char_p, c_size_t, ctypes.c_int]
		self._decompress = lib.ZSTD_decompress
		self._decompress.restype = c_size_t
		self._decompress.argtypes = [c_void_p, c_size_t, c_char_p, c_size_t]
		self._bound = lib.ZSTD_compressBound
		self._bound.restype = c_size_t
		self._bound.argtypes = [c_size_t]
		self._is_error = lib.ZSTD_isError
		self._is_error.restype = c_uint
		self._is_error.argtypes = [c_size_t]
		# ZSTD_CLEVEL_DEFAULT, the level the C implementation passes.
		self._level = 3

	def compress_into(self, src, dst) -> int:
		if len(dst) == 0:
			raise _short_buffer(self.bound(len(src)), 0)
		got = self._compress(ctypes.addressof(_out_ptr(dst)), len(dst), _as_char_p(src), len(src), self._level)
		if self._is_error(got):
			if len(dst) < self.bound(len(src)):
				raise _short_buffer(self.bound(len(src)), len(dst))
			raise BinpazerError("format", "zstd failed to encode")
		return got

	def decompress_into(self, src, dst) -> None:
		if len(dst) == 0:
			scratch = bytearray(1)
			got = self._decompress(ctypes.addressof(_out_ptr(scratch)), 0, _as_char_p(src), len(src))
		else:
			got = self._decompress(ctypes.addressof(_out_ptr(dst)), len(dst), _as_char_p(src), len(src))
		if self._is_error(got):
			raise BinpazerError("format", "zstd stream is corrupt or decodes past the declared length")
		if got != len(dst):
			raise _wrong_length(got, len(dst))

	def bound(self, n: int) -> int:
		return self._bound(n)


def _as_char_p(src):
	p = _in_ptr(src)
	return p if isinstance(p, bytes) else ctypes.cast(p, c_char_p)


_LZ4F_VERSION = 100


class _Lz4(Codec):
	def __init__(self, lib):
		self._compress = lib.LZ4F_compressFrame
		self._compress.restype = c_size_t
		self._compress.argtypes = [c_void_p, c_size_t, c_char_p, c_size_t, c_void_p]
		self._bound = lib.LZ4F_compressFrameBound
		self._bound.restype = c_size_t
		self._bound.argtypes = [c_size_t, c_void_p]
		self._is_error = lib.LZ4F_isError
		self._is_error.restype = c_uint
		self._is_error.argtypes = [c_size_t]
		self._create = lib.LZ4F_createDecompressionContext
		self._create.restype = c_size_t
		self._create.argtypes = [ctypes.POINTER(c_void_p), c_uint]
		self._free = lib.LZ4F_freeDecompressionContext
		self._free.restype = c_size_t
		self._free.argtypes = [c_void_p]
		self._decompress = lib.LZ4F_decompress
		self._decompress.restype = c_size_t
		self._decompress.argtypes = [c_void_p, c_void_p, ctypes.POINTER(c_size_t), c_void_p, ctypes.POINTER(c_size_t), c_void_p]

	def compress_into(self, src, dst) -> int:
		if len(dst) == 0:
			raise _short_buffer(self.bound(len(src)), 0)
		got = self._compress(ctypes.addressof(_out_ptr(dst)), len(dst), _as_char_p(src), len(src), None)
		if self._is_error(got):
			if len(dst) < self.bound(len(src)):
				raise _short_buffer(self.bound(len(src)), len(dst))
			raise BinpazerError("format", "lz4 failed to encode")
		return got

	def decompress_into(self, src, dst) -> None:
		corrupt = BinpazerError("format", "lz4 frame is corrupt or decodes past the declared length")
		ctx = c_void_p()
		if self._is_error(self._create(ctypes.byref(ctx), _LZ4F_VERSION)):
			raise BinpazerError("format", "lz4 cannot make a decompression context")
		try:
			keep = _in_ptr(src)
			src_at = ctypes.cast(keep, c_void_p).value or 0
			out = _out_ptr(dst if len(dst) else bytearray(1))
			dst_at = ctypes.addressof(out)
			src_left = len(src)
			dst_left = len(dst)
			hint = 1
			while hint != 0:
				out_n = c_size_t(dst_left)
				in_n = c_size_t(src_left)
				hint = self._decompress(ctx, dst_at, ctypes.byref(out_n), src_at, ctypes.byref(in_n), None)
				if self._is_error(hint):
					raise corrupt
				if out_n.value == 0 and in_n.value == 0:
					# No progress with the frame still open: the input ended early, or the output is full.
					raise corrupt
				src_at += in_n.value
				src_left -= in_n.value
				dst_at += out_n.value
				dst_left -= out_n.value
			if src_left != 0:
				raise BinpazerError("format", "bytes follow the lz4 frame")
			if dst_left != 0:
				raise _wrong_length(len(dst) - dst_left, len(dst))
		finally:
			self._free(ctx)

	def bound(self, n: int) -> int:
		return self._bound(n, None)


_BUILTIN_LIBRARIES = {fmt.CODEC_ZSTD: ("zstd", _Zstd), fmt.CODEC_LZ4: ("lz4", _Lz4)}
_builtins: dict[int, Codec | None] = {fmt.CODEC_STORED: _Stored(), fmt.CODEC_DEFLATE: _Deflate()}
_registered: dict[int, Codec] = {}
_guid_registered: dict[bytes, Codec] = {}


def _builtin(codec_id: int) -> Codec | None:
	if codec_id not in _builtins and codec_id in _BUILTIN_LIBRARIES:
		name, make = _BUILTIN_LIBRARIES[codec_id]
		lib = _load(name)
		_builtins[codec_id] = None if lib is None else make(lib)
	return _builtins.get(codec_id)


def register_codec(codec_id: int, codec: Codec) -> None:
	"""Installs a codec on a registry id. It wins over a built-in codec on the same id."""
	check_u16(codec_id, "codec id")
	if codec_id in (fmt.CODEC_INVALID, fmt.CODEC_BY_GUID):
		raise BinpazerError("range", f"codec id {codec_id} cannot be registered (use register_codec_guid for 65535)")
	_registered[codec_id] = codec


def register_codec_guid(guid: bytes, codec: Codec) -> None:
	fmt.check_guid(guid, "codec GUID")
	_guid_registered[bytes(guid)] = codec


def lookup_codec(codec_id: int, guid: bytes | None = None) -> Codec | None:
	"""Returns the codec, or None when it is not available here."""
	if codec_id == fmt.CODEC_BY_GUID:
		return _guid_registered.get(bytes(guid)) if guid is not None else None
	return _registered.get(codec_id) or _builtin(codec_id)


def _must_lookup(codec_id: int, guid: bytes | None) -> Codec:
	c = lookup_codec(codec_id, guid)
	if c is not None:
		return c
	if codec_id in _BUILTIN_LIBRARIES:
		name = _BUILTIN_LIBRARIES[codec_id][0]
		raise BinpazerError("codec_unavailable", f"codec id {codec_id} needs the lib{name} shared library, and it is not installed")
	which = format_guid(guid) if codec_id == fmt.CODEC_BY_GUID and guid is not None else f"id {codec_id}"
	raise BinpazerError("codec_unavailable", f"codec {which} is not available")


def compress_bound(codec_id: int, guid: bytes | None, src_len: int) -> int:
	return _must_lookup(codec_id, guid).bound(src_len)


def _writable(dst) -> memoryview:
	view = memoryview(dst)
	if view.readonly:
		raise BinpazerError("range", "the destination buffer is read-only")
	return view.cast("B")


def compress_into(codec_id: int, guid: bytes | None, src, dst) -> int:
	"""Encodes src into the writable buffer dst and returns the encoded length. Size dst with compress_bound."""
	view = _writable(dst)
	n = _must_lookup(codec_id, guid).compress_into(src, view)
	if not isinstance(n, int) or n < 0 or n > len(view):
		raise BinpazerError("short_buffer", f"codec {codec_id} reported {n!r} bytes for a {len(view)}-byte destination")
	return n


def decompress_into(c: Compression, stored, dst) -> None:
	"""Decodes exactly c.uncompressed_length bytes into the front of the writable buffer dst."""
	codec = _must_lookup(c.codec_id, c.codec_guid)
	view = _writable(dst)
	if len(view) < c.uncompressed_length:
		raise BinpazerError("short_buffer", f"destination holds {len(view)} bytes, the envelope declares {c.uncompressed_length}")
	try:
		codec.decompress_into(stored, view[: c.uncompressed_length])
	except BinpazerError:
		raise
	except Exception as cause:
		raise BinpazerError("format", f"codec {c.codec_id} failed to decode") from cause


def compress(codec_id: int, guid: bytes | None, src) -> bytearray:
	dst = bytearray(compress_bound(codec_id, guid, len(src)))
	del dst[compress_into(codec_id, guid, src, dst) :]
	return dst


def decompress(c: Compression, stored) -> bytearray:
	"""Allocates c.uncompressed_length bytes. Gate that untrusted field before the call."""
	dst = bytearray(c.uncompressed_length)
	decompress_into(c, stored, dst)
	return dst


def compress_block(w, type_id: int, flags: int, codec_id: int, codec_guid: bytes | None, payload) -> None:
	"""Writes one block from its logical payload, encoded with codec_id."""
	w.check_can_compress()
	if codec_id == fmt.CODEC_BY_GUID and codec_guid is None:
		raise BinpazerError("range", "codec id 65535 needs a codec GUID")
	if codec_id == fmt.CODEC_STORED and lookup_codec(codec_id) is _builtins[fmt.CODEC_STORED]:
		w.block_compressed(type_id, flags, codec_id, None, payload, len(payload))
		return
	w.block_compressed(type_id, flags, codec_id, codec_guid, compress(codec_id, codec_guid, payload), len(payload))


def read_payload_decompressed(r, b):
	"""Reads the block, and decodes it when it carries an envelope. Returns a bytes-like object."""
	payload = r.read_payload(b)
	if not b.flags & fmt.FLAG_COMPRESSED:
		return payload
	c = parse_envelope(payload)
	r.claim_decoded(c.uncompressed_length)
	view = memoryview(payload)[c.header_size :]
	if c.codec_id == fmt.CODEC_STORED and lookup_codec(c.codec_id) is _builtins[fmt.CODEC_STORED]:
		if len(view) != c.uncompressed_length:
			raise _wrong_length(len(view), c.uncompressed_length)
		return view
	return decompress(c, view)
