package binpazer

import (
	"encoding/hex"
	"fmt"
	"strings"
)

func (g GUID) String() string {
	h := hex.EncodeToString(g[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}

// ParseGUID parses a canonical UUID string (hyphens optional) into a GUID.
func ParseGUID(s string) (GUID, error) {
	var g GUID
	clean := strings.ReplaceAll(s, "-", "")
	if len(clean) != 32 {
		return g, fmt.Errorf("binpazer: invalid GUID %q (need 32 hex digits)", s)
	}
	b, err := hex.DecodeString(clean)
	if err != nil {
		return g, fmt.Errorf("binpazer: invalid GUID %q: %w", s, err)
	}
	copy(g[:], b)
	return g, nil
}
