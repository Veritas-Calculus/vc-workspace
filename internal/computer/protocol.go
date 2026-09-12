package computer

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const SchemaVersion = 1

var (
	ErrInvalid     = errors.New("invalid computer request")
	ErrUnavailable = errors.New("computer helper unavailable")
	ErrTimeout     = errors.New("computer action timed out")
	requestIDRE    = regexp.MustCompile(`^action_[A-Za-z0-9_-]{20,80}$`)
)

type Operation string

const (
	OperationScreenshot    Operation = "screenshot"
	OperationAccessibility Operation = "accessibility_snapshot"
	OperationMouse         Operation = "mouse"
	OperationKey           Operation = "key"
	OperationTypeText      Operation = "type_text"
)

type Request struct {
	SchemaVersion int            `json:"schema_version"`
	RequestID     string         `json:"request_id"`
	LeaseID       string         `json:"lease_id"`
	ControlEpoch  int64          `json:"control_epoch"`
	ExpiresUnixMS int64          `json:"expires_unix_ms"`
	Operation     Operation      `json:"operation"`
	Screenshot    *Screenshot    `json:"screenshot,omitempty"`
	Accessibility *Accessibility `json:"accessibility,omitempty"`
	Mouse         *Mouse         `json:"mouse,omitempty"`
	Key           *Key           `json:"key,omitempty"`
	Text          *Text          `json:"text,omitempty"`
}

type Screenshot struct {
	MaxWidth int `json:"max_width,omitempty"`
}

type Accessibility struct {
	MaxDepth int `json:"max_depth,omitempty"`
	MaxNodes int `json:"max_nodes,omitempty"`
}

type Mouse struct {
	Action string `json:"action"`
	X      int    `json:"x,omitempty"`
	Y      int    `json:"y,omitempty"`
	Button string `json:"button,omitempty"`
	DeltaX int    `json:"delta_x,omitempty"`
	DeltaY int    `json:"delta_y,omitempty"`
}

type Key struct {
	Key       string   `json:"key"`
	Modifiers []string `json:"modifiers,omitempty"`
}

type Text struct {
	Value     string `json:"value"`
	Sensitive bool   `json:"sensitive,omitempty"`
}

type Response struct {
	SchemaVersion int                        `json:"schema_version"`
	RequestID     string                     `json:"request_id"`
	OK            bool                       `json:"ok"`
	Error         string                     `json:"error,omitempty"`
	Screenshot    *ScreenshotResult          `json:"screenshot,omitempty"`
	Accessibility *AccessibilityResult       `json:"accessibility,omitempty"`
	Input         *InputResult               `json:"input,omitempty"`
	Metadata      map[string]json.RawMessage `json:"metadata,omitempty"`
}

type ScreenshotResult struct {
	ContentType   string         `json:"content_type"`
	Data          string         `json:"data"`
	Width         int            `json:"width"`
	Height        int            `json:"height"`
	SHA256        string         `json:"sha256"`
	DesktopBounds *DesktopBounds `json:"desktop_bounds,omitempty"`
}

// DesktopBounds locates the captured monitor in native desktop input coordinates,
// before the screenshot is resized. It is absent on older Guest agents.
type DesktopBounds struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type AccessibilityResult struct {
	Source    string              `json:"source"`
	Truncated bool                `json:"truncated"`
	Nodes     []AccessibilityNode `json:"nodes"`
}

type AccessibilityNode struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id,omitempty"`
	Role     string `json:"role"`
	Name     string `json:"name,omitempty"`
	Value    string `json:"value,omitempty"`
	X        int    `json:"x,omitempty"`
	Y        int    `json:"y,omitempty"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	Focused  bool   `json:"focused,omitempty"`
	Enabled  bool   `json:"enabled"`
}

type InputResult struct {
	Applied bool `json:"applied"`
}

type Authority struct {
	SchemaVersion int    `json:"schema_version"`
	LeaseID       string `json:"lease_id"`
	ControlEpoch  int64  `json:"control_epoch"`
	State         string `json:"state"`
	ExpiresUnixMS int64  `json:"expires_unix_ms"`
}

func (r *Request) ApplyDefaults() {
	if r.SchemaVersion == 0 {
		r.SchemaVersion = SchemaVersion
	}
	if r.Screenshot != nil && r.Screenshot.MaxWidth == 0 {
		r.Screenshot.MaxWidth = 1600
	}
	if r.Accessibility != nil {
		if r.Accessibility.MaxDepth == 0 {
			r.Accessibility.MaxDepth = 6
		}
		if r.Accessibility.MaxNodes == 0 {
			r.Accessibility.MaxNodes = 300
		}
	}
}

func (r Request) Validate(now time.Time) error {
	if r.SchemaVersion != SchemaVersion || !requestIDRE.MatchString(r.RequestID) ||
		!strings.HasPrefix(r.LeaseID, "lease_") || r.ControlEpoch < 1 {
		return ErrInvalid
	}
	expiresAt := time.UnixMilli(r.ExpiresUnixMS)
	if r.ExpiresUnixMS <= 0 || !expiresAt.After(now) || expiresAt.After(now.Add(20*time.Second)) {
		return fmt.Errorf("%w: expiry is outside the allowed action window", ErrInvalid)
	}
	payloads := 0
	for _, present := range []bool{r.Screenshot != nil, r.Accessibility != nil, r.Mouse != nil, r.Key != nil, r.Text != nil} {
		if present {
			payloads++
		}
	}
	if payloads != 1 {
		return fmt.Errorf("%w: exactly one operation payload is required", ErrInvalid)
	}
	switch r.Operation {
	case OperationScreenshot:
		if r.Screenshot == nil || r.Screenshot.MaxWidth < 320 || r.Screenshot.MaxWidth > 3840 {
			return fmt.Errorf("%w: screenshot max_width must be 320-3840", ErrInvalid)
		}
	case OperationAccessibility:
		if r.Accessibility == nil || r.Accessibility.MaxDepth < 1 || r.Accessibility.MaxDepth > 10 || r.Accessibility.MaxNodes < 1 || r.Accessibility.MaxNodes > 500 {
			return fmt.Errorf("%w: accessibility bounds are invalid", ErrInvalid)
		}
	case OperationMouse:
		if err := validateMouse(r.Mouse); err != nil {
			return err
		}
	case OperationKey:
		if err := validateKey(r.Key); err != nil {
			return err
		}
	case OperationTypeText:
		if r.Text == nil || r.Text.Value == "" || len([]byte(r.Text.Value)) > 4096 || strings.ContainsRune(r.Text.Value, '\x00') {
			return fmt.Errorf("%w: text must contain 1-4096 UTF-8 bytes and no NUL", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: unsupported operation", ErrInvalid)
	}
	return nil
}

func validateMouse(input *Mouse) error {
	if input == nil {
		return fmt.Errorf("%w: mouse payload is required", ErrInvalid)
	}
	switch input.Action {
	case "move":
		if input.X < 0 || input.X > 16384 || input.Y < 0 || input.Y > 16384 {
			return fmt.Errorf("%w: mouse coordinates are outside the allowed range", ErrInvalid)
		}
	case "click":
		if input.X < 0 || input.X > 16384 || input.Y < 0 || input.Y > 16384 || !oneOf(input.Button, "left", "right", "middle") {
			return fmt.Errorf("%w: mouse click is invalid", ErrInvalid)
		}
	case "scroll":
		if input.DeltaX < -100 || input.DeltaX > 100 || input.DeltaY < -100 || input.DeltaY > 100 || (input.DeltaX == 0 && input.DeltaY == 0) {
			return fmt.Errorf("%w: mouse scroll delta is invalid", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: unsupported mouse action", ErrInvalid)
	}
	return nil
}

func validateKey(input *Key) error {
	if input == nil {
		return fmt.Errorf("%w: unsupported key", ErrInvalid)
	}
	normalizedKey := strings.ToLower(input.Key)
	namedKey := oneOf(normalizedKey, "enter", "escape", "tab", "backspace", "delete", "space", "up", "down", "left", "right", "home", "end", "page_up", "page_down", "f1", "f2", "f3", "f4", "f5", "f6", "f7", "f8", "f9", "f10", "f11", "f12")
	shortcutKey := len(normalizedKey) == 1 && ((normalizedKey[0] >= 'a' && normalizedKey[0] <= 'z') || (normalizedKey[0] >= '0' && normalizedKey[0] <= '9')) && len(input.Modifiers) > 0
	if !namedKey && !shortcutKey {
		return fmt.Errorf("%w: unsupported key", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, modifier := range input.Modifiers {
		modifier = strings.ToLower(modifier)
		if !oneOf(modifier, "control", "alt", "shift", "meta") || seen[modifier] {
			return fmt.Errorf("%w: unsupported or duplicate key modifier", ErrInvalid)
		}
		seen[modifier] = true
	}
	return nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func (r Request) AuditDetail() map[string]any {
	detail := map[string]any{"operation": r.Operation, "control_epoch": r.ControlEpoch}
	switch r.Operation {
	case OperationScreenshot:
		detail["max_width"] = r.Screenshot.MaxWidth
	case OperationAccessibility:
		detail["max_depth"] = r.Accessibility.MaxDepth
		detail["max_nodes"] = r.Accessibility.MaxNodes
	case OperationMouse:
		detail["action"] = r.Mouse.Action
		switch r.Mouse.Action {
		case "move", "click":
			detail["x"] = r.Mouse.X
			detail["y"] = r.Mouse.Y
		}
		if r.Mouse.Action == "click" {
			detail["button"] = r.Mouse.Button
		}
		if r.Mouse.Action == "scroll" {
			detail["delta_x"] = r.Mouse.DeltaX
			detail["delta_y"] = r.Mouse.DeltaY
		}
	case OperationKey:
		detail["key"] = strings.ToLower(r.Key.Key)
		modifiers := make([]string, len(r.Key.Modifiers))
		for index, modifier := range r.Key.Modifiers {
			modifiers[index] = strings.ToLower(modifier)
		}
		detail["modifiers"] = modifiers
	case OperationTypeText:
		detail["text_bytes"] = len([]byte(r.Text.Value))
		detail["sensitive"] = r.Text.Sensitive
	}
	return detail
}
