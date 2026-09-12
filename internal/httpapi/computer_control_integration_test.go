package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/computer"
	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
	"github.com/Veritas-Calculus/vc-workspace/internal/testdb"
)

type controlledComputerExecutor struct {
	started            chan struct{}
	proceed            chan struct{}
	active             atomic.Int32
	revocations        atomic.Int32
	overlapped         atomic.Bool
	failNextRevocation atomic.Bool
	sessionFence       atomic.Int64
	accounts           sync.Map
}

func (c *controlledComputerExecutor) Execute(ctx context.Context, _ pve.VM, r computer.Request, _ time.Duration) (computer.Response, error) {
	c.active.Add(1)
	defer c.active.Add(-1)
	close(c.started)
	select {
	case <-c.proceed:
	case <-ctx.Done():
		return computer.Response{}, ctx.Err()
	}
	return computer.Response{SchemaVersion: computer.SchemaVersion, RequestID: r.RequestID, OK: true, Screenshot: &computer.ScreenshotResult{ContentType: "image/jpeg", Data: "private-screen-sentinel", Width: 320, Height: 200}}, nil
}
func (*controlledComputerExecutor) ActivateAuthority(context.Context, pve.VM, computer.Authority) error {
	return nil
}
func (c *controlledComputerExecutor) RevokeAuthority(context.Context, pve.VM, computer.Authority) error {
	if c.active.Load() != 0 {
		c.overlapped.Store(true)
	}
	c.revocations.Add(1)
	if c.failNextRevocation.Swap(false) {
		return errors.New("temporary QGA failure")
	}
	return nil
}

func (c *controlledComputerExecutor) DiscoverSession(_ context.Context, _ pve.VM, username string) (computer.SessionTarget, error) {
	return computer.SessionTarget{Username: username, UID: 1001, SessionID: "linux:1001:123::10", InstanceID: strings.Repeat("a", 64)}, nil
}
func (*controlledComputerExecutor) InitializeSessionTransport(context.Context, pve.VM, string) error {
	return nil
}
func (*controlledComputerExecutor) ActivateSessionAuthority(context.Context, pve.VM, computer.SessionTarget, computer.Authority) error {
	return nil
}
func (c *controlledComputerExecutor) RevokeSessionAuthority(_ context.Context, _ pve.VM, authority computer.Authority) error {
	c.sessionFence.Store(authority.ControlEpoch)
	return nil
}
func (c *controlledComputerExecutor) ExecuteForSession(ctx context.Context, m pve.VM, target computer.SessionTarget, r computer.Request, d time.Duration) (computer.Response, error) {
	if target.Username != store.AgentGuestUsername("agent-a") {
		return computer.Response{}, errors.New("wrong Agent OS identity")
	}
	return c.Execute(ctx, m, r, d)
}
func (*controlledComputerExecutor) PrepareAgentAccount(_ context.Context, _ pve.VM, username string) (computer.AccountIdentity, error) {
	return computer.AccountIdentity{Username: username, UID: 1001}, nil
}
func (c *controlledComputerExecutor) InspectAgentAccount(_ context.Context, _ pve.VM, username string) (computer.LinuxAccountObservation, error) {
	absent := true
	identity := computer.AccountIdentity{Username: username, UID: 1001}
	value := computer.LinuxAccountObservation{Identity: &identity, Exists: true, Disabled: true, ProcessesAbsent: &absent, LoginWritersAbsent: &absent}
	if raw, ok := c.accounts.Load(username); ok {
		lease := raw.(computer.AccountLease)
		value.Lifecycle = &lease
		value.Disabled = lease.Phase == "revoked"
		absent = value.Disabled
		if !value.Disabled {
			day := uint64((lease.ExpiresUnixSeconds + 86399) / 86400)
			value.ExpiryDay = &day
		}
	}
	return value, nil
}
func (c *controlledComputerExecutor) StartAgentSession(ctx context.Context, m pve.VM, intent computer.AccountLease) (computer.SessionTarget, error) {
	intent.Phase = "sealed"
	c.accounts.Store(intent.Identity.Username, intent)
	return c.DiscoverSession(ctx, m, intent.Identity.Username)
}
func (c *controlledComputerExecutor) StopAgentSession(_ context.Context, _ pve.VM, intent computer.AccountLease) error {
	intent.Phase = "revoked"
	intent.ExpiresUnixSeconds = 0
	c.accounts.Store(intent.Identity.Username, intent)
	return nil
}

func TestComputerControlAcrossReplicas(t *testing.T) {
	for _, revokeDuringAction := range []bool{false, true} {
		name := "release_waits_for_action"
		if revokeDuringAction {
			name = "assignment_revocation_discards_output"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			url := testdb.URL(t)
			first, err := store.Open(ctx, url)
			if err != nil {
				t.Fatal(err)
			}
			defer first.Close()
			second, err := store.Open(ctx, url)
			if err != nil {
				t.Fatal(err)
			}
			defer second.Close()
			if err := first.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			if err := first.UpsertManagedDesktop(ctx, store.ManagedDesktop{VMID: 9001, DisplayName: "Control test", Node: "test", OSFamily: "linux"}); err != nil {
				t.Fatal(err)
			}
			if _, err := first.PutDesktopAccessPolicy(ctx, 9001, "standard", true, false, false, ""); err != nil {
				t.Fatal(err)
			}
			if _, err := first.CreateAgentPrincipal(ctx, store.AgentPrincipal{ID: "agent-a", DisplayName: "Agent"}, []byte("unused")); err != nil {
				t.Fatal(err)
			}
			if _, err := first.PutDesktopAssignment(ctx, store.DesktopAssignment{SubjectType: "agent", SubjectID: "agent-a", DesktopVMID: 9001}); err != nil {
				t.Fatal(err)
			}
			lease, err := first.CreateDesktopLease(ctx, store.DesktopLease{ID: "lease_abcdefghijklmnopqrstuvwxyz", AgentID: "agent-a", DesktopID: "9001", ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			fakePVE := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/version") {
					_, _ = w.Write([]byte(`{"data":{"version":"test"}}`))
					return
				}
				if strings.HasSuffix(r.URL.Path, "/cluster/resources") {
					_, _ = w.Write([]byte(`{"data":[{"vmid":9001,"node":"test","type":"qemu","name":"Control test","status":"running","tags":"vc-workspace;desktop;rdp"}]}`))
					return
				}
				if strings.HasSuffix(r.URL.Path, "/config") {
					_, _ = w.Write([]byte(`{"data":{"ostype":"l26"}}`))
					return
				}
				if strings.HasSuffix(r.URL.Path, "/agent/exec") {
					_, _ = w.Write([]byte(`{"data":{"pid":1}}`))
					return
				}
				if strings.HasSuffix(r.URL.Path, "/agent/exec-status") {
					_, _ = w.Write([]byte(`{"data":{"exited":1,"exitcode":0}}`))
					return
				}
				_, _ = w.Write([]byte(`{"data":[]}`))
			}))
			defer fakePVE.Close()
			pveClient, err := pve.New(pve.Config{Endpoint: fakePVE.URL, TokenID: "test@pve!test", TokenSecret: "unused", HTTPClient: fakePVE.Client(), MutationsEnabled: true})
			if err != nil {
				t.Fatal(err)
			}
			executor := &controlledComputerExecutor{started: make(chan struct{}), proceed: make(chan struct{})}
			var unblock sync.Once
			defer unblock.Do(func() { close(executor.proceed) })
			a := New(Dependencies{Store: first, PVE: pveClient, Computer: executor})
			b := New(Dependencies{Store: second, PVE: pveClient, Computer: executor})
			action := httptest.NewRequest("POST", "/api/v1/agent/desktop-leases/"+lease.ID+"/computer-actions?agent_id=agent-a", strings.NewReader(`{"control_epoch":1,"operation":"screenshot","screenshot":{"max_width":320}}`)).WithContext(ctx)
			action.SetPathValue("id", lease.ID)
			response := httptest.NewRecorder()
			actionDone := make(chan struct{})
			go func() { defer close(actionDone); a.agentComputerAction(response, action) }()
			select {
			case <-executor.started:
			case <-actionDone:
				t.Fatalf("computer action returned before execution: %d %s", response.Code, response.Body.String())
			case <-ctx.Done():
				t.Fatal("computer action never started")
			}
			var releaseResponse *httptest.ResponseRecorder
			releaseDone := make(chan struct{})
			if revokeDuringAction {
				if _, err := second.DeleteDesktopAssignment(ctx, "agent", "agent-a", 9001); err != nil {
					t.Fatal(err)
				}
				close(releaseDone)
			} else {
				release := httptest.NewRequest("DELETE", "/api/v1/agent/desktop-leases/"+lease.ID+"?agent_id=agent-a", nil).WithContext(ctx)
				release.SetPathValue("id", lease.ID)
				releaseResponse = httptest.NewRecorder()
				go func() { defer close(releaseDone); b.releaseAgentDesktopLease(releaseResponse, release) }()
				select {
				case <-releaseDone:
					t.Fatal("another replica released authority during a running action")
				case <-time.After(100 * time.Millisecond):
				}
			}
			unblock.Do(func() { close(executor.proceed) })
			select {
			case <-actionDone:
			case <-ctx.Done():
				t.Fatal("action did not finish")
			}
			select {
			case <-releaseDone:
			case <-ctx.Done():
				t.Fatal("release did not finish")
			}
			if revokeDuringAction {
				if response.Code != 409 || strings.Contains(response.Body.String(), "private-screen-sentinel") {
					t.Fatalf("revoked caller received output: %d %s", response.Code, response.Body)
				}
				executor.failNextRevocation.Store(true)
				b.revokeDueComputerAuthorities(ctx)
				if pending, err := second.PendingComputerRevocations(ctx, 9001); err != nil || len(pending) != 1 {
					t.Fatalf("failed Guest fencing lost its retry: %v %v", pending, err)
				}
				b.revokeDueComputerAuthorities(ctx)
			} else if response.Code != 200 || releaseResponse.Code != 200 {
				t.Fatalf("unexpected action/release status: %d/%d", response.Code, releaseResponse.Code)
			}
			if executor.overlapped.Load() {
				t.Fatal("Guest action overlapped a Guest fencing write")
			}
			if pending, err := first.PendingComputerRevocations(ctx, 9001); err != nil || len(pending) != 0 {
				t.Fatalf("successful fencing was not acknowledged: %v %v", pending, err)
			}
			count := executor.revocations.Load()
			a.revokeDueComputerAuthorities(ctx)
			if executor.revocations.Load() != count {
				t.Fatal("completed queue work was repeated by another replica")
			}
		})
	}
}

func TestSessionCleanupUsesClosingEpochNotNextGrant(t *testing.T) {
	db, err := store.Open(t.Context(), testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertManagedDesktop(t.Context(), store.ManagedDesktop{VMID: 9001, Node: "test", OSFamily: "linux", DisplayName: "Fence"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateAgentPrincipal(t.Context(), store.AgentPrincipal{ID: "agent-a", DisplayName: "A"}, []byte("unused")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutDesktopAssignment(t.Context(), store.DesktopAssignment{SubjectType: "agent", SubjectID: "agent-a", DesktopVMID: 9001}); err != nil {
		t.Fatal(err)
	}
	old, err := db.CreateDesktopLease(t.Context(), store.DesktopLease{ID: "lease_before", AgentID: "agent-a", DesktopID: "9001", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.BeginAgentGuestSession(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	closed, err := db.ReleaseDesktopLease(t.Context(), old.ID)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := db.CreateDesktopLease(t.Context(), store.DesktopLease{ID: "lease_after", AgentID: "agent-a", DesktopID: "9001", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	executor := &controlledComputerExecutor{}
	s := New(Dependencies{Store: db, Computer: executor})
	if err := s.revokeAgentGuestSessions(t.Context(), pve.VM{VMID: 9001}); err != nil {
		t.Fatal(err)
	}
	if actual := executor.sessionFence.Load(); actual != closed.ControlEpoch || actual <= old.ControlEpoch || actual >= fresh.ControlEpoch {
		t.Fatal("wrong Guest tombstone", actual, old.ControlEpoch, closed.ControlEpoch, fresh.ControlEpoch)
	}
}
