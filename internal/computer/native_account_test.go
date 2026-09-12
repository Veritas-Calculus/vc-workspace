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

type nativeAccountGuest struct {
	*sessionGuest
	value   *NativeCredential
	inputs  [][]byte
	argv    []string
	failure string
}

func newNativeAccountGuest() *nativeAccountGuest {
	g := &nativeAccountGuest{sessionGuest: newSessionGuest()}
	g.target.Username = "vcw123456abcdef"
	return g
}

func (g *nativeAccountGuest) ExecGuest(ctx context.Context, node string, vmid int, args []string) (pve.GuestExecResult, error) {
	if slices.Contains(args, "computer-v2-capabilities") {
		fence, birth := nativeAccountFence, nativeLoginBirthFence
		if g.failure == "old-protocol" {
			fence = ""
		}
		if g.failure == "missing-pam" {
			birth = "unavailable"
		}
		raw, _ := json.Marshal(map[string]any{"schema_version": 2, "native_account_fence": fence,
			"native_login_birth_fence": birth, "login_birth_fence": loginBirthFence})
		return pve.GuestExecResult{Stdout: string(raw)}, nil
	}
	if slices.Contains(args, "computer-v2-native-account-inspect") {
		disabled := g.value == nil || g.value.Phase == "revoked"
		var day any
		if !disabled {
			day = (g.value.ExpiresUnixSeconds + 86399) / 86400
		}
		var writers any = disabled
		if g.failure == "missing-pam" {
			writers = nil
		}
		if g.failure == "remaining-writer" {
			writers = false
		}
		if g.failure == "disabled-after-issue" {
			disabled = true
		}
		raw, _ := json.Marshal(map[string]any{"schema_version": 1,
			"identity":         AccountIdentity{Username: g.target.Username, UID: g.target.UID},
			"account":          map[string]any{"exists": true, "disabled": disabled, "expiry_day": day},
			"processes_absent": disabled, "login_writers_absent": writers, "lifecycle": g.value})
		return pve.GuestExecResult{Stdout: string(raw)}, nil
	}
	if slices.Contains(args, "computer-v2-native-account-provision") {
		return pve.GuestExecResult{}, nil
	}
	return g.sessionGuest.ExecGuest(ctx, node, vmid, args)
}

func (g *nativeAccountGuest) ExecGuestWithInput(_ context.Context, _ string, _ int, args []string, input []byte) (pve.GuestExecResult, error) {
	g.inputs = append(g.inputs, slices.Clone(input))
	g.argv = slices.Clone(args)
	var value map[string]any
	if json.Unmarshal(input, &value) != nil {
		panic("invalid Native JSON")
	}
	phase := map[string]string{"issue": "issued", "retire": "retired", "revoke": "revoked"}[value["operation"].(string)]
	delete(value, "operation")
	delete(value, "password")
	value["phase"] = phase
	raw, _ := json.Marshal(value)
	credential, err := parseNativeCredential(raw, "linux")
	if err != nil {
		panic(err)
	}
	g.value = &credential
	switch g.failure {
	case "lost":
		return pve.GuestExecResult{}, errors.New("private-native-sentinel")
	case "empty":
		return pve.GuestExecResult{}, nil
	case "revision":
		value["revision"] = 99
	case "connection":
		value["connection_id"] = "conn_wrong_subject"
	case "identity":
		value["identity"].(map[string]any)["uid"] = 9876
	case "phase":
		value["phase"] = "issuing"
	case "secret-field":
		value["password"] = "private-native-sentinel"
	}
	raw, _ = json.Marshal(value)
	return pve.GuestExecResult{Stdout: string(raw)}, nil
}

func nativeTestIntent(g *nativeAccountGuest) NativeCredential {
	return NativeCredential{SchemaVersion: 1, Identity: AccountIdentity{Username: g.target.Username, UID: g.target.UID},
		ConnectionID: "conn_native_test", Revision: 1, ExpiresUnixSeconds: time.Now().Add(time.Hour).Unix()}
}

func TestNativeCredentialTransportUsesSingleBoundStdinAndIndependentObservation(t *testing.T) {
	g := newNativeAccountGuest()
	e := NewPVEExecutor(g)
	a, err := e.PrepareNativeAccount(t.Context(), pve.VM{}, g.target.Username)
	if err != nil || a != nativeTestIntent(g).Identity || len(g.inputs) != 0 {
		t.Fatal("provision did not bind identity before credentials", err)
	}
	intent := nativeTestIntent(g)
	password := []byte("Vcw1!disposable-native-transport")
	if err := e.ChangeNativeCredential(t.Context(), pve.VM{}, intent, "issue", password); err != nil {
		t.Fatal(err)
	}
	if len(g.inputs) != 1 || !slices.Equal(g.argv, []string{sessionBinary, "computer-v2-native-account-credential", "--guest-user", a.Username}) ||
		strings.Contains(strings.Join(g.argv, " "), string(password)) || len(g.writtenPaths) != 0 {
		t.Fatal("credential replay, unsafe argv or secret spooling")
	}
	intent.Revision++
	if err := e.ChangeNativeCredential(t.Context(), pve.VM{}, intent, "retire", nil); err != nil {
		t.Fatal(err)
	}
	intent.Revision++
	intent.ExpiresUnixSeconds = 0
	if err := e.ChangeNativeCredential(t.Context(), pve.VM{}, intent, "revoke", nil); err != nil || len(g.inputs) != 3 {
		t.Fatal("versioned cleanup failed", err)
	}
}

func TestNativeCredentialTransportRejectsUpgradeGapsUncertainReceiptsAndIncompleteClosure(t *testing.T) {
	for _, failure := range []string{"old-protocol", "missing-pam", "lost", "empty", "revision", "connection", "identity", "phase", "secret-field", "disabled-after-issue", "remaining-writer"} {
		t.Run(failure, func(t *testing.T) {
			g := newNativeAccountGuest()
			g.failure = failure
			intent, operation, password := nativeTestIntent(g), "issue", []byte("Vcw1!disposable-native-transport")
			if failure == "remaining-writer" {
				intent.ExpiresUnixSeconds, operation, password = 0, "revoke", nil
			}
			err := NewPVEExecutor(g).ChangeNativeCredential(t.Context(), pve.VM{}, intent, operation, password)
			if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "private-native-sentinel") {
				t.Fatal("accepted unavailable/mismatched Guest or leaked diagnostics", err)
			}
			want := 1
			if failure == "old-protocol" || failure == "missing-pam" {
				want = 0
			}
			if len(g.inputs) != want {
				t.Fatal("retried a credential or mutated before prerequisite", len(g.inputs))
			}
		})
	}
	g := newNativeAccountGuest()
	g.failure = "missing-pam"
	intent := nativeTestIntent(g)
	intent.ExpiresUnixSeconds = 0
	if err := NewPVEExecutor(g).ChangeNativeCredential(t.Context(), pve.VM{}, intent, "revoke", nil); err == nil || len(g.inputs) != 1 {
		t.Fatal("unknown login-writer closure was acknowledged or account disable was skipped", err)
	}
}

func TestNativeCredentialParserSeparatesAgentIdentityAndRejectsMissingObservation(t *testing.T) {
	g := newNativeAccountGuest()
	intent := nativeTestIntent(g)
	intent.Phase = "issued"
	raw, _ := json.Marshal(intent)
	if _, err := parseAccountLease(raw, "linux"); err == nil {
		t.Fatal("Native was reinterpreted as Agent lease")
	}
	g.value = &intent
	observed, _ := g.ExecGuest(t.Context(), "", 0, []string{"computer-v2-native-account-inspect"})
	for _, field := range []string{"identity", "account", "processes_absent", "login_writers_absent", "lifecycle"} {
		var wire map[string]any
		_ = json.Unmarshal([]byte(observed.Stdout), &wire)
		delete(wire, field)
		raw, _ := json.Marshal(wire)
		if _, err := parseNativeAccountObservation(raw, g.target.Username); err == nil {
			t.Fatal("missing evidence accepted", field)
		}
	}
}
