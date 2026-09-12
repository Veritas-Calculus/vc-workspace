// Package linuxguest contains the canonical systemd units shipped in Linux
// templates. Acceptance uses these exact bytes, not a second test-only unit.
package linuxguest

import "embed"

//go:embed *.service *.timer
var Units embed.FS

//go:embed install-login-fence.py
var LoginFenceInstaller string
