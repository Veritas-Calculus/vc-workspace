package store

import "testing"

func TestSanitizeAuditValueRedactsSecretsAndBoundsUntrustedValues(t *testing.T) {
	clean := sanitizeAuditValue(map[string]any{
		"username": "alice",
		"password": "not-for-logs",
		"nested":   map[string]any{"access_token": "also-secret"},
		"error":    string(make([]byte, 1100)),
	}, 0).(map[string]any)
	if clean["password"] != "[REDACTED]" {
		t.Fatalf("password was not redacted: %#v", clean)
	}
	if clean["nested"].(map[string]any)["access_token"] != "[REDACTED]" {
		t.Fatalf("nested token was not redacted: %#v", clean)
	}
	if len(clean["error"].(string)) > 1027 {
		t.Fatalf("untrusted audit string was not bounded: %d", len(clean["error"].(string)))
	}
	if clean["username"] != "alice" {
		t.Fatalf("safe value changed: %#v", clean)
	}
}

func TestAuditOutcomeSupportsDotAndUnderscoreEventNames(t *testing.T) {
	for _, eventType := range []string{"job.failed", "auth.native_login_failed", "request.denied"} {
		if actual := auditOutcome(eventType); actual != "failure" {
			t.Errorf("auditOutcome(%q)=%q, want failure", eventType, actual)
		}
	}
	if actual := auditOutcome("auth.login_succeeded"); actual != "success" {
		t.Fatalf("auditOutcome(success)=%q", actual)
	}
}
