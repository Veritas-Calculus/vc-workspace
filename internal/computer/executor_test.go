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

type fakeGuestChannel struct {
	configuration       pve.VMConfiguration
	writtenPath         string
	written             string
	writtenPaths        []string
	commands            [][]string
	response            Response
	waitExitCode        int
	currentAgentMissing bool
}

func (f *fakeGuestChannel) VMConfiguration(context.Context, string, int) (pve.VMConfiguration, error) {
	return f.configuration, nil
}
func (f *fakeGuestChannel) WriteGuestFile(_ context.Context, _ string, _ int, path, content string) error {
	f.writtenPath, f.written = path, content
	f.writtenPaths = append(f.writtenPaths, path)
	return nil
}
func (f *fakeGuestChannel) ReadGuestFile(context.Context, string, int, string, int) (string, error) {
	encoded, _ := json.Marshal(f.response)
	return string(encoded), nil
}
func (f *fakeGuestChannel) ExecGuest(_ context.Context, _ string, _ int, command []string) (pve.GuestExecResult, error) {
	f.commands = append(f.commands, command)
	if f.currentAgentMissing && strings.Contains(strings.Join(command, " "), "vc-workspace-guest-agent") {
		return pve.GuestExecResult{ExitCode: 1}, nil
	}
	if slices.Contains(command, "computer-dispatch") {
		encoded, _ := json.Marshal(f.response)
		return pve.GuestExecResult{ExitCode: f.waitExitCode, Stdout: string(encoded)}, nil
	}
	return pve.GuestExecResult{}, nil
}

func TestPVEExecutorUsesFixedGuestBinaryAndFileChannel(t *testing.T) {
	now := time.Now()
	request := validRequest(now)
	guest := &fakeGuestChannel{
		configuration: pve.VMConfiguration{OSType: "win11"},
		response: Response{SchemaVersion: SchemaVersion, RequestID: request.RequestID, OK: true,
			Input: &InputResult{Applied: true}},
	}
	response, err := NewPVEExecutor(guest).Execute(t.Context(), pve.VM{Node: "node-1", VMID: 9113}, request, 5*time.Second)
	if err != nil || !response.OK {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	if !strings.HasSuffix(guest.writtenPath, `\requests\`+request.RequestID+`.json.tmp`) {
		t.Fatalf("unexpected staged path %q", guest.writtenPath)
	}
	dispatch := commandContaining(t, guest.commands, "computer-dispatch")
	if strings.Contains(strings.Join(dispatch, " "), "abcdefghijklmnopqrstuvwxyz\"") {
		t.Fatal("request payload must not be placed on the guest process command line")
	}
	if dispatch[0] != `C:\Program Files\VC Workspace\Agent\vc-workspace-guest-agent.exe` {
		t.Fatalf("unexpected executable: %#v", dispatch)
	}
	var staged Request
	if err := json.Unmarshal([]byte(guest.written), &staged); err != nil || staged.RequestID != request.RequestID {
		t.Fatalf("invalid staged request: %#v err=%v", staged, err)
	}
}

func TestPVEExecutorDispatchesStagedLinuxRequest(t *testing.T) {
	now := time.Now()
	request := validRequest(now)
	guest := &fakeGuestChannel{
		configuration: pve.VMConfiguration{OSType: "l26"},
		response:      Response{SchemaVersion: SchemaVersion, RequestID: request.RequestID, OK: true, Input: &InputResult{Applied: true}},
	}
	if _, err := NewPVEExecutor(guest).Execute(t.Context(), pve.VM{Node: "node-1", VMID: 158}, request, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if got := commandContaining(t, guest.commands, "computer-dispatch"); got[0] != "/usr/local/sbin/vc-workspace-guest-agent" {
		t.Fatalf("unexpected dispatch command: %#v", got)
	}
}

func TestPVEExecutorFallsBackToLegacyAgentDuringRollingUpgrade(t *testing.T) {
	now := time.Now()
	request := validRequest(now)
	guest := &fakeGuestChannel{
		configuration:       pve.VMConfiguration{OSType: "l26"},
		currentAgentMissing: true,
		response:            Response{SchemaVersion: SchemaVersion, RequestID: request.RequestID, OK: true, Input: &InputResult{Applied: true}},
	}
	if _, err := NewPVEExecutor(guest).Execute(t.Context(), pve.VM{Node: "node-1", VMID: 158}, request, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	dispatch := commandContaining(t, guest.commands, "computer-dispatch")
	if dispatch[0] != "/usr/local/sbin/vc-vdi-guest-agent" {
		t.Fatalf("unexpected legacy dispatch command: %#v", dispatch)
	}
}

func TestPVEExecutorPublishesAuthorityAtomically(t *testing.T) {
	guest := &fakeGuestChannel{configuration: pve.VMConfiguration{OSType: "l26"}}
	authority := Authority{SchemaVersion: SchemaVersion, LeaseID: "lease_abcdefghijklmnopqrstuvwxyz", ControlEpoch: 1, ExpiresUnixMS: time.Now().Add(time.Minute).UnixMilli()}
	if err := NewPVEExecutor(guest).ActivateAuthority(t.Context(), pve.VM{Node: "node-1", VMID: 158}, authority); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(guest.writtenPath, "/authority.json.tmp") {
		t.Fatalf("unexpected authority staging path: %q", guest.writtenPath)
	}
	if got := commandContaining(t, guest.commands, "authority.json.tmp"); len(got) != 3 || got[0] != "/bin/sh" {
		t.Fatalf("unexpected authority publish command: %#v", got)
	}
}

func commandContaining(t *testing.T, commands [][]string, value string) []string {
	t.Helper()
	for _, command := range commands {
		if strings.Contains(strings.Join(command, " "), value) {
			return command
		}
	}
	t.Fatalf("no command contains %q: %#v", value, commands)
	return nil
}

func TestPVEExecutorResynchronizesAuthorityInsteadOfTrustingLocalCache(t *testing.T) {
	guest := &fakeGuestChannel{configuration: pve.VMConfiguration{OSType: "l26"}}
	executor := NewPVEExecutor(guest)
	machine := pve.VM{Node: "node-1", VMID: 158}
	authority := Authority{SchemaVersion: SchemaVersion, LeaseID: "lease_abcdefghijklmnopqrstuvwxyz", ControlEpoch: 1, ExpiresUnixMS: time.Now().Add(time.Minute).UnixMilli()}
	if err := executor.ActivateAuthority(t.Context(), machine, authority); err != nil {
		t.Fatal(err)
	}
	if err := executor.ActivateAuthority(t.Context(), machine, authority); err != nil {
		t.Fatal(err)
	}
	if len(guest.writtenPaths) != 2 {
		t.Fatalf("identical authority was written %d times", len(guest.writtenPaths))
	}
	authority.ControlEpoch++
	if err := executor.ActivateAuthority(t.Context(), machine, authority); err != nil {
		t.Fatal(err)
	}
	if len(guest.writtenPaths) != 3 {
		t.Fatal("a new control epoch must be synchronized")
	}
	if err := executor.RevokeAuthority(t.Context(), machine, authority); err != nil {
		t.Fatal(err)
	}
	command := commandContaining(t, guest.commands, "Fence both legacy spools")
	if len(command) != 4 || !strings.Contains(command[2], `publish("vc-vdi")`) || !strings.Contains(command[2], `publish("vc-workspace")`) {
		t.Fatal("revocation must synchronize both installed generations")
	}
	var revoked Authority
	if err := json.Unmarshal([]byte(command[3]), &revoked); err != nil || revoked.State != "revoked" {
		t.Fatal("revocation did not write a denial tombstone", err)
	}
}

func TestTransientGuestOperationErrors(t *testing.T) {
	for _, err := range []error{
		errors.New("QEMU guest agent is not running"),
		errors.New("VM 9113 qga command 'guest-file-read' failed - got timeout"),
		errors.New("PVE returned 596"),
	} {
		if !transientGuestOperationError(err) {
			t.Fatalf("expected transient error: %v", err)
		}
	}
	if transientGuestOperationError(errors.New("permission denied")) {
		t.Fatal("permanent guest errors must not be retried")
	}
}

func TestPVEExecutorMapsHelperTimeout(t *testing.T) {
	now := time.Now()
	request := validRequest(now)
	guest := &fakeGuestChannel{configuration: pve.VMConfiguration{OSType: "l26"}, waitExitCode: 124}
	if _, err := NewPVEExecutor(guest).Execute(t.Context(), pve.VM{Node: "node-1", VMID: 158}, request, 5*time.Second); err != ErrTimeout {
		t.Fatalf("expected timeout, got %v", err)
	}
}

func TestPVEExecutorRejectsExpiryBeyondActionTimeout(t *testing.T) {
	request := validRequest(time.Now())
	guest := &fakeGuestChannel{configuration: pve.VMConfiguration{OSType: "l26"}}
	if _, err := NewPVEExecutor(guest).Execute(t.Context(), pve.VM{Node: "node-1", VMID: 158}, request, time.Second); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected an inconsistent expiry to be rejected, got %v", err)
	}
	if guest.writtenPath != "" {
		t.Fatal("an invalid request must not be staged in the guest")
	}
}

func TestLiveHelperHeartbeatRejectsStaleMarkers(t *testing.T) {
	now := time.UnixMilli(2_000_000)
	if !liveHelperHeartbeatFresh("1995000\n", now) {
		t.Fatal("a recent interactive helper heartbeat must be accepted")
	}
	for _, value := range []string{"not-a-time", "1900000", "2010000"} {
		if liveHelperHeartbeatFresh(value, now) {
			t.Fatalf("stale or invalid helper heartbeat was accepted: %q", value)
		}
	}
}
