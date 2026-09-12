package store

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/testdb"
)

func TestDesktopControlLocksAcrossReplicasAndCancellation(t *testing.T) {
	url := testdb.URL(t)
	first, err := Open(t.Context(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(t.Context(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	unlock, err := first.AcquireDesktopControlLock(t.Context(), 9001)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if release, err := second.AcquireDesktopControlLock(ctx, 9001); err == nil {
		release()
		t.Fatal("second replica bypassed native/computer control gate")
	}
	unlock()
	unlock() // cleanup is safe even if a handler explicitly released early
	ctx, cancel = context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	release, err := second.AcquireDesktopControlLock(ctx, 9001)
	if err != nil {
		t.Fatalf("canceled acquisition left a session lock behind: %v", err)
	}
	release()
}

func TestControlLocksCannotStarveTheirOwnSQLWork(t *testing.T) {
	endpoint, err := url.Parse(testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	query := endpoint.Query()
	query.Set("pool_max_conns", "2")
	endpoint.RawQuery = query.Encode()
	db, err := Open(t.Context(), endpoint.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := 0; i < int(db.controlLocks.Config().MaxConns); i++ {
		release, err := db.AcquireDesktopControlLock(t.Context(), 9001+i)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := db.Ping(ctx); err != nil {
		t.Fatalf("desktop gates exhausted authorization SQL connections: %v", err)
	}
}

func TestAgentLeaseRevalidatesAssignmentAndHumanControl(t *testing.T) {
	db, connection, _ := guestRevocationFixture(t)
	ctx := t.Context()
	if _, err := db.CreateAgentPrincipal(ctx, AgentPrincipal{ID: "agent-a", DisplayName: "Agent"}, []byte("unused-agent-digest")); err != nil {
		t.Fatal(err)
	}
	lease := DesktopLease{ID: "lease_unassigned", AgentID: "agent-a", DesktopID: "9001", ExpiresAt: time.Now().Add(time.Hour)}
	if _, err := db.CreateDesktopLease(ctx, lease); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unassigned principal obtained a lease: %v", err)
	}
	if _, err := db.PutDesktopAssignment(ctx, DesktopAssignment{SubjectType: "agent", SubjectID: "agent-a", DesktopVMID: 9001}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateDesktopLease(ctx, lease); !errors.Is(err, ErrAlreadyLeased) {
		t.Fatalf("Agent took over an active human connection: %v", err)
	}
	closeGuestFixtureConnection(t, db, connection)
	for i, event := range []string{"agent_disabled", "assignment_removed", "inventory_removed"} {
		if _, err := db.SetAgentEnabled(ctx, "agent-a", true); err != nil {
			t.Fatal(err)
		}
		if _, err := db.PutDesktopAssignment(ctx, DesktopAssignment{SubjectType: "agent", SubjectID: "agent-a", DesktopVMID: 9001}); err != nil {
			t.Fatal(err)
		}
		lease.ID = fmt.Sprintf("lease_control_%d", i)
		issued, err := db.CreateDesktopLease(ctx, lease)
		if err != nil {
			t.Fatal(err)
		}
		switch event {
		case "agent_disabled":
			_, err = db.SetAgentEnabled(ctx, "agent-a", false)
		case "assignment_removed":
			_, err = db.DeleteDesktopAssignment(ctx, "agent", "agent-a", 9001)
		case "inventory_removed":
			err = db.ReconcileManagedDesktops(ctx, nil)
		}
		if err != nil {
			t.Fatal(err)
		}
		current, err := db.DesktopLeaseByID(ctx, issued.ID)
		if err != nil || current.State != "revoked" || current.ControlEpoch != issued.ControlEpoch+1 {
			t.Fatalf("%s did not revoke current lease: %v %v", event, current, err)
		}
		lease.ID += "_retry"
		if _, err := db.CreateDesktopLease(ctx, lease); !errors.Is(err, ErrNotFound) {
			t.Fatalf("stale preflight bypassed %s: %v", event, err)
		}
	}
	items, err := db.PendingComputerRevocations(ctx, 9001)
	if err != nil || len(items) != 1 || items[0].Revision != 3 {
		t.Fatalf("lease transitions were not durably queued: %v %v", items, err)
	}
	stale := items[0]
	stale.Revision--
	if err := db.CompleteComputerRevocation(ctx, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale worker acknowledged newer revocation: %v", err)
	}
	if err := db.CompleteComputerRevocation(ctx, items[0]); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteComputerRevocation(ctx, items[0]); !errors.Is(err, ErrConflict) {
		t.Fatalf("already completed revision was acknowledged again: %v", err)
	}
	page, err := db.AuditEvents(ctx, AuditEventQuery{Limit: 100, Search: "agent.computer_authority_revoked"})
	if err != nil || len(page.Events) != 1 || page.Events[0].ActorID != "" {
		t.Fatalf("expected one system completion audit: %v %v", page, err)
	}
}
