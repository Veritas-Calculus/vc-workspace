package pve

import "testing"

func TestPrincipalNeverReturnsCredentialMaterial(t *testing.T) {
	for _, tc := range []struct {
		client *Client
		want   string
	}{
		{&Client{username: "user@pve", password: "password-secret", ticket: "ticket-secret"}, "user@pve"},
		{&Client{username: "unused@pve", tokenID: "user@pve!builder", tokenSecret: "token-secret"}, "user@pve!builder"},
	} {
		if got := tc.client.Principal(); got != tc.want {
			t.Fatalf("incorrect principal: %q", got)
		}
	}
}
