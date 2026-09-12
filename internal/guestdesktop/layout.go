// Package guestdesktop owns the managed Linux session's display integration.
package guestdesktop

import (
	_ "embed"
	"encoding/base64"
	"fmt"
)

//go:embed session_layout.py
var layout []byte

// InstallScript is run as root, without executing any code from a user's Home.
// The helper itself runs unprivileged via XFCE autostart. Old templates without
// its optional runtime dependencies still retain the existing DPI fallback.
func InstallScript() string {
	return fmt.Sprintf(`install -d -m 0755 /usr/local/lib/vc-workspace /etc/xdg/autostart /var/lib/vc-workspace/display-scale
layout_tmp=$(mktemp /usr/local/lib/vc-workspace/.session-layout.XXXXXX)
printf '%%s' '%s' | base64 -d >"$layout_tmp"
chmod 0644 "$layout_tmp"
mv -f "$layout_tmp" /usr/local/lib/vc-workspace/session_layout.py
printf '%%s\n' '[Desktop Entry]' 'Type=Application' 'Name=VC Workspace Display' 'Exec=/usr/bin/python3 /usr/local/lib/vc-workspace/session_layout.py' 'OnlyShowIn=XFCE;' 'NoDisplay=true' > /etc/xdg/autostart/vc-workspace-display.desktop
chmod 0644 /etc/xdg/autostart/vc-workspace-display.desktop
`, base64.StdEncoding.EncodeToString(layout))
}
