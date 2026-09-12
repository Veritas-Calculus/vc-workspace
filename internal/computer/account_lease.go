package computer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/guestidentity"
	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
)

type AccountIdentity struct {
	Username string `json:"username"`
	UID      uint32 `json:"uid"`
	SID      string `json:"sid"`
}

// AccountLease is the Guest's durable account mutation fence, NOT a
// Helper/input grant or an owned RDP process. Secrets never appear in receipts.
type AccountLease struct {
	SchemaVersion      int             `json:"schema_version"`
	Identity           AccountIdentity `json:"identity"`
	LeaseID            string          `json:"lease_id"`
	ControlEpoch       int64           `json:"control_epoch"`
	LoginGeneration    int64           `json:"login_generation"`
	ExpiresUnixSeconds int64           `json:"expires_unix_seconds"`
	Phase              string          `json:"phase"`
}

// Platform entry points share one wire envelope; OS identities remain distinct.
type WindowsAccountIdentity = AccountIdentity
type WindowsAccountLease = AccountLease

func (lease AccountLease) validFor(platform string) bool {
	if lease.SchemaVersion != 1 || lease.LoginGeneration < 1 || !validAgentUser(lease.Identity.Username) || !guestidentity.ValidAccount(platform, lease.Identity.UID, lease.Identity.SID) ||
		!validAuthority(Authority{SchemaVersion: 1, LeaseID: lease.LeaseID, ControlEpoch: lease.ControlEpoch, State: "revoked"}) || len(lease.LeaseID) < 7 {
		return false
	}
	switch lease.Phase {
	case "revoked":
		return lease.ExpiresUnixSeconds == 0
	case "opening", "open", "sealing", "sealed":
		return lease.ExpiresUnixSeconds > 0 && lease.ExpiresUnixSeconds < 0xffffffff
	default:
		return false
	}
}

func parseWindowsAccountLease(raw []byte) (WindowsAccountLease, error) {
	return parseAccountLease(raw, "windows")
}

func parseAccountLease(raw []byte, platform string) (AccountLease, error) {
	var lease AccountLease
	if len(raw) == 0 || len(raw) > 4096 {
		return lease, ErrUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&lease) != nil || decoder.Decode(new(any)) != io.EOF || !lease.validFor(platform) {
		return AccountLease{}, fmt.Errorf("%w: account lifecycle receipt", ErrUnavailable)
	}
	return lease, nil
}

// ChangeWindowsAccountLease sends exactly one mutation with an immutable full
// SID/lease/epoch. QGA failure is uncertain: do not retry, replace the request,
// fall back to username-only commands, or acknowledge from a missing receipt.
// The caller owns password storage; this method wipes its serialized copy.
func (e *WindowsSessionExecutor) ChangeWindowsAccountLease(ctx context.Context, machine pve.VM, desired WindowsAccountLease, operation string, password []byte) (WindowsAccountLease, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	phase := map[string]string{"open": "open", "seal": "sealed", "revoke": "revoked"}[operation]
	if phase == "" || desired.Phase != "" {
		return WindowsAccountLease{}, ErrInvalid
	}
	desired.Phase = phase
	if !desired.validFor("windows") {
		return WindowsAccountLease{}, ErrInvalid
	}
	if operation == "open" {
		if len(password) < 24 || len(password) > 256 {
			return WindowsAccountLease{}, ErrInvalid
		}
		for _, b := range password {
			if b < 33 || b > 126 {
				return WindowsAccountLease{}, ErrInvalid
			}
		}
	} else if len(password) != 0 {
		return WindowsAccountLease{}, ErrInvalid
	}
	if operation != "revoke" && (desired.ExpiresUnixSeconds <= time.Now().Unix() || desired.ExpiresUnixSeconds > time.Now().Unix()+28800) {
		return WindowsAccountLease{}, ErrInvalid
	}
	payload, err := pve.MarshalGuestJSON(struct {
		SchemaVersion      int                    `json:"schema_version"`
		Identity           WindowsAccountIdentity `json:"identity"`
		LeaseID            string                 `json:"lease_id"`
		ControlEpoch       int64                  `json:"control_epoch"`
		LoginGeneration    int64                  `json:"login_generation"`
		ExpiresUnixSeconds int64                  `json:"expires_unix_seconds"`
		Operation          string                 `json:"operation"`
		Password           string                 `json:"password,omitempty"`
	}{1, desired.Identity, desired.LeaseID, desired.ControlEpoch, desired.LoginGeneration, desired.ExpiresUnixSeconds, operation, string(password)})
	if err != nil || len(payload) > 4096 {
		return WindowsAccountLease{}, ErrInvalid
	}
	defer clear(payload)
	if err := e.probe(ctx, machine); err != nil {
		return WindowsAccountLease{}, err
	}
	result, err := e.command(ctx, machine, "account-lease", payload, "--guest-user", desired.Identity.Username)
	if err != nil {
		return WindowsAccountLease{}, err
	}
	receipt, err := parseWindowsAccountLease([]byte(result.Stdout))
	if err != nil || receipt != desired {
		return WindowsAccountLease{}, fmt.Errorf("%w: Windows account lifecycle did not confirm exact intent", ErrUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return WindowsAccountLease{}, err
	}
	return receipt, nil
}
