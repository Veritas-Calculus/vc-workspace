package store

import (
	"strings"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/testdb"
)

func TestComputerEpochMigrationFencesLegacyActiveGrants(t *testing.T) {
	db, err := Open(t.Context(), testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if _, err := db.pool.Exec(t.Context(), `CREATE TABLE schema_migrations(name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") || entry.Name() >= "025_" {
			continue
		}
		raw, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.pool.Exec(t.Context(), string(raw)); err != nil {
			t.Fatalf("legacy migration %s: %v", entry.Name(), err)
		}
		if _, err := db.pool.Exec(t.Context(), `INSERT INTO schema_migrations(name) VALUES($1)`, entry.Name()); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.UpsertManagedDesktop(t.Context(), ManagedDesktop{VMID: 9001, Node: "test", OSFamily: "linux", DisplayName: "Legacy epoch"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateAgentPrincipal(t.Context(), AgentPrincipal{ID: "agent-old", DisplayName: "Old"}, []byte("test-only")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutDesktopAssignment(t.Context(), DesktopAssignment{SubjectType: "agent", SubjectID: "agent-old", DesktopVMID: 9001}); err != nil {
		t.Fatal(err)
	}
	old, err := db.CreateDesktopLease(t.Context(), DesktopLease{ID: "lease_historical", AgentID: "agent-old", DesktopID: "9001", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(t.Context(), `UPDATE desktop_leases SET state='released',control_epoch=7 WHERE id=$1`, old.ID); err != nil {
		t.Fatal(err)
	}
	active, err := db.CreateDesktopLease(t.Context(), DesktopLease{ID: "lease_legacy_active", AgentID: old.AgentID, DesktopID: old.DesktopID, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil || active.ControlEpoch != 1 {
		t.Fatal(active, err)
	}
	// Finish the old historical cleanup before recording the still-active Helper.
	if _, err := db.pool.Exec(t.Context(), `UPDATE desktop_computer_revocations SET completed_revision=requested_revision`); err != nil {
		t.Fatal(err)
	}
	// Seed the pre-025 shape directly. Current Store methods require current
	// migrations (including the later OS-account binding columns).
	if _, err := db.pool.Exec(t.Context(), `INSERT INTO agent_guest_sessions(desktop_vmid,agent_id,guest_username,lease_id,control_epoch,expires_at)
	VALUES(9001,$1,$2,$3,$4,$5)`, active.AgentID, AgentGuestUsername(active.AgentID), active.ID, active.ControlEpoch, active.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	fenced, err := db.DesktopLeaseByID(t.Context(), active.ID)
	if err != nil || fenced.State != "revoked" || fenced.ControlEpoch != 8 {
		t.Fatal(fenced, err)
	}
	epoch, err := db.ComputerRevocationEpoch(t.Context(), 9001)
	if err != nil || epoch != 8 {
		t.Fatal(epoch, err)
	}
	bindings, err := db.AgentGuestSessions(t.Context(), 9001)
	if err != nil || len(bindings) != 1 || bindings[0].State != "revoking" {
		t.Fatal(bindings, err)
	}
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	epoch, err = db.ComputerControlEpoch(t.Context(), 9001)
	if err != nil || epoch != 8 {
		t.Fatal("migration replay advanced epoch", epoch, err)
	}
	fresh, err := db.CreateDesktopLease(t.Context(), DesktopLease{ID: "lease_after_upgrade", AgentID: old.AgentID, DesktopID: old.DesktopID, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil || fresh.ControlEpoch != 9 {
		t.Fatal(fresh, err)
	}
}

func TestComputerEpochsOrderReacquisitionAndRetainOldQueueFence(t *testing.T) {
	db, first := agentSessionFixture(t)
	closed, err := db.ReleaseDesktopLease(t.Context(), first.ID)
	if err != nil || closed.ControlEpoch <= first.ControlEpoch {
		t.Fatal(closed, err)
	}
	second, err := db.CreateDesktopLease(t.Context(), DesktopLease{ID: "lease_second", AgentID: first.AgentID, DesktopID: first.DesktopID, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil || second.ControlEpoch <= closed.ControlEpoch {
		t.Fatal("new lease reset the VM epoch", second, err)
	}
	closing, err := db.ComputerRevocationEpoch(t.Context(), 9001)
	if err != nil || closing != closed.ControlEpoch || closing >= second.ControlEpoch {
		t.Fatal("old cleanup adopted the new grant's epoch", closing, err)
	}
	current, err := db.ComputerControlEpoch(t.Context(), 9001)
	if err != nil || current != second.ControlEpoch {
		t.Fatal(current, err)
	}
	// Removing a closed historical row cannot reset the next counter.
	if _, err := db.pool.Exec(t.Context(), `DELETE FROM desktop_leases WHERE id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ReleaseDesktopLease(t.Context(), second.ID); err != nil {
		t.Fatal(err)
	}
	third, err := db.CreateDesktopLease(t.Context(), DesktopLease{ID: "lease_third", AgentID: first.AgentID, DesktopID: first.DesktopID, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil || third.ControlEpoch <= second.ControlEpoch+1 {
		t.Fatal(third, err)
	}
	if _, err := db.pool.Exec(t.Context(), `UPDATE desktop_leases SET state='active' WHERE id=$1`, second.ID); err == nil {
		t.Fatal("closed lease reactivated")
	}
}

func TestFailedBootstrapClosesExactLeaseAndQueuesNewEpoch(t *testing.T) {
	db, lease := agentSessionFixture(t)
	binding, err := db.BeginAgentGuestSession(t.Context(), lease)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.QueueAgentGuestSessionRevocation(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	current, err := db.DesktopLeaseByID(t.Context(), lease.ID)
	if err != nil || current.State != "revoked" || current.ControlEpoch <= lease.ControlEpoch {
		t.Fatal(current, err)
	}
	epoch, err := db.ComputerRevocationEpoch(t.Context(), 9001)
	if err != nil || epoch != current.ControlEpoch {
		t.Fatal(epoch, err)
	}
	if err := db.QueueAgentGuestSessionRevocation(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	repeated, err := db.ComputerControlEpoch(t.Context(), 9001)
	if err != nil || repeated != epoch {
		t.Fatal("retry allocated another revocation version", repeated, err)
	}
}
