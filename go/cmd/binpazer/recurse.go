package main

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	binpazer "github.com/wow-look-at-my/bin-file-fmt/go"
)

// maxNestDepth bounds group recursion against hostile or corrupt nesting.
const maxNestDepth = 32

// typeTable is a lookup view of a file's Block Type Table.
type typeTable struct {
	names map[uint16]string
	guids map[uint16]binpazer.GUID
}

func newTypeTable(br *binpazer.Reader) *typeTable {
	t := &typeTable{
		names: make(map[uint16]string, len(br.Types)),
		guids: make(map[uint16]binpazer.GUID, len(br.Types)),
	}
	for _, e := range br.Types {
		if _, dup := t.guids[e.TypeID]; dup {
			continue // first entry wins, matching the reader's resolution
		}
		t.names[e.TypeID] = e.Name
		t.guids[e.TypeID] = e.GUID
	}
	return t
}

// name resolves a display name: table name, else predefined name, else "".
func (t *typeTable) name(id uint16) string {
	if n := t.names[id]; n != "" {
		return n
	}
	return predefinedName(id)
}

// isGroup reports whether blocks of this type are Groups: the predefined
// Group id, or a table entry carrying the well-known Group GUID.
func (t *typeTable) isGroup(id uint16) bool {
	return id == binpazer.TypeGroup || t.guids[id] == binpazer.GroupGUID
}

// printGroupChildren renders the children of a group whose payload is in
// memory. base is the absolute file offset of the payload, label the parent's
// index path (e.g. "2"), depth the nesting level (top-level children are at a
// set value). dump selects dump-style output (hex payloads) over info-style
// lines. A malformed tail is reported inline after the intact children, and
// nested groups recurse up to maxNestDepth.
func printGroupChildren(w io.Writer, tt *typeTable, payload []byte, base uint64, label string, depth int, dump bool) {
	indent := strings.Repeat("  ", depth+1)
	if depth > maxNestDepth {
		fmt.Fprintf(w, "%s(nesting depth exceeds %d; not descending)\n", indent, maxNestDepth)
		return
	}
	children, perr := binpazer.ParseGroupPayload(payload)
	for i, c := range children {
		clabel := fmt.Sprintf("%s.%d", label, i)
		crcNote := ""
		if !c.CRCOK {
			crcNote = " (CRC MISMATCH)"
		}
		if dump {
			fmt.Fprintf(w, "block %s (type %d, %d bytes)%s:\n",
				clabel, c.TypeID, len(c.Payload), crcNote)
		} else {
			fmt.Fprintf(w, "%s[%s] type=%-6d %-14s flags=[%s] offset=%d len=%d%s\n",
				indent, clabel, c.TypeID, tt.name(c.TypeID),
				strings.Join(flagNames(c.Flags), ","), base+c.Offset, len(c.Payload), crcNote)
		}
		if tt.isGroup(c.TypeID) {
			printGroupChildren(w, tt, c.Payload, base+c.Offset+8, clabel, depth+1, dump)
		} else if dump {
			_, _ = io.WriteString(w, hex.Dump(c.Payload))
		}
	}
	if perr != nil {
		fmt.Fprintf(w, "%serror in group %s: %v\n", indent, label, perr)
	}
}

// descendGroup buffers a group block's payload and renders its children. A
// payload-level failure of the group itself (its own CRC) is reported for the
// subtree and the walk continues with the next block. Anything else (e.g.
// truncation) aborts the walk.
func descendGroup(br *binpazer.Reader, w io.Writer, tt *typeTable, b *binpazer.Block, label string, dump bool) error {
	payload, err := br.ReadPayload(b)
	if err != nil {
		if errors.Is(err, binpazer.ErrCRCMismatch) {
			fmt.Fprintf(w, "    error in group %s: %v\n", label, err)
			return br.SkipPayload(b) // realign past the trailing pad and continue
		}
		return err
	}
	printGroupChildren(w, tt, payload, b.Offset()+8, label, 1, dump)
	return nil
}
