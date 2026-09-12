package httpapi

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/computer"
	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
	"github.com/Veritas-Calculus/vc-workspace/internal/testdb"
)

type accountBindingExecutor struct {
	controlledComputerExecutor
	target                   computer.SessionTarget
	discoveries, activations int
}

func (e *accountBindingExecutor) DiscoverSession(context.Context, pve.VM, string) (computer.SessionTarget, error) {
	e.discoveries++
	return e.target, nil
}

func (e *accountBindingExecutor) ActivateSessionAuthority(context.Context, pve.VM, computer.SessionTarget, computer.Authority) error {
	e.activations++
	return nil
}

func TestAgentPreparationChecksPinnedAccountBeforePublishingAuthority(t *testing.T) {
	for _, changedAccount := range []bool{false, true} {
		name := "helper_restart"
		if changedAccount {
			name = "replaced_account"
		}
		t.Run(name, func(t *testing.T) {
			db, err := store.Open(t.Context(), testdb.URL(t))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(db.Close)
			if err := db.Migrate(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := db.UpsertManagedDesktop(t.Context(), store.ManagedDesktop{VMID: 9001, Node: "test", OSFamily: "linux", DisplayName: "Binding"}); err != nil {
				t.Fatal(err)
			}
			policy, err := db.EnsureDesktopAccessPolicy(t.Context(), 9001)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.MarkDesktopAccessPolicyApplied(t.Context(), 9001, policy.DesiredRevision, "linux"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.CreateAgentPrincipal(t.Context(), store.AgentPrincipal{ID: "agent-a", DisplayName: "Agent"}, []byte("unused")); err != nil {
				t.Fatal(err)
			}
			if _, err := db.PutDesktopAssignment(t.Context(), store.DesktopAssignment{SubjectType: "agent", SubjectID: "agent-a", DesktopVMID: 9001}); err != nil {
				t.Fatal(err)
			}
			lease, err := db.CreateDesktopLease(t.Context(), store.DesktopLease{ID: "lease_pinned_account", AgentID: "agent-a", DesktopID: "9001", ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			binding, err := db.BeginAgentGuestSession(t.Context(), lease)
			if err != nil {
				t.Fatal(err)
			}
			binding.GuestUID, binding.SessionID, binding.InstanceID = 1001, "linux:1001:123::10", strings.Repeat("a", 64)
			if err := db.BindAgentGuestAccount(t.Context(), binding); err != nil {
				t.Fatal(err)
			}
			next, err := db.BeginAgentGuestLogin(t.Context(), binding)
			if err != nil {
				t.Fatal(err)
			}
			binding.LoginGeneration = next.LoginGeneration
			if err := db.MarkAgentGuestSessionReady(t.Context(), binding); err != nil {
				t.Fatal(err)
			}
			executor := &accountBindingExecutor{target: computer.SessionTarget{Username: binding.GuestUsername, UID: 1001, SessionID: binding.SessionID, InstanceID: strings.Repeat("b", 64)}}
			intent := agentAccountLease(binding)
			intent.Phase = "sealed"
			executor.accounts.Store(binding.GuestUsername, intent)
			if changedAccount {
				executor.target.UID = 1002
				executor.target.SessionID = "linux:1002:456::11"
			}
			s := New(Dependencies{Store: db, Computer: executor})
			err = db.WithDesktopConnectionLock(t.Context(), 9001, func() error {
				_, err := s.prepareAgentGuestSession(t.Context(), pve.VM{VMID: 9001, Node: "test"}, lease)
				return err
			})
			if changedAccount {
				if !errors.Is(err, store.ErrConflict) || executor.activations != 0 {
					t.Fatal("replaced account received authority", err, executor.activations)
				}
				current, err := db.DesktopLeaseByID(t.Context(), lease.ID)
				if err != nil || current.State != "revoked" {
					t.Fatal("failed bootstrap retained lease", current, err)
				}
				rows, err := db.AgentGuestSessions(t.Context(), 9001)
				if err != nil || len(rows) != 1 || rows[0].State != "revoking" || rows[0].GuestUID != 1001 {
					t.Fatal("lost pinned identity or cleanup", rows, err)
				}
			} else {
				if err != nil || executor.activations != 1 {
					t.Fatal("same-account Helper restart failed", err)
				}
				rows, err := db.AgentGuestSessions(t.Context(), 9001)
				if err != nil || len(rows) != 1 || rows[0].InstanceID != executor.target.InstanceID || rows[0].GuestUID != 1001 {
					t.Fatal("new instance not bound", rows, err)
				}
			}
		})
	}
}

func TestWindowsAgentBindingDoesNotOpenUnimplementedBootstrap(t *testing.T) {
	db, err := store.Open(t.Context(), testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertManagedDesktop(t.Context(), store.ManagedDesktop{VMID: 9001, Node: "test", OSFamily: "windows", DisplayName: "Windows"}); err != nil {
		t.Fatal(err)
	}
	executor := &accountBindingExecutor{}
	s := New(Dependencies{Store: db, Computer: executor})
	_, err = s.prepareAgentGuestSession(t.Context(), pve.VM{VMID: 9001, Node: "test"}, store.DesktopLease{})
	if !errors.Is(err, computer.ErrUnavailable) || executor.activations != 0 || executor.discoveries != 0 {
		t.Fatal("schema support enabled unattended Windows actions", err)
	}
	rows, err := db.AgentGuestSessions(t.Context(), 9001)
	if err != nil || len(rows) != 0 {
		t.Fatal("closed capability created Guest binding", rows, err)
	}
}
