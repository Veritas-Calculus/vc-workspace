package httpapi

import (
	"errors"

	"github.com/Veritas-Calculus/vc-workspace/internal/store"
)

// A validation clone is an explicit administrator action, not a shortcut for
// promoting an untested image. Its job remains an ordinary, auditable clone;
// successful cloning never changes the source image's testing state.
func validateCloneImage(request desktopCloneRequest, profile store.ImageProfile, registered bool) error {
	if request.ValidationImageProfileID != "" {
		if !registered || profile.ID != request.ValidationImageProfileID || profile.TemplateVMID != request.SourceVMID {
			return errors.New("Validation image does not match the source template")
		}
		if !profile.Enabled || profile.BuildStatus != "testing" {
			return errors.New("Validation requires an enabled image awaiting testing")
		}
		if !request.Full {
			return errors.New("Validation requires a full clone")
		}
		return nil
	}
	// Templates not registered as platform images retain the existing admin
	// import path. Registered candidates must not bypass the image lifecycle.
	if registered && (!profile.Enabled || profile.BuildStatus != "ready") {
		return errors.New("Source image is not ready; create an explicit validation clone first")
	}
	return nil
}
