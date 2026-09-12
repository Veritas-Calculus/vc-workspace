package computer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Veritas-Calculus/vc-workspace/internal/guestidentity"
	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
)

// WindowsAccountObservation is a point-in-time SAM/WTS observation bound to a
// previously recorded full SID, not an authorization or a transport owner. A
// missing SAM account may still own a retained logon. No Helper is needed to
// observe locked, disconnected or partially torn-down sessions.
type WindowsAccountObservation struct {
	observed           bool
	Username           string
	SID                string
	Exists             bool
	Disabled           bool
	ExpiresUnixSeconds uint32
	Sessions           []string
	Lifecycle          *WindowsAccountLease
}

// LoginStopped proves this observation has neither an enabled SAM account nor
// a retained WTS logon. The broker must still fence grants/in-flight login and
// stop its owned RDP transport; this is not a durable revocation acknowledgement.
func (state WindowsAccountObservation) LoginStopped() bool {
	return state.observed && (!state.Exists || state.Disabled) && len(state.Sessions) == 0 && (state.Lifecycle == nil || state.Lifecycle.Phase == "revoked")
}

func parseWindowsAccountObservation(raw string, username, sid string) (WindowsAccountObservation, error) {
	failure := func() (WindowsAccountObservation, error) {
		return WindowsAccountObservation{}, fmt.Errorf("%w: Windows SAM/WTS observation receipt", ErrUnavailable)
	}
	if !validAgentUser(username) || !validWindowsAccountSID(sid) || len(raw) == 0 || len(raw) > 65536 {
		return failure()
	}
	var receipt struct {
		SchemaVersion      int             `json:"schema_version"`
		ObservationVersion int             `json:"observation_version"`
		Username           string          `json:"username"`
		SID                string          `json:"sid"`
		Exists             *bool           `json:"exists"`
		Disabled           json.RawMessage `json:"disabled"`
		ExpiresUnixSeconds json.RawMessage `json:"expires_unix_seconds"`
		Sessions           []string        `json:"sessions"`
		Lifecycle          json.RawMessage `json:"lifecycle"`
	}
	if json.Unmarshal([]byte(raw), &receipt) != nil || receipt.SchemaVersion != 1 || receipt.ObservationVersion != 1 ||
		receipt.Username != username || receipt.SID != sid || receipt.Exists == nil ||
		len(receipt.Disabled) == 0 || len(receipt.ExpiresUnixSeconds) == 0 || receipt.Sessions == nil || len(receipt.Sessions) > 1024 {
		return failure()
	}
	var disabled *bool
	var expiry *uint32
	if json.Unmarshal(receipt.Disabled, &disabled) != nil || json.Unmarshal(receipt.ExpiresUnixSeconds, &expiry) != nil ||
		(disabled != nil) != *receipt.Exists || (expiry != nil) != *receipt.Exists {
		return failure()
	}
	seen := map[string]bool{}
	for _, session := range receipt.Sessions {
		if !guestidentity.ValidWindowsLogonBinding(session) {
			return failure()
		}
		// One WTS enumeration cannot contain two incarnations of the same
		// numeric session. Reject duplicates instead of silently deduplicating.
		id := strings.Split(session, ":")[1]
		if seen[id] {
			return failure()
		}
		seen[id] = true
	}
	state := WindowsAccountObservation{observed: true, Username: username, SID: sid, Exists: *receipt.Exists, Sessions: receipt.Sessions}
	if len(receipt.Lifecycle) != 0 && string(receipt.Lifecycle) != "null" {
		lease, err := parseWindowsAccountLease(receipt.Lifecycle)
		if err != nil || lease.Identity.Username != username || lease.Identity.SID != sid {
			return failure()
		}
		state.Lifecycle = &lease
	}
	if state.Exists {
		state.Disabled, state.ExpiresUnixSeconds = *disabled, *expiry
	}
	return state, nil
}

// ObserveWindowsAgentAccount never provisions, enables, disables or repairs an
// account/journal. Missing or old-format receipts and transport failures remain
// errors; only explicit journal-bound JSON can report SAM/WTS absence.
func (e *WindowsSessionExecutor) ObserveWindowsAgentAccount(ctx context.Context, machine pve.VM, username, expectedSID string) (WindowsAccountObservation, error) {
	if !validAgentUser(username) || !validWindowsAccountSID(expectedSID) {
		return WindowsAccountObservation{}, ErrInvalid
	}
	if err := e.probe(ctx, machine); err != nil {
		return WindowsAccountObservation{}, err
	}
	result, err := e.command(ctx, machine, "account-inspect", nil, "--guest-user", username)
	if err != nil {
		return WindowsAccountObservation{}, err
	}
	return parseWindowsAccountObservation(result.Stdout, username, expectedSID)
}
