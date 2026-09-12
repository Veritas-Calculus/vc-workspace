package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/guestidentity"
	"github.com/Veritas-Calculus/vc-workspace/internal/testdb"
	"github.com/jackc/pgx/v5/pgconn"
)

func readyAgentBinding(item AgentGuestSession) AgentGuestSession {
	item.InstanceID = strings.Repeat("a", 64)
	if item.OSFamily == "windows" {
		item.GuestUID, item.GuestSID, item.SessionID = 0, "S-1-5-21-1-2-3-1001", "windows:2:000000000001a2b3"
	} else {
		item.GuestUID, item.GuestSID, item.SessionID = 1001, "", "linux:1001:123::10.0"
	}
	return item
}

func reserveAgentBinding(t *testing.T, db *Store, item AgentGuestSession) AgentGuestSession {
	t.Helper()
	if err := db.BindAgentGuestAccount(t.Context(), item); err != nil {
		t.Fatal("pre-login identity binding", err)
	}
	next, err := db.BeginAgentGuestLogin(t.Context(), item)
	if err != nil {
		t.Fatal("reserve login generation", err)
	}
	next.SessionID, next.InstanceID = item.SessionID, item.InstanceID
	return next
}

func revokeAgentBinding(t *testing.T, db *Store, item AgentGuestSession) {
	t.Helper()
	if err := db.QueueAgentGuestSessionRevocation(t.Context(), item); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteAgentGuestSessionRevocation(t.Context(), item); err != nil {
		t.Fatal(err)
	}
	pending, err := db.PendingComputerRevocations(t.Context(), item.DesktopVMID)
	if err != nil || len(pending) != 1 {
		t.Fatal("lost cleanup queue", err)
	}
	if err := db.CompleteComputerRevocation(t.Context(), pending[0]); err != nil {
		t.Fatal(err)
	}
}

func TestAgentAccountBindingPersistsAcrossLeaseGenerations(t *testing.T) {
	for _, platform := range []string{"linux", "windows"} {
		t.Run(platform, func(t *testing.T) {
			db, lease := agentSessionFixture(t)
			if _, err := db.pool.Exec(t.Context(), `UPDATE managed_desktops SET os_family=$1 WHERE vmid=9001`, platform); err != nil {
				t.Fatal(err)
			}
			item, err := db.BeginAgentGuestSession(t.Context(), lease)
			if err != nil || item.OSFamily != platform {
				t.Fatal("platform was not pinned", err)
			}
			item = readyAgentBinding(item)
			item = reserveAgentBinding(t, db, item)
			if err := db.MarkAgentGuestSessionReady(t.Context(), item); err != nil {
				t.Fatal(err)
			}
			for name, change := range map[string]func(*AgentGuestSession){
				"account-replaced": func(v *AgentGuestSession) {
					if platform == "windows" {
						v.GuestSID = "S-1-5-21-1-2-3-1002"
					} else {
						v.GuestUID = 1002
						v.SessionID = "linux:1002:123::10"
					}
				},
				"platform":         func(v *AgentGuestSession) { v.OSFamily = "unknown" },
				"session-mismatch": func(v *AgentGuestSession) { v.SessionID = "windows:0:0000000000000000" },
				"subject":          func(v *AgentGuestSession) { v.GuestUsername = "vca000000000000" },
				"epoch":            func(v *AgentGuestSession) { v.ControlEpoch++ },
				"generation":       func(v *AgentGuestSession) { v.Generation++ },
			} {
				t.Run(name, func(t *testing.T) {
					bad := item
					change(&bad)
					if err := db.MarkAgentGuestSessionReady(t.Context(), bad); !errors.Is(err, ErrConflict) {
						t.Fatal("accepted stale/replaced identity", err)
					}
				})
			}
			// Helper restart is not account replacement; its instance can change.
			item.InstanceID = strings.Repeat("b", 64)
			if err := db.MarkAgentGuestSessionReady(t.Context(), item); err != nil {
				t.Fatal(err)
			}
			revokeAgentBinding(t, db, item)
			rows, err := db.AgentGuestSessions(t.Context(), 9001)
			if err != nil || len(rows) != 1 || rows[0].State != "disabled" || rows[0].GuestUID != item.GuestUID || rows[0].GuestSID != item.GuestSID || rows[0].SessionID != "" || rows[0].InstanceID != "" {
				t.Fatal("revocation lost durable account or retained live session", rows, err)
			}
			lease, err = db.CreateDesktopLease(t.Context(), DesktopLease{ID: "lease_binding_reacquired", AgentID: item.AgentID, DesktopID: "9001", ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			fresh, err := db.BeginAgentGuestSession(t.Context(), lease)
			if err != nil || fresh.Generation != item.Generation+1 || fresh.GuestSID != item.GuestSID || fresh.GuestUID != item.GuestUID {
				t.Fatal("reacquire forgot account", fresh, err)
			}
			fresh = readyAgentBinding(fresh)
			fresh = reserveAgentBinding(t, db, fresh)
			if platform == "windows" {
				fresh.SessionID = "windows:2:000000000001a2b4"
			}
			if err := db.MarkAgentGuestSessionReady(t.Context(), fresh); err != nil {
				t.Fatal("new logon incarnation rejected", err)
			}
			if err := db.MarkAgentGuestSessionReady(t.Context(), item); !errors.Is(err, ErrConflict) {
				t.Fatal("old lease became ready", err)
			}
		})
	}
}

func TestAgentAccountBindingCannotBeClearedReassignedOrAliasedInSQL(t *testing.T) {
	for _, platform := range []string{"linux", "windows"} {
		t.Run(platform, func(t *testing.T) {
			db, lease := agentSessionFixture(t)
			if _, err := db.pool.Exec(t.Context(), `UPDATE managed_desktops SET os_family=$1 WHERE vmid=9001`, platform); err != nil {
				t.Fatal(err)
			}
			item, err := db.BeginAgentGuestSession(t.Context(), lease)
			if err != nil {
				t.Fatal(err)
			}
			item = readyAgentBinding(item)
			item = reserveAgentBinding(t, db, item)
			if err := db.MarkAgentGuestSessionReady(t.Context(), item); err != nil {
				t.Fatal(err)
			}
			revokeAgentBinding(t, db, item)
			for _, update := range []string{
				`guest_uid=0,guest_sid=''`, `guest_uid=1002`, `guest_sid='S-1-5-21-1-2-3-1002'`,
				`guest_username='vca000000000000'`, `os_family=CASE os_family WHEN 'linux' THEN 'windows' ELSE 'linux' END`,
			} {
				_, err := db.pool.Exec(t.Context(), `UPDATE agent_guest_sessions SET `+update+` WHERE desktop_vmid=9001`)
				var rejected *pgconn.PgError
				if !errors.As(err, &rejected) || rejected.Code != "23514" {
					t.Fatal("database accepted identity overwrite", update, err)
				}
			}
			// A second Agent cannot adopt the pinned SID/UID after revocation.
			if _, err := db.CreateAgentPrincipal(t.Context(), AgentPrincipal{ID: "agent-b", DisplayName: "B"}, []byte("test-agent-b")); err != nil {
				t.Fatal(err)
			}
			if _, err := db.PutDesktopAssignment(t.Context(), DesktopAssignment{SubjectType: "agent", SubjectID: "agent-b", DesktopVMID: 9001}); err != nil {
				t.Fatal(err)
			}
			otherLease, err := db.CreateDesktopLease(t.Context(), DesktopLease{ID: "lease_other_agent", AgentID: "agent-b", DesktopID: "9001", ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			other, err := db.BeginAgentGuestSession(t.Context(), otherLease)
			if err != nil {
				t.Fatal(err)
			}
			other = readyAgentBinding(other)
			var rejected *pgconn.PgError
			if err := db.BindAgentGuestAccount(t.Context(), other); !errors.As(err, &rejected) || rejected.Code != "23505" {
				t.Fatal("two agents bound same OS account", err)
			}
		})
	}
}

func TestAgentAccountPlatformChangesCannotReinterpretBindings(t *testing.T) {
	db, lease := agentSessionFixture(t)
	item, err := db.BeginAgentGuestSession(t.Context(), lease)
	if err != nil {
		t.Fatal(err)
	}
	item = readyAgentBinding(item)
	item = reserveAgentBinding(t, db, item)
	for _, platform := range []string{"unknown", "windows"} {
		if _, err := db.pool.Exec(t.Context(), `UPDATE managed_desktops SET os_family=$1 WHERE vmid=9001`, platform); err != nil {
			t.Fatal(err)
		}
		if _, err := db.BeginAgentGuestSession(t.Context(), lease); !errors.Is(err, ErrConflict) {
			t.Fatal("reinterpreted platform", err)
		}
		if err := db.MarkAgentGuestSessionReady(t.Context(), item); !errors.Is(err, ErrConflict) {
			t.Fatal("platform switch published ready", err)
		}
	}
}

func TestSQLSIDValidationMatchesProtocol(t *testing.T) {
	db, _ := agentSessionFixture(t)
	for _, sid := range []string{
		"S-1-5-21-1-2-3-1000", "S-1-5-21-4294967295-0-1-4294967295",
		"", "S-1-5-18", "S-1-5-32-544", "S-1-5-21-1-2-3-500", "S-1-5-21-1-2-3-999",
		"S-1-5-21-01-2-3-1001", "S-1-5-21-1-2-3-+1001", "S-1-5-21-1-2-3-4294967296",
		"S-1-5-21-4294967296-2-3-1001", "S-1-5-21-1-2-3-1001\n", "S-1-5-21-1-2-3-1001)(A;;GA;;;WD)",
	} {
		var accepted bool
		if err := db.pool.QueryRow(t.Context(), `SELECT vcw_valid_windows_account_sid($1)`, sid).Scan(&accepted); err != nil {
			t.Fatal(err)
		}
		if accepted != guestidentity.ValidWindowsAccountSID(sid) {
			t.Fatalf("SQL/protocol SID disagreement %q: %v", sid, accepted)
		}
	}
}

func TestAgentAccountBindingConcurrentFirstDiscoveryPinsOneIdentity(t *testing.T) {
	db, lease := agentSessionFixture(t)
	item, err := db.BeginAgentGuestSession(t.Context(), lease)
	if err != nil {
		t.Fatal(err)
	}
	one := readyAgentBinding(item)
	two := one
	two.GuestUID, two.SessionID = 1002, "linux:1002:234::11"
	start := make(chan struct{})
	done := make(chan error, 2)
	for _, value := range []AgentGuestSession{one, two} {
		go func() { <-start; done <- db.BindAgentGuestAccount(t.Context(), value) }()
	}
	close(start)
	success, conflict := 0, 0
	for range 2 {
		err := <-done
		if err == nil {
			success++
		} else if errors.Is(err, ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("concurrent identities overwrote each other", success, conflict)
	}
}

func TestAgentAccountBindingMigrationPreservesIdentityAndQueuesUnversionedLogin(t *testing.T) {
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
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") || entry.Name() >= "026_" {
			continue
		}
		raw, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.pool.Exec(t.Context(), string(raw)); err != nil {
			t.Fatal(entry.Name(), err)
		}
		if _, err := db.pool.Exec(t.Context(), `INSERT INTO schema_migrations(name) VALUES($1)`, entry.Name()); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.UpsertManagedDesktop(t.Context(), ManagedDesktop{VMID: 9001, Node: "test", OSFamily: "linux", DisplayName: "Pre-026"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateAgentPrincipal(t.Context(), AgentPrincipal{ID: "agent-a", DisplayName: "A"}, []byte("unused")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutDesktopAssignment(t.Context(), DesktopAssignment{SubjectType: "agent", SubjectID: "agent-a", DesktopVMID: 9001}); err != nil {
		t.Fatal(err)
	}
	lease, err := db.CreateDesktopLease(t.Context(), DesktopLease{ID: "lease_pre026", AgentID: "agent-a", DesktopID: "9001", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(t.Context(), `INSERT INTO agent_guest_sessions(desktop_vmid,agent_id,guest_username,lease_id,control_epoch,expires_at,state,guest_uid,session_id,instance_id)
	VALUES(9001,$1,$2,$3,$4,$5,'ready',1001,'linux:1001:123::10.0',$6)`, lease.AgentID, AgentGuestUsername(lease.AgentID), lease.ID, lease.ControlEpoch, lease.ExpiresAt, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.Migrate(t.Context()); err != nil {
			t.Fatal(err)
		}
		rows, err := db.AgentGuestSessions(t.Context(), 9001)
		if err != nil || len(rows) != 1 {
			t.Fatal(rows, err)
		}
		row := rows[0]
		if row.State != "revoking" || row.LoginGeneration != 0 || row.OSFamily != "linux" || row.GuestUID != 1001 || row.GuestSID != "" || row.Generation != 1 || row.SessionID != "linux:1001:123::10.0" || row.ControlEpoch != lease.ControlEpoch {
			t.Fatal("migration changed existing identity", row)
		}
		if err := db.MarkAgentGuestSessionReady(t.Context(), row); !errors.Is(err, ErrConflict) {
			t.Fatal("unversioned login reused after migration", err)
		}
		pending, err := db.PendingComputerRevocations(t.Context(), 9001)
		closing, epochErr := db.ComputerRevocationEpoch(t.Context(), 9001)
		if err != nil || len(pending) != 1 || epochErr != nil || closing <= lease.ControlEpoch {
			t.Fatal("migration lost closing epoch", err, pending)
		}
	}
}
