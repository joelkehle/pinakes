package configutil

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// SplitCSV returns non-empty, trimmed comma-separated values in input order.
func SplitCSV(raw string) []string {
	values := []string{}
	for _, entry := range strings.Split(raw, ",") {
		if value := strings.TrimSpace(entry); value != "" {
			values = append(values, value)
		}
	}
	return values
}

// ParseSHA256Map parses identity=sha256hex entries without retaining the
// encoded representation. Duplicate and malformed entries fail closed.
func ParseSHA256Map(raw string) (map[string][sha256.Size]byte, error) {
	result := map[string][sha256.Size]byte{}
	if strings.TrimSpace(raw) == "" {
		return result, nil
	}
	for _, rawEntry := range strings.Split(raw, ",") {
		entry := strings.TrimSpace(rawEntry)
		if entry == "" {
			return nil, fmt.Errorf("empty identity-to-hash entry")
		}
		identity, encoded, ok := strings.Cut(entry, "=")
		identity = strings.TrimSpace(identity)
		encoded = strings.TrimSpace(encoded)
		if !ok || identity == "" || encoded == "" {
			return nil, fmt.Errorf("malformed identity-to-hash entry")
		}
		if _, exists := result[identity]; exists {
			return nil, fmt.Errorf("duplicate identity %q", identity)
		}
		if len(encoded) != sha256.Size*2 {
			return nil, fmt.Errorf("identity %q must have a 64-character SHA-256 digest", identity)
		}
		decoded, err := hex.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("identity %q has invalid SHA-256 encoding", identity)
		}
		var digest [sha256.Size]byte
		copy(digest[:], decoded)
		result[identity] = digest
	}
	return result, nil
}
