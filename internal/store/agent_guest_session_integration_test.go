package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/testdb"
)

func agentSessionFixture(t *testing.T) (*Store, DesktopLease) {
	t.Helper()
	db, err := Open(t.Context(), testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertManagedDesktop(t.Context(), ManagedDesktop{VMID: 9001, Node: "test", OSFamily: "linux", DisplayName: "Agent session"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateAgentPrincipal(t.Context(), AgentPrincipal{ID: "agent-a", DisplayName: "A"}, []byte("test-agent")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutDesktopAssignment(t.Context(), DesktopAssignment{SubjectType: "agent", SubjectID: "agent-a", DesktopVMID: 9001}); err != nil {
		t.Fatal(err)
	}
	lease, err := db.CreateDesktopLease(t.Context(), DesktopLease{ID: "lease_agent_test", AgentID: "agent-a", DesktopID: "9001", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	return db, lease
}

func TestAgentSessionRegisteredBeforeRevocationAndStaleReadyDenied(t *testing.T) {
	for _, reason := range []string{"release", "assignment", "disable", "expiry", "failed-bootstrap"} {
		t.Run(reason, func(t *testing.T) {
			db, lease := agentSessionFixture(t)
			item, err := db.BeginAgentGuestSession(t.Context(), lease)
			if err != nil {
				t.Fatal(err)
			}
			if item.GuestUsername != AgentGuestUsername(lease.AgentID) || !strings.HasPrefix(item.GuestUsername, "vca") {
				t.Fatal("identity not derived from Agent subject")
			}
			same, err := db.BeginAgentGuestSession(t.Context(), lease)
			if err != nil || same.Generation != item.Generation {
				t.Fatal("bootstrap registration is not idempotent", err)
			}
			switch reason {
			case "release":
				_, err = db.ReleaseDesktopLease(t.Context(), lease.ID)
			case "assignment":
				_, err = db.DeleteDesktopAssignment(t.Context(), "agent", lease.AgentID, 9001)
			case "disable":
				_, err = db.SetAgentEnabled(t.Context(), lease.AgentID, false)
			case "expiry":
				_, err = db.pool.Exec(t.Context(), `UPDATE desktop_leases SET expires_at=now()-interval '1 second' WHERE id=$1`, lease.ID)
				if err == nil {
					err = db.QueueExpiredComputerLeases(t.Context(), 9001)
				}
			case "failed-bootstrap":
				err = db.QueueAgentGuestSessionRevocation(t.Context(), item)
			}
			if err != nil {
				t.Fatal(err)
			}
			items, err := db.AgentGuestSessions(t.Context(), 9001)
			if err != nil || len(items) != 1 || items[0].State != "revoking" {
				t.Fatal("in-progress OS identity escaped revocation", items, err)
			}
			item.GuestUID, item.SessionID, item.InstanceID = 1001, "linux:1001:123::10", strings.Repeat("a", 64)
			if err := db.MarkAgentGuestSessionReady(t.Context(), item); !errors.Is(err, ErrConflict) {
				t.Fatal("revoked bootstrap became ready", err)
			}
			if _, err := db.BeginAgentGuestSession(t.Context(), lease); !errors.Is(err, ErrConflict) {
				t.Fatal("pending revocation allowed bootstrap", err)
			}
			pending, err := db.PendingComputerRevocations(t.Context(), 9001)
			if err != nil || len(pending) != 1 {
				t.Fatal("lost Guest cleanup queue", err)
			}
			if err := db.CompleteAgentGuestSessionRevocation(t.Context(), items[0]); err != nil {
				t.Fatal(err)
			}
			if err := db.CompleteAgentGuestSessionRevocation(t.Context(), items[0]); !errors.Is(err, ErrConflict) {
				t.Fatal("duplicate cleanup acknowledged", err)
			}
			if err := db.CompleteComputerRevocation(t.Context(), pending[0]); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAgentSessionGenerationProtectsReacquiredIdentity(t *testing.T) {
	db, lease := agentSessionFixture(t)
	old, err := db.BeginAgentGuestSession(t.Context(), lease)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.QueueAgentGuestSessionRevocation(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteAgentGuestSessionRevocation(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	pending, err := db.PendingComputerRevocations(t.Context(), 9001)
	if err != nil || len(pending) != 1 {
		t.Fatal(err)
	}
	if err := db.CompleteComputerRevocation(t.Context(), pending[0]); err != nil {
		t.Fatal(err)
	}
	lease, err = db.CreateDesktopLease(t.Context(), DesktopLease{ID: "lease_after_bootstrap_failure", AgentID: lease.AgentID, DesktopID: lease.DesktopID, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := db.BeginAgentGuestSession(t.Context(), lease)
	if err != nil || fresh.Generation != old.Generation+1 {
		t.Fatal("new generation missing", err)
	}
	if err := db.QueueAgentGuestSessionRevocation(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	items, err := db.AgentGuestSessions(t.Context(), 9001)
	if err != nil || items[0].State != "provisioning" {
		t.Fatal("old cleanup killed new generation", err)
	}
	fresh.GuestUID, fresh.SessionID, fresh.InstanceID = 1001, "linux:1001:123::10", strings.Repeat("a", 64)
	fresh = reserveAgentBinding(t, db, fresh)
	if err := db.MarkAgentGuestSessionReady(t.Context(), fresh); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteAgentGuestSessionRevocation(t.Context(), old); !errors.Is(err, ErrConflict) {
		t.Fatal("stale completion affected new OS session", err)
	}
	if _, err := db.pool.Exec(t.Context(), `UPDATE desktop_leases SET expires_at=now()-interval '1 second' WHERE id=$1`, lease.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueueExpiredComputerLeases(t.Context(), 0); err != nil {
		t.Fatal("global expiration scan", err)
	}
	items, err = db.AgentGuestSessions(t.Context(), 9001)
	if err != nil || items[0].State != "revoking" {
		t.Fatal("reacquired identity escaped global expiration", err)
	}
}
