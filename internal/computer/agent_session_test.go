package computer

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
)

type agentLoginGuest struct {
	*sessionGuest
	ready     bool
	lease     *AccountLease
	inputs    [][]byte
	loginArgs []string
	failure   string
}

func (g *agentLoginGuest) ExecGuest(ctx context.Context, node string, vmid int, args []string) (pve.GuestExecResult, error) {
	if slices.Contains(args, "computer-v2-session") && !g.ready {
		return pve.GuestExecResult{ExitCode: 1}, nil
	}
	if slices.Contains(args, "computer-v2-capabilities") {
		fence := accountFenceTransport
		if g.failure == "old-protocol" {
			fence = ""
		}
		birth := loginBirthFence
		if g.failure == "missing-pam-fence" {
			birth = "unavailable"
		}
		if g.failure == "old-pam-fence" {
			birth = "pam_logind_pidfd_v2"
		}
		raw, _ := json.Marshal(map[string]any{"schema_version": 2, "authority_transport": authorityTransport, "experimental_account_fence": fence, "login_birth_fence": birth})
		return pve.GuestExecResult{Stdout: string(raw)}, nil
	}
	if slices.Contains(args, "computer-v2-account-inspect") {
		disabled := g.lease == nil || g.lease.Phase == "revoked"
		absent := disabled && g.failure != "remaining-processes"
		var day any
		if !disabled {
			day = (g.lease.ExpiresUnixSeconds + 86399) / 86400
		}
		raw, _ := json.Marshal(map[string]any{"schema_version": 1, "username": g.target.Username,
			"identity": AccountIdentity{Username: g.target.Username, UID: g.target.UID},
			"account":  map[string]any{"exists": true, "disabled": disabled, "expiry_day": day}, "processes_absent": absent, "login_writers_absent": disabled && g.failure != "remaining-root-writer", "lifecycle": g.lease})
		return pve.GuestExecResult{Stdout: string(raw)}, nil
	}
	return g.sessionGuest.ExecGuest(ctx, node, vmid, args)
}

func (*agentLoginGuest) SetGuestUserPassword(context.Context, string, int, string, string) error {
	panic("unfenced password write")
}

func (g *agentLoginGuest) ExecGuestWithInput(_ context.Context, _ string, _ int, args []string, input []byte) (pve.GuestExecResult, error) {
	g.loginArgs = slices.Clone(args)
	g.inputs = append(g.inputs, slices.Clone(input))
	var request map[string]any
	if json.Unmarshal(input, &request) != nil {
		panic("invalid fixed login input")
	}
	phase := "sealed"
	if request["operation"] == "revoke" {
		phase = "revoked"
	}
	delete(request, "password")
	delete(request, "operation")
	request["phase"] = phase
	raw, _ := json.Marshal(request)
	lease, err := parseAccountLease(raw, "linux")
	if err != nil {
		panic(err)
	}
	g.lease = &lease
	g.ready = phase == "sealed"
	switch g.failure {
	case "lost":
		return pve.GuestExecResult{}, errors.New("private-sentinel: transport lost after login")
	case "denied":
		return pve.GuestExecResult{ExitCode: 1, Stderr: "private-sentinel"}, nil
	case "empty":
		return pve.GuestExecResult{}, nil
	case "generation":
		request["login_generation"] = 9
	case "uid":
		request["identity"].(map[string]any)["uid"] = 1002
	case "phase":
		request["phase"] = "open"
	case "secret-field":
		request["password"] = "private-sentinel"
	}
	raw, _ = json.Marshal(request)
	return pve.GuestExecResult{Stdout: string(raw)}, nil
}

func linuxLoginIntent() AccountLease {
	target := testSessionTarget()
	return AccountLease{SchemaVersion: 1, Identity: AccountIdentity{Username: target.Username, UID: target.UID},
		LeaseID: "lease_bound_linux", ControlEpoch: 10, LoginGeneration: 1, ExpiresUnixSeconds: time.Now().Add(time.Hour).Unix()}
}

func TestAgentSessionBootstrapSendsOneBoundLoginAndRequiresSealedReceipt(t *testing.T) {
	g := &agentLoginGuest{sessionGuest: newSessionGuest()}
	e := NewPVEExecutor(g)
	identity, err := e.PrepareAgentAccount(t.Context(), pve.VM{}, g.target.Username)
	if err != nil || identity != linuxLoginIntent().Identity || len(g.inputs) != 0 {
		t.Fatal("account preparation installed credentials", err)
	}
	target, err := e.StartAgentSession(t.Context(), pve.VM{}, linuxLoginIntent())
	if err != nil || target != g.target || len(g.inputs) != 1 {
		t.Fatal("bound login failed", err)
	}
	if !slices.Equal(g.loginArgs, []string{sessionBinary, "computer-v2-account-login", "--guest-user", g.target.Username}) {
		t.Fatal("unfenced login argv")
	}
	var payload map[string]any
	_ = json.Unmarshal(g.inputs[0], &payload)
	secret := payload["password"].(string)
	if len(secret) < 24 || payload["control_epoch"] != float64(10) || payload["login_generation"] != float64(1) {
		t.Fatal("missing credential/version binding")
	}
	for _, args := range append(g.commands, g.loginArgs) {
		if strings.Contains(strings.Join(args, " "), secret) {
			t.Fatal("credential in argv")
		}
	}
	if len(g.writtenPaths) != 0 {
		t.Fatal("password spooled to disk")
	}
	if err := e.StopAgentSession(t.Context(), pve.VM{}, linuxLoginIntent()); err != nil || len(g.inputs) != 2 {
		t.Fatal("exact versioned cleanup failed", err)
	}
}

func TestAgentSessionLostOrWrongLoginReceiptsDoNotReplayOrFallback(t *testing.T) {
	for _, failure := range []string{"lost", "denied", "empty", "generation", "uid", "phase", "secret-field", "old-protocol", "missing-pam-fence", "old-pam-fence"} {
		t.Run(failure, func(t *testing.T) {
			g := &agentLoginGuest{sessionGuest: newSessionGuest(), failure: failure}
			_, err := NewPVEExecutor(g).StartAgentSession(t.Context(), pve.VM{}, linuxLoginIntent())
			if err == nil || strings.Contains(err.Error(), "private-sentinel") {
				t.Fatal("unsafe login acknowledgement", err)
			}
			count := 1
			if failure == "old-protocol" || failure == "missing-pam-fence" || failure == "old-pam-fence" {
				count = 0
			}
			if len(g.inputs) != count {
				t.Fatal("write was replayed or old protocol mutated", len(g.inputs))
			}
		})
	}
	for _, failure := range []string{"remaining-processes", "remaining-root-writer"} {
		g := &agentLoginGuest{sessionGuest: newSessionGuest(), failure: failure}
		if err := NewPVEExecutor(g).StopAgentSession(t.Context(), pve.VM{}, linuxLoginIntent()); err == nil {
			t.Fatal("pending processes acknowledged as cleanup", failure)
		}
	}
}

func TestLinuxAccountObservationRejectsMissingFieldsAndUnboundFalseAbsence(t *testing.T) {
	g := &agentLoginGuest{sessionGuest: newSessionGuest()}
	result, _ := g.ExecGuest(t.Context(), "", 0, []string{sessionBinary, "computer-v2-account-inspect"})
	var base map[string]any
	_ = json.Unmarshal([]byte(result.Stdout), &base)
	for _, field := range []string{"identity", "account", "processes_absent", "login_writers_absent", "lifecycle", "username", "schema_version"} {
		var value map[string]any
		_ = json.Unmarshal([]byte(result.Stdout), &value)
		delete(value, field)
		raw, _ := json.Marshal(value)
		if _, err := parseLinuxAccountObservation(raw, g.target.Username); err == nil {
			t.Fatal("missing observation accepted", field)
		}
	}
	base["identity"] = nil
	raw, _ := json.Marshal(base)
	if _, err := parseLinuxAccountObservation(raw, g.target.Username); err == nil {
		t.Fatal("unbound UID got absence proof")
	}
	base["account"].(map[string]any)["exists"] = false
	base["processes_absent"] = nil
	base["login_writers_absent"] = nil
	raw, _ = json.Marshal(base)
	state, err := parseLinuxAccountObservation(raw, g.target.Username)
	if err != nil || state.Identity != nil || state.LoginStopped() {
		t.Fatal("unknown UID was treated as a revoked owned account", err)
	}
}
