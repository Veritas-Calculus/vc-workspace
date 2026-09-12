package pve

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestMarshalGuestJSONPreservesUnicodeAndLiteralEscapes(t *testing.T) {
	value := map[string]string{"中文鍵🙂": "é 中文🙂\n\"\\\\u4e2d\x00<>"}
	raw, err := MarshalGuestJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range raw {
		if b >= 128 {
			t.Fatal("non-ASCII QGA input")
		}
	}
	var decoded map[string]string
	if err := json.Unmarshal(raw, &decoded); err != nil || !reflect.DeepEqual(decoded, value) {
		t.Fatal("changed JSON meaning", err)
	}
	again, err := MarshalGuestJSON(value)
	if err != nil || !bytes.Equal(raw, again) {
		t.Fatal("encoding is not deterministic")
	}
	if !bytes.Contains(raw, []byte(`\ud83d\ude42`)) {
		t.Fatal("missing valid surrogate pair")
	}
}

func TestMarshalGuestJSONBoundsEscapedWireBytes(t *testing.T) {
	for _, value := range []any{strings.Repeat("中", 11000), strings.Repeat("a", 65535), make(chan int)} {
		if _, err := MarshalGuestJSON(value); err == nil {
			t.Fatal("invalid/oversized input accepted")
		}
	}
	raw, err := MarshalGuestJSON(strings.Repeat("a", 65534))
	if err != nil || len(raw) != 65536 {
		t.Fatal("exact boundary failed", err)
	}
}
