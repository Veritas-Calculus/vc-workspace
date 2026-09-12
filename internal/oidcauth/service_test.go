package oidcauth

import (
	"strings"
	"testing"
)

func TestNormalizeGroupsTrimsDeduplicatesAndPreservesOrder(t *testing.T) {
	groups, err := normalizeGroups([]string{" engineering ", "", "operators", "engineering"})
	if err != nil {
		t.Fatalf("normalize groups: %v", err)
	}
	if len(groups) != 2 || groups[0] != "engineering" || groups[1] != "operators" {
		t.Fatalf("unexpected groups: %#v", groups)
	}
}

func TestNormalizeGroupsRejectsUnboundedClaims(t *testing.T) {
	tooMany := make([]string, 257)
	for index := range tooMany {
		tooMany[index] = "group"
	}
	if _, err := normalizeGroups(tooMany); err == nil {
		t.Fatal("expected excessive group count to fail")
	}
	if _, err := normalizeGroups([]string{strings.Repeat("g", 513)}); err == nil {
		t.Fatal("expected excessive group length to fail")
	}
}
