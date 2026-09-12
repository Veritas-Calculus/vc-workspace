// Package brand exposes small, immutable brand assets required for Guest
// policy convergence. The SVG remains the design source; the embedded JPEG is
// a bounded transport derivative for PVE's QEMU Guest Agent request limit.
package brand

import _ "embed"

// DesktopBackgroundSessionJPEG is a 1920x1080 fallback installed when an
// older Guest predates the template-bundled 4K asset.
//
//go:embed vc-workspace-desktop-background-session.jpg
var DesktopBackgroundSessionJPEG []byte
