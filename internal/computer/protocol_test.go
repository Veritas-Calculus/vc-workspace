package computer

import (
	"testing"
	"time"
)

func validRequest(now time.Time) Request {
	return Request{
		SchemaVersion: SchemaVersion,
		RequestID:     "action_abcdefghijklmnopqrstuvwxyz",
		LeaseID:       "lease_abcdefghijklmnopqrstuvwxyz",
		ControlEpoch:  1,
		ExpiresUnixMS: now.Add(5 * time.Second).UnixMilli(),
		Operation:     OperationScreenshot,
		Screenshot:    &Screenshot{MaxWidth: 1600},
	}
}

func TestRequestValidationEnforcesOneBoundedPayload(t *testing.T) {
	now := time.Now()
	request := validRequest(now)
	if err := request.Validate(now); err != nil {
		t.Fatal(err)
	}
	request.Text = &Text{Value: "secret", Sensitive: true}
	if err := request.Validate(now); err == nil {
		t.Fatal("expected multiple payloads to be rejected")
	}
	request = validRequest(now)
	request.ExpiresUnixMS = now.Add(21 * time.Second).UnixMilli()
	if err := request.Validate(now); err == nil {
		t.Fatal("expected an unbounded expiry to be rejected")
	}
}

func TestTextAuditNeverContainsText(t *testing.T) {
	now := time.Now()
	request := validRequest(now)
	request.Operation = OperationTypeText
	request.Screenshot = nil
	request.Text = &Text{Value: "do-not-log-this-password", Sensitive: true}
	if err := request.Validate(now); err != nil {
		t.Fatal(err)
	}
	detail := request.AuditDetail()
	if detail["text_bytes"] != len([]byte(request.Text.Value)) || detail["sensitive"] != true {
		t.Fatalf("unexpected audit metadata: %#v", detail)
	}
	for _, value := range detail {
		if value == request.Text.Value {
			t.Fatal("audit metadata leaked typed text")
		}
	}
}

func TestKeyAndMouseInputsAreAllowlisted(t *testing.T) {
	if err := validateKey(&Key{Key: "Enter", Modifiers: []string{"control", "shift"}}); err != nil {
		t.Fatal(err)
	}
	if err := validateKey(&Key{Key: "a"}); err == nil {
		t.Fatal("printable text must use the text operation")
	}
	if err := validateKey(&Key{Key: "l", Modifiers: []string{"control"}}); err != nil {
		t.Fatalf("expected a modified ASCII shortcut key to be accepted: %v", err)
	}
	if err := validateMouse(&Mouse{Action: "click", X: 100, Y: 200, Button: "left"}); err != nil {
		t.Fatal(err)
	}
	if err := validateMouse(&Mouse{Action: "scroll", DeltaY: 101}); err == nil {
		t.Fatal("expected oversized scroll to be rejected")
	}
}

func TestInputAuditRetainsActionMetadata(t *testing.T) {
	mouse := validRequest(time.Now())
	mouse.Operation = OperationMouse
	mouse.Screenshot = nil
	mouse.Mouse = &Mouse{Action: "click", X: 123, Y: 456, Button: "left"}
	mouseDetail := mouse.AuditDetail()
	if mouseDetail["action"] != "click" || mouseDetail["x"] != 123 || mouseDetail["y"] != 456 || mouseDetail["button"] != "left" {
		t.Fatalf("unexpected mouse audit metadata: %#v", mouseDetail)
	}

	key := validRequest(time.Now())
	key.Operation = OperationKey
	key.Screenshot = nil
	key.Key = &Key{Key: "L", Modifiers: []string{"Control", "Shift"}}
	keyDetail := key.AuditDetail()
	modifiers, ok := keyDetail["modifiers"].([]string)
	if keyDetail["key"] != "l" || !ok || len(modifiers) != 2 || modifiers[0] != "control" || modifiers[1] != "shift" {
		t.Fatalf("unexpected key audit metadata: %#v", keyDetail)
	}
}
