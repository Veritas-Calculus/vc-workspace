package computer

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
)

func testSessionTarget() SessionTarget {
	return SessionTarget{Username: "vca0123456789ab", UID: 1001, SessionID: "linux:1001:123::10.0", InstanceID: strings.Repeat("a", 64)}
}

func TestSessionTargetUsesNativeOSIdentityWithoutUIDSIDCoercion(t *testing.T) {
	linux := testSessionTarget()
	windows := SessionTarget{Username: linux.Username, SID: "S-1-5-21-1-2-3-1001", SessionID: "windows:2:000000000001a2b3", InstanceID: linux.InstanceID}
	for _, target := range []SessionTarget{linux, windows} {
		if err := target.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for name, change := range map[string]func(*SessionTarget){
		"system_sid":    func(v *SessionTarget) { v.SID = "S-1-5-18" },
		"admin_sid":     func(v *SessionTarget) { v.SID = "S-1-5-32-544" },
		"low_rid":       func(v *SessionTarget) { v.SID = "S-1-5-21-1-2-3-500" },
		"sid_sddl":      func(v *SessionTarget) { v.SID += ")(A;;GA;;;WD)" },
		"sid_overflow":  func(v *SessionTarget) { v.SID = "S-1-5-21-1-2-3-4294967296" },
		"sid_alias":     func(v *SessionTarget) { v.SID = "S-1-5-21-01-2-3-1001" },
		"unix_uid":      func(v *SessionTarget) { v.UID = 1001 },
		"session_zero":  func(v *SessionTarget) { v.SessionID = "windows:0:000000000001a2b3" },
		"missing_logon": func(v *SessionTarget) { v.SessionID = "windows:2" },
		"zero_logon":    func(v *SessionTarget) { v.SessionID = "windows:2:0000000000000000" },
		"session_alias": func(v *SessionTarget) { v.SessionID = "windows:02:000000000001a2b3" },
		"linux_session": func(v *SessionTarget) { v.SessionID = linux.SessionID },
	} {
		t.Run(name, func(t *testing.T) {
			value := windows
			change(&value)
			if value.Validate() == nil {
				t.Fatal("accepted an ambiguous or privileged Windows identity")
			}
		})
	}
	linux.SessionID = "linux:1002:123::10.0"
	if linux.Validate() == nil {
		t.Fatal("Linux UID and session identity mismatch accepted")
	}
	guest := newSessionGuest()
	executor := NewPVEExecutor(guest)
	if _, err := executor.ExecuteForSession(t.Context(), pve.VM{VMID: 158}, windows, validRequest(time.Now()), time.Second); err == nil || len(guest.commands) != 0 {
		t.Fatal("a Windows target was dispatched through the Linux-only executor")
	}
}

type sessionGuest struct {
	fakeGuestChannel
	target          SessionTarget
	request         boundAction
	failOperation   string
	lostDispatch    bool
	dispatchCount   int
	wrongResponseID bool
	authorityInput  []byte
	authorityCalls  int
	missingFence    bool
	lostAuthority   bool
	legacyHelper    bool
	legacyAuthority bool
}

func (f *sessionGuest) WriteGuestFile(ctx context.Context, node string, vmid int, path, content string) error {
	if strings.Contains(path, "/inbox/") {
		if err := json.Unmarshal([]byte(content), &f.request); err != nil {
			return err
		}
	}
	return f.fakeGuestChannel.WriteGuestFile(ctx, node, vmid, path, content)
}

func (*sessionGuest) ReadGuestFile(context.Context, string, int, string, int) (string, error) {
	panic("root must not read a user-writable response file")
}

func (f *sessionGuest) ExecGuest(_ context.Context, _ string, _ int, args []string) (pve.GuestExecResult, error) {
	f.commands = append(f.commands, append([]string(nil), args...))
	if len(args) < 2 || args[0] != sessionBinary || !strings.HasPrefix(args[1], "computer-v2-") {
		return pve.GuestExecResult{}, errors.New("legacy or shell fallback is forbidden")
	}
	if args[1] == "computer-v2-"+f.failOperation {
		return pve.GuestExecResult{ExitCode: 1, Stderr: "private-desktop-sentinel"}, nil
	}
	if args[1] == "computer-v2-session" {
		transport := authorityTransport
		if f.legacyHelper {
			transport = "stdin_epoch_v1"
		}
		payload, _ := json.Marshal(map[string]any{"schema_version": 2, "target": f.target, "authority_transport": transport})
		return pve.GuestExecResult{Stdout: string(payload)}, nil
	}
	if args[1] == "computer-v2-capabilities" {
		transport := authorityTransport
		if f.missingFence {
			transport = ""
		}
		if f.legacyAuthority {
			transport = "stdin_epoch_v1"
		}
		payload, _ := json.Marshal(map[string]any{"schema_version": 2, "authority_transport": transport})
		return pve.GuestExecResult{Stdout: string(payload)}, nil
	}
	if args[1] == "computer-v2-dispatch" {
		f.dispatchCount++
		if f.lostDispatch && f.dispatchCount == 1 {
			return pve.GuestExecResult{}, errors.New("QGA command timeout")
		}
		id := f.request.Request.RequestID
		if f.wrongResponseID {
			id += "wrong"
		}
		payload, _ := json.Marshal(Response{SchemaVersion: 1, RequestID: id, OK: true, Input: &InputResult{Applied: true}})
		return pve.GuestExecResult{Stdout: string(payload)}, nil
	}
	return pve.GuestExecResult{}, nil
}

func newSessionGuest() *sessionGuest {
	return &sessionGuest{fakeGuestChannel: fakeGuestChannel{configuration: pve.VMConfiguration{OSType: "l26"}}, target: testSessionTarget()}
}

func (f *sessionGuest) ExecGuestWithInput(ctx context.Context, node string, vmid int, args []string, input []byte) (pve.GuestExecResult, error) {
	f.authorityCalls++
	f.authorityInput = append([]byte(nil), input...)
	if f.lostAuthority {
		return pve.GuestExecResult{}, errors.New("QGA command timeout private-desktop-sentinel")
	}
	return f.ExecGuest(ctx, node, vmid, args)
}

func TestSessionExecutorBindsAnImmutableActionAndRetriesWithoutLegacyFallback(t *testing.T) {
	guest := newSessionGuest()
	guest.lostDispatch = true
	executor := NewPVEExecutor(guest)
	machine := pve.VM{VMID: 158, Node: "test"}
	if err := executor.InitializeSessionTransport(t.Context(), machine, guest.target.Username); err != nil {
		t.Fatal(err)
	}
	target, err := executor.DiscoverSession(t.Context(), machine, guest.target.Username)
	if err != nil || target != guest.target {
		t.Fatalf("target=%+v err=%v", target, err)
	}
	request := validRequest(time.Now())
	request.Operation, request.Screenshot, request.Text = OperationTypeText, nil, &Text{Value: "private-input-sentinel"}
	response, err := executor.ExecuteForSession(t.Context(), machine, target, request, 5*time.Second)
	if err != nil || !response.OK || guest.dispatchCount != 2 {
		t.Fatalf("response=%+v err=%v dispatches=%d", response, err, guest.dispatchCount)
	}
	if guest.request.SchemaVersion != 2 || guest.request.Target != target || guest.request.TimeoutMS != 5000 {
		t.Fatalf("incorrect action binding: %+v", guest.request)
	}
	if guest.request.Request.ExpiresUnixMS <= request.ExpiresUnixMS || guest.request.Request.ExpiresUnixMS > time.Now().Add(10*time.Second).UnixMilli() {
		t.Fatal("transport grace must be bounded independently of worker timeout")
	}
	if len(guest.writtenPaths) != 1 || !strings.HasPrefix(guest.writtenPath, sessionBase+"/inbox/") {
		t.Fatalf("unexpected request staging: %+v", guest.writtenPaths)
	}
	if !slices.Contains(guest.commands[len(guest.commands)-1], "computer-v2-cleanup") {
		t.Fatal("completed request was not cleaned up")
	}
	var dispatches [][]string
	for _, args := range guest.commands {
		if slices.Contains(args, "computer-v2-dispatch") {
			dispatches = append(dispatches, args)
		}
		if strings.Contains(strings.Join(args, " "), request.Text.Value) {
			t.Fatal("private action text leaked into process arguments")
		}
	}
	if !reflect.DeepEqual(dispatches[0], dispatches[1]) {
		t.Fatal("transport retry changed request identity")
	}
}

func TestSessionExecutorRejectsUnsafeTargetsAndUnsupportedGuests(t *testing.T) {
	for _, mutate := range []func(*sessionGuest){
		func(g *sessionGuest) { g.target.Username = "vdi" },
		func(g *sessionGuest) { g.target.Username = "vcw0123456789ab" },
		func(g *sessionGuest) { g.target.UID = 0 },
		func(g *sessionGuest) { g.target.InstanceID = "cached" },
		func(g *sessionGuest) { g.target.SessionID = "../active" },
		func(g *sessionGuest) { g.configuration.OSType = "win11" },
		func(g *sessionGuest) { g.failOperation = "session" },
	} {
		guest := newSessionGuest()
		mutate(guest)
		_, err := NewPVEExecutor(guest).DiscoverSession(t.Context(), pve.VM{VMID: 158, Node: "test"}, "vca0123456789ab")
		if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "private-desktop-sentinel") {
			t.Fatalf("unsafe target returned: %v", err)
		}
	}
}

func TestSessionAuthorityContainsTargetAndTombstoneClearsIt(t *testing.T) {
	guest := newSessionGuest()
	executor := NewPVEExecutor(guest)
	machine := pve.VM{VMID: 158, Node: "test"}
	authority := Authority{SchemaVersion: 1, LeaseID: "lease_" + strings.Repeat("a", 24), ControlEpoch: 4, ExpiresUnixMS: time.Now().Add(time.Minute).UnixMilli()}
	if err := executor.ActivateSessionAuthority(t.Context(), machine, guest.target, authority); err != nil {
		t.Fatal(err)
	}
	var bound boundAuthority
	if err := json.Unmarshal(guest.authorityInput, &bound); err != nil || bound.Target == nil || *bound.Target != guest.target || bound.Authority.State != "active" {
		t.Fatalf("invalid active authority: %+v err=%v", bound, err)
	}
	if err := executor.RevokeSessionAuthority(t.Context(), machine, authority); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(guest.authorityInput, &bound); err != nil || bound.Target != nil || bound.Authority.State != "revoked" || bound.Authority.ExpiresUnixMS != 0 || bound.Authority.ControlEpoch != authority.ControlEpoch {
		t.Fatalf("invalid tombstone: %+v err=%v", bound, err)
	}
	if len(guest.writtenPaths) != 0 || guest.authorityCalls != 2 {
		t.Fatal("authority used shared staging or unexpected retry")
	}
}

func TestSessionAuthorityRefusesOldGuestAndNeverRetriesUncertainPublication(t *testing.T) {
	for _, mode := range []string{"old-guest", "pre-account-guest", "lost-result", "rejected"} {
		t.Run(mode, func(t *testing.T) {
			guest := newSessionGuest()
			guest.missingFence = mode == "old-guest"
			guest.legacyAuthority = mode == "pre-account-guest"
			guest.lostAuthority = mode == "lost-result"
			if mode == "rejected" {
				guest.failOperation = "authority"
			}
			err := NewPVEExecutor(guest).ActivateSessionAuthority(t.Context(), pve.VM{VMID: 158}, guest.target, Authority{SchemaVersion: 1, LeaseID: "lease_fixture", ControlEpoch: 8, ExpiresUnixMS: time.Now().Add(time.Minute).UnixMilli()})
			if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "private-desktop-sentinel") {
				t.Fatal(err)
			}
			expected := 1
			if mode == "old-guest" || mode == "pre-account-guest" {
				expected = 0
			}
			if guest.authorityCalls != expected || len(guest.writtenPaths) != 0 {
				t.Fatal("unsafe fallback or duplicate publication")
			}
		})
	}
}

func TestSessionDiscoveryRefusesHelperWithoutAccountInputChecks(t *testing.T) {
	guest := newSessionGuest()
	guest.legacyHelper = true
	if _, err := NewPVEExecutor(guest).DiscoverSession(t.Context(), pve.VM{VMID: 9001}, guest.target.Username); !errors.Is(err, ErrUnavailable) {
		t.Fatal("pre-account Helper was accepted", err)
	}
	if guest.authorityCalls != 0 || guest.dispatchCount != 0 {
		t.Fatal("old Helper caused a write or action")
	}
}

func TestSessionExecutorCleansUpFailedAndMismatchedResponses(t *testing.T) {
	for _, operation := range []string{"publish", "dispatch", "wrong-id"} {
		t.Run(operation, func(t *testing.T) {
			guest := newSessionGuest()
			guest.failOperation = operation
			guest.wrongResponseID = operation == "wrong-id"
			_, err := NewPVEExecutor(guest).ExecuteForSession(t.Context(), pve.VM{VMID: 158, Node: "test"}, guest.target, validRequest(time.Now()), 5*time.Second)
			if err == nil || strings.Contains(err.Error(), "private-desktop-sentinel") {
				t.Fatalf("unexpected error: %v", err)
			}
			if !slices.Contains(guest.commands[len(guest.commands)-1], "computer-v2-cleanup") {
				t.Fatal("failed request cleanup omitted")
			}
		})
	}
}
