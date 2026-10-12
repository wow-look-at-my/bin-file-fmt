package binpazer

import (
	"encoding/binary"
	"hash/crc32"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rawChild builds one on-disk child block: header + payload (+ CRC when
// FlagHasCRC is set) + zero padding to several bytes.
func rawChild(tid, flags uint16, payload []byte) []byte {
	var h [8]byte
	binary.LittleEndian.PutUint16(h[0:], tid)
	binary.LittleEndian.PutUint16(h[2:], flags)
	binary.LittleEndian.PutUint32(h[4:], uint32(len(payload)))
	b := append(h[:], payload...)
	if flags&FlagHasCRC != 0 {
		crc := crc32.Update(0, crcTable, h[:])
		crc = crc32.Update(crc, crcTable, payload)
		var cb [4]byte
		binary.LittleEndian.PutUint32(cb[:], crc)
		b = append(b, cb[:]...)
	}
	for len(b)%8 != 0 {
		b = append(b, 0)
	}
	return b
}

func TestParseGroupPayload(t *testing.T) {
	gp := append(rawChild(1, FlagHasCRC, []byte("mesh-data")), rawChild(2, 0, []byte("note"))...)

	children, err := ParseGroupPayload(gp)
	require.NoError(t, err)
	require.Len(t, children, 2)

	assert.Equal(t, uint16(1), children[0].TypeID)
	assert.Equal(t, FlagHasCRC, children[0].Flags)
	assert.Equal(t, uint64(0), children[0].Offset)
	assert.Equal(t, "mesh-data", string(children[0].Payload))
	assert.True(t, children[0].CRCOK)

	assert.Equal(t, uint16(2), children[1].TypeID)
	assert.Equal(t, "note", string(children[1].Payload))
	assert.True(t, children[1].CRCOK)
	assert.Equal(t, uint64(0), children[1].Offset%8, "children must stay 8-byte aligned")

	// Empty payload: zero children, no error.
	children, err = ParseGroupPayload(nil)
	require.NoError(t, err)
	assert.Empty(t, children)
}

func TestParseGroupPayloadCRCMismatch(t *testing.T) {
	gp := rawChild(1, FlagHasCRC, []byte("mesh-data"))
	gp[8] ^= 0xFF // corrupt the first payload byte

	children, err := ParseGroupPayload(gp)
	require.NoError(t, err, "a CRC mismatch is recorded, not fatal")
	require.Len(t, children, 1)
	assert.False(t, children[0].CRCOK)
}

func TestParseGroupPayloadMalformed(t *testing.T) {
	// A child claiming more bytes than the group holds: partial result + error.
	gp := append(rawChild(1, 0, []byte("ok")), rawChild(2, 0, []byte("victim"))...)
	binary.LittleEndian.PutUint32(gp[16+4:], 0xFFFF)
	children, err := ParseGroupPayload(gp)
	require.ErrorIs(t, err, ErrTruncated)
	require.Len(t, children, 1, "the intact prefix is still returned")
	assert.Equal(t, uint16(1), children[0].TypeID)

	// A sub-8-byte non-zero tail cannot be a block.
	_, err = ParseGroupPayload([]byte("junk!"))
	require.ErrorIs(t, err, ErrTruncated)

	// A sub-8-byte zero tail is alignment padding.
	children, err = ParseGroupPayload(append(rawChild(1, 0, nil), 0, 0, 0, 0))
	require.NoError(t, err)
	assert.Len(t, children, 1)
}
