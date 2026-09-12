package pve

import (
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf16"
)

// MarshalGuestJSON encodes JSON for PVE's text input-data channel. Some PVE
// versions pass Unicode Perl strings into a byte-oriented Base64 encoder;
// literal non-ASCII input then fails before QGA receives it. JSON \u escapes
// preserve the decoded value without changing the generic raw-stdin API.
// Callers must retain these exact bytes for any idempotent replay.
func MarshalGuestJSON(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(raw) > 64*1024 {
		return nil, errors.New("guest JSON input exceeds limit")
	}
	result := make([]byte, 0, len(raw))
	for _, r := range string(raw) {
		switch {
		case r < 128:
			result = append(result, byte(r))
		case r <= 0xffff:
			result = fmt.Appendf(result, `\u%04x`, r)
		default:
			hi, lo := utf16.EncodeRune(r)
			result = fmt.Appendf(result, `\u%04x\u%04x`, hi, lo)
		}
		if len(result) > 64*1024 {
			return nil, errors.New("guest JSON input exceeds limit after escaping")
		}
	}
	return result, nil
}
