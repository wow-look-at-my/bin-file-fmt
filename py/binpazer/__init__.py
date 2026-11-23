"""binpazer: a block-based binary container format. SPEC.md at the repository root is the contract."""

from .codec import (
	Codec,
	compress,
	compress_block,
	compress_bound,
	compress_into,
	decompress,
	decompress_into,
	lookup_codec,
	read_payload_decompressed,
	register_codec,
	register_codec_guid,
)
from .envelope import Compression, encode_envelope_header, parse_envelope
from .format import *  # noqa: F401,F403
from .format import BinpazerError, TypeDef, TypeEntry, align8, crc32c, format_guid, parse_guid
from .group import GroupChild, GroupResult, parse_group
from .reader import Block, Reader, block_header_bytes
from .writer import Writer, resume_append_writer
