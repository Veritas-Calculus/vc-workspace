package computer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
)

type windowsLeaseGuest struct {
	*windowsExecutorGuest
	reply func(map[string]any) (pve.GuestExecResult, error)
}

func (g *windowsLeaseGuest) ExecGuestWithInput(_ context.Context, _ string, _ int, args []string, input []byte) (pve.GuestExecResult, error) {
	g.commands = append(g.commands, slices.Clone(args))
	g.input = append(g.input, slices.Clone(input))
	if !slices.Equal(args, []string{windowsSessionBinary, "computer-v2-account-lease", "--guest-user", g.target.Username}) {
		return pve.GuestExecResult{}, errors.New("unexpected account mutation")
	}
	var request map[string]any
	if json.Unmarshal(input, &request) != nil {
		return pve.GuestExecResult{}, errors.New("invalid account payload")
	}
	return g.reply(request)
}

func leaseTestIntent() WindowsAccountLease {
	return WindowsAccountLease{SchemaVersion: 1, Identity: WindowsAccountIdentity{Username: "vca0123456789ab", SID: "S-1-5-21-1-2-3-1001"}, LeaseID: "lease_exact", ControlEpoch: 10, LoginGeneration: 1, ExpiresUnixSeconds: time.Now().Add(time.Hour).Unix()}
}

func leaseTestReceipt(request map[string]any) map[string]any {
	delete(request, "password")
	operation := request["operation"].(string)
	delete(request, "operation")
	request["phase"] = map[string]string{"open": "open", "seal": "sealed", "revoke": "revoked"}[operation]
	return request
}

func TestWindowsAccountLeaseSendsOneBoundMutationWithNoLegacyFallback(t *testing.T) {
	for _, operation := range []string{"open", "seal", "revoke"} {
		t.Run(operation, func(t *testing.T) {
			g := &windowsLeaseGuest{windowsExecutorGuest: newWindowsExecutorGuest()}
			g.reply = func(request map[string]any) (pve.GuestExecResult, error) {
				raw, _ := json.Marshal(leaseTestReceipt(request))
				return pve.GuestExecResult{Stdout: string(raw)}, nil
			}
			desired := leaseTestIntent()
			var password []byte
			if operation == "open" {
				password = []byte("Vcw1!private-sentinel-not-real-secret")
			}
			if operation == "revoke" {
				desired.ExpiresUnixSeconds = 0
			}
			receipt, err := NewWindowsSessionExecutor(g).ChangeWindowsAccountLease(t.Context(), pve.VM{}, desired, operation, password)
			if err != nil || len(g.input) != 1 || receipt.Identity != desired.Identity || receipt.ControlEpoch != 10 {
				t.Fatal("bound mutation failed", err)
			}
			if len(password) > 0 && !bytes.Equal(password, []byte("Vcw1!private-sentinel-not-real-secret")) {
				t.Fatal("caller-owned secret changed")
			}
			for _, cmd := range g.commands {
				if len(cmd) > 1 && strings.Contains(cmd[1], "account-") && cmd[1] != "computer-v2-account-lease" {
					t.Fatal("legacy fallback")
				}
			}
			if len(g.writtenPaths) != 0 {
				t.Fatal("credential spooled to Guest file")
			}
		})
	}
}

func TestWindowsAccountLeaseLostOrWrongReceiptsAreNeverReplayedOrAcknowledged(t *testing.T) {
	for _, mode := range []string{"lost", "denied", "empty", "old-epoch", "other-generation", "other-sid", "other-lease", "pending", "secret-field", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			g := &windowsLeaseGuest{windowsExecutorGuest: newWindowsExecutorGuest()}
			g.reply = func(request map[string]any) (pve.GuestExecResult, error) {
				value := leaseTestReceipt(request)
				switch mode {
				case "lost":
					return pve.GuestExecResult{}, errors.New("PVE returned 500: Agent error: PID 123 does not exist private-sentinel")
				case "denied":
					return pve.GuestExecResult{ExitCode: 1, Stderr: "private-sentinel"}, nil
				case "empty":
					return pve.GuestExecResult{}, nil
				case "old-epoch":
					value["control_epoch"] = 9
				case "other-generation":
					value["login_generation"] = 2
				case "other-sid":
					value["identity"].(map[string]any)["sid"] = "S-1-5-21-1-2-3-1002"
				case "other-lease":
					value["lease_id"] = "lease_other"
				case "pending":
					value["phase"] = "opening"
				case "secret-field":
					value["password"] = "private-sentinel"
				case "cancel":
					cancel()
				}
				raw, _ := json.Marshal(value)
				return pve.GuestExecResult{Stdout: string(raw)}, nil
			}
			_, err := NewWindowsSessionExecutor(g).ChangeWindowsAccountLease(ctx, pve.VM{}, leaseTestIntent(), "open", []byte("Vcw1!private-sentinel-not-real-secret"))
			if err == nil || strings.Contains(err.Error(), "private-sentinel") || len(g.input) != 1 {
				t.Fatal("uncertain lifecycle replayed/acknowledged or exposed", err, len(g.input))
			}
		})
	}
}

func TestWindowsAccountLeaseRejectsUnboundIdentityUnboundedExpiryAndSecretsBeforeGuest(t *testing.T) {
	for _, mode := range []string{"user", "sid", "uid", "epoch", "generation", "lease", "forever", "past", "phase", "operation", "password", "seal-password", "revoke-expiry"} {
		t.Run(mode, func(t *testing.T) {
			intent := leaseTestIntent()
			operation := "open"
			password := []byte("Vcw1!private-sentinel-not-real-secret")
			switch mode {
			case "user":
				intent.Identity.Username = "vdi"
			case "sid":
				intent.Identity.SID = ""
			case "uid":
				intent.Identity.UID = 1000
			case "epoch":
				intent.ControlEpoch = 0
			case "generation":
				intent.LoginGeneration = 0
			case "lease":
				intent.LeaseID = "lease_"
			case "forever":
				intent.ExpiresUnixSeconds = 0xffffffff
			case "past":
				intent.ExpiresUnixSeconds = 1
			case "phase":
				intent.Phase = "open"
			case "operation":
				operation = "enable"
			case "password":
				password = []byte("short")
			case "seal-password":
				operation = "seal"
			case "revoke-expiry":
				operation = "revoke"
				password = nil
			}
			g := newWindowsExecutorGuest()
			_, err := NewWindowsSessionExecutor(g).ChangeWindowsAccountLease(t.Context(), pve.VM{}, intent, operation, password)
			if !errors.Is(err, ErrInvalid) || len(g.commands) != 0 {
				t.Fatal("invalid account intent reached Guest", err)
			}
		})
	}
}

func TestWindowsAccountObservationKeepsPendingFencesAndRejectsSubstitutions(t *testing.T) {
	for _, phase := range []string{"opening", "open", "sealing", "sealed", "revoked"} {
		value := windowsObservationFixture()
		lease := leaseTestIntent()
		lease.Phase = phase
		if phase == "revoked" {
			lease.ExpiresUnixSeconds = 0
		}
		value["lifecycle"] = lease
		raw, _ := json.Marshal(value)
		state, err := parseWindowsAccountObservation(string(raw), lease.Identity.Username, lease.Identity.SID)
		if err != nil || state.Lifecycle == nil || state.LoginStopped() != (phase == "revoked") {
			t.Fatal("pending lifecycle became cleanup acknowledgement", phase, err)
		}
	}
	for _, change := range []func(*WindowsAccountLease){func(l *WindowsAccountLease) { l.ControlEpoch = 0 }, func(l *WindowsAccountLease) { l.Identity.SID = "S-1-5-21-1-2-3-1002" }, func(l *WindowsAccountLease) { l.Phase = "unknown" }} {
		value := windowsObservationFixture()
		lease := leaseTestIntent()
		lease.Phase = "open"
		change(&lease)
		value["lifecycle"] = lease
		raw, _ := json.Marshal(value)
		state, err := parseWindowsAccountObservation(string(raw), "vca0123456789ab", "S-1-5-21-1-2-3-1001")
		if err == nil || state.LoginStopped() {
			t.Fatal("invalid lifecycle became absence")
		}
	}
}
