package pve

import "strings"

// MatchesGPUResourceMapping compares the complete property set emitted by our
// GPU writers. Order is immaterial; unknown/duplicate keys and implicit
// defaults are not silently accepted as equivalent configuration.
func MatchesGPUResourceMapping(raw, mapping, mdev string) bool {
	if mapping == "" || len(raw) > 1024 {
		return false
	}
	expected := map[string]string{"mapping": mapping, "pcie": "1", "x-vga": "1"}
	if mdev != "" {
		expected = map[string]string{"mapping": mapping, "mdev": mdev}
	}
	parts := strings.Split(raw, ",")
	if len(parts) != len(expected) {
		return false
	}
	for _, part := range parts {
		key, value, ok := strings.Cut(part, "=")
		want, known := expected[key]
		if !ok || !known || value != want {
			return false
		}
		delete(expected, key)
	}
	return len(expected) == 0
}
