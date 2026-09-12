package httpapi

import (
	"testing"

	"github.com/Veritas-Calculus/vc-workspace/internal/store"
)

func TestCloneImageLifecycle(t *testing.T) {
	for _, state := range []string{"draft", "blocked", "building", "testing", "ready", "failed", "unknown"} {
		for _, enabled := range []bool{false, true} {
			for _, validation := range []bool{false, true} {
				p := store.ImageProfile{ID: "image", TemplateVMID: 9202, BuildStatus: state, Enabled: enabled}
				r := desktopCloneRequest{SourceVMID: 9202, Full: true}
				if validation {
					r.ValidationImageProfileID = p.ID
				}
				want := enabled && ((!validation && state == "ready") || (validation && state == "testing"))
				if got := validateCloneImage(r, p, true) == nil; got != want {
					t.Errorf("state=%s enabled=%v validation=%v: allowed=%v want=%v", state, enabled, validation, got, want)
				}
			}
		}
	}
	p := store.ImageProfile{ID: "image", TemplateVMID: 9202, BuildStatus: "testing", Enabled: true}
	r := desktopCloneRequest{SourceVMID: 9202, Full: true, ValidationImageProfileID: "image"}
	for _, mutate := range []func(*desktopCloneRequest){
		func(r *desktopCloneRequest) { r.Full = false },
		func(r *desktopCloneRequest) { r.SourceVMID++ },
		func(r *desktopCloneRequest) { r.ValidationImageProfileID = "other" },
	} {
		bad := r
		mutate(&bad)
		if validateCloneImage(bad, p, true) == nil {
			t.Fatal("invalid validation accepted")
		}
	}
	if validateCloneImage(r, p, false) == nil {
		t.Fatal("unregistered validation accepted")
	}
	if validateCloneImage(desktopCloneRequest{}, store.ImageProfile{}, false) != nil {
		t.Fatal("legacy admin template import rejected")
	}
}
