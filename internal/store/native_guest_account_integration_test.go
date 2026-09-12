package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/testdb"
	"github.com/jackc/pgx/v5/pgconn"
)

func nativeAccountFixture(t *testing.T, platform string) (*Store, NativeGuestAccount, []byte) {
	t.Helper()
	db, err := Open(t.Context(), testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	a, digest := populateNativeAccountFixture(t, db, platform)
	return db, a, digest
}

func populateNativeAccountFixture(t *testing.T, db *Store, platform string) (NativeGuestAccount, []byte) {
	t.Helper()
	if _, err := db.CreateLocalUser(t.Context(), User{ID: "native-a", Username: "native-a", DisplayName: "A", PasswordHash: "unused"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertManagedDesktop(t.Context(), ManagedDesktop{VMID: 9001, Node: "test", OSFamily: platform, DisplayName: "Test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutDesktopAssignment(t.Context(), DesktopAssignment{SubjectType: "user", SubjectID: "native-a", DesktopVMID: 9001}); err != nil {
		t.Fatal(err)
	}
	a := NativeGuestAccount{DesktopVMID: 9001, UserID: "native-a", GuestUsername: NativeGuestUsername("native-a"), OSFamily: platform, GuestUID: 1001}
	if platform == "windows" {
		a.GuestUID, a.GuestSID = 0, "S-1-5-21-1-2-3-1001"
	}
	if _, err := db.PutGuestIdentityBinding(t.Context(), GuestIdentityBinding{DesktopVMID: 9001, UserID: a.UserID, GuestUsername: a.GuestUsername, ProfileID: "managed-local-" + platform, State: "ready"}); err != nil {
		t.Fatal(err)
	}
	digest := []byte("native-account-test-digest")
	if err := db.CreateNativeSession(t.Context(), digest, a.UserID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	return a, digest
}

func completeNativeAccountFixture(t *testing.T, db *Store, a NativeGuestAccount) NativeGuestAccount {
	t.Helper()
	if err := db.CompleteNativeGuestAccountOperation(t.Context(), a); err != nil {
		t.Fatal("complete exact intent", err)
	}
	next, err := db.NativeGuestAccount(t.Context(), a.DesktopVMID, a.UserID)
	if err != nil || next.State != "applied" || next.Revision != a.Revision || next.Operation != a.Operation {
		t.Fatal("completion changed intent", next, err)
	}
	return next
}

func TestNativeGuestCredentialLifecyclePreservesAccountAndRetirementDeadline(t *testing.T) {
	for _, platform := range []string{"linux", "windows"} {
		t.Run(platform, func(t *testing.T) {
			db, identity, digest := nativeAccountFixture(t, platform)
			bound, err := db.BindNativeGuestAccount(t.Context(), identity, digest)
			if err != nil || bound.Revision != 0 || bound.State != "idle" {
				t.Fatal("initial binding", bound, err)
			}
			deadline := time.Now().Add(time.Hour).Truncate(time.Second)
			issued, err := db.BeginNativeGuestCredential(t.Context(), bound, "conn_native_first", deadline, digest)
			if err != nil || issued.Revision != 1 || issued.State != "pending" || issued.Operation != "issue" {
				t.Fatal("durable reservation", issued, err)
			}
			if _, err := db.BeginNativeGuestCredential(t.Context(), issued, "conn_native_other", deadline, digest); !errors.Is(err, ErrConflict) {
				t.Fatal("replaced unresolved issue", err)
			}
			issued = completeNativeAccountFixture(t, db, issued)
			retired, err := db.RetireNativeGuestCredential(t.Context(), issued)
			if err != nil || retired.Revision != 2 || retired.Operation != "retire" || retired.ConnectionID != issued.ConnectionID || !retired.ExpiresAt.Equal(deadline) {
				t.Fatal("retirement lost original OS deadline", retired, err)
			}
			retired = completeNativeAccountFixture(t, db, retired)
			next, err := db.BeginNativeGuestCredential(t.Context(), retired, "conn_native_second", deadline, digest)
			if err != nil || next.Revision != 3 || next.GuestUID != identity.GuestUID || next.GuestSID != identity.GuestSID {
				t.Fatal("reconnect lost bound identity", next, err)
			}
			// Lose the issue receipt: no reissue, commit a higher revoke instead.
			closed, err := db.RevokeNativeGuestAccount(t.Context(), next)
			if err != nil || closed.Revision != 4 || closed.ExpiresAt != nil || closed.Operation != "revoke" {
				t.Fatal("compensating revoke lost identity/version", closed, err)
			}
			if err := db.CompleteNativeGuestAccountOperation(t.Context(), next); !errors.Is(err, ErrConflict) {
				t.Fatal("late issue confirmation replaced revoke", err)
			}
			closed = completeNativeAccountFixture(t, db, closed)
			if _, err := db.BeginNativeGuestCredential(t.Context(), closed, "conn_native_first", deadline, digest); !errors.Is(err, ErrConflict) {
				t.Fatal("reused a historical connection ID", err)
			}
			last, err := db.BeginNativeGuestCredential(t.Context(), closed, "conn_native_third", deadline, digest)
			if err != nil || last.Revision != 5 {
				t.Fatal("rollback consumed revision or fresh connection failed", last, err)
			}
			if _, err := db.RetireNativeGuestCredential(t.Context(), issued); !errors.Is(err, ErrConflict) {
				t.Fatal("stale retirement affected fresh credentials", err)
			}
		})
	}
}

func TestNativeGuestBindingAndIssueCASRejectConcurrentReplacement(t *testing.T) {
	db, candidate, digest := nativeAccountFixture(t, "linux")
	other := candidate
	other.GuestUID++
	start := make(chan struct{})
	done := make(chan error, 2)
	for _, identity := range []NativeGuestAccount{candidate, other} {
		go func() { <-start; _, err := db.BindNativeGuestAccount(t.Context(), identity, digest); done <- err }()
	}
	close(start)
	checkNativeCAS(t, done)
	bound, err := db.NativeGuestAccount(t.Context(), candidate.DesktopVMID, candidate.UserID)
	if err != nil {
		t.Fatal(err)
	}
	start = make(chan struct{})
	for _, id := range []string{"conn_concurrent_a", "conn_concurrent_b"} {
		go func() {
			<-start
			_, err := db.BeginNativeGuestCredential(t.Context(), bound, id, time.Now().Add(time.Hour).Truncate(time.Second), digest)
			done <- err
		}()
	}
	close(start)
	checkNativeCAS(t, done)
}

func TestNativeGuestConnectionCommitRollsBackAcknowledgementWhenSessionInsertFails(t *testing.T) {
	db, identity, digest := nativeAccountFixture(t, "linux")
	bound, err := db.BindNativeGuestAccount(t.Context(), identity, digest)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := db.BeginNativeGuestCredential(t.Context(), bound, "conn_atomic_native", time.Now().Add(time.Hour).Truncate(time.Second), digest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateNativeDesktopConnectionSession(t.Context(), DesktopConnectionSession{ID: issued.ConnectionID,
		UserID: issued.UserID, DesktopVMID: issued.DesktopVMID, GuestUsername: issued.GuestUsername, ExpiresAt: *issued.ExpiresAt}, digest); err == nil {
		t.Fatal("legacy connection insertion bypassed the bound credential receipt")
	}
	if _, err := db.pool.Exec(t.Context(), `CREATE FUNCTION reject_native_connection_fixture() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN RAISE EXCEPTION 'disposable insertion fault'; END; $$;
 CREATE TRIGGER reject_native_connection_fixture BEFORE INSERT ON desktop_connection_sessions FOR EACH ROW EXECUTE FUNCTION reject_native_connection_fixture()`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CompleteNativeGuestConnection(t.Context(), issued); err == nil {
		t.Fatal("faulted connection insert succeeded")
	}
	current, err := db.NativeGuestAccount(t.Context(), issued.DesktopVMID, issued.UserID)
	if err != nil || current.State != "pending" || current.Revision != issued.Revision {
		t.Fatal("connection failure left applied credential without a connection", err)
	}
	if _, err := db.DesktopConnectionByID(t.Context(), issued.ConnectionID); !errors.Is(err, ErrNotFound) {
		t.Fatal("connection insertion was not rolled back", err)
	}
	if _, err := db.pool.Exec(t.Context(), `DROP TRIGGER reject_native_connection_fixture ON desktop_connection_sessions`); err != nil {
		t.Fatal(err)
	}
	connection, err := db.CompleteNativeGuestConnection(t.Context(), issued)
	if err != nil || connection.ID != issued.ConnectionID || connection.State != "active" || !connection.ExpiresAt.Equal(*issued.ExpiresAt) {
		t.Fatal("exact receipt and connection could not commit together", err)
	}
	if _, err := db.CompleteNativeGuestConnection(t.Context(), issued); !errors.Is(err, ErrConflict) {
		t.Fatal("stale commit reused a completed intent", err)
	}
}

func checkNativeCAS(t *testing.T, results <-chan error) {
	t.Helper()
	success, conflict := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			success++
		} else if errors.Is(err, ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("CAS did not retain one exact operation", success, conflict)
	}
}

func TestNativeGuestIssueRevalidatesTokenAccessProfileAndPlatform(t *testing.T) {
	for _, event := range []string{"logout", "disable-user", "remove-access", "expire-token", "profile-disabled", "platform-change"} {
		t.Run(event, func(t *testing.T) {
			db, candidate, digest := nativeAccountFixture(t, "linux")
			bound, err := db.BindNativeGuestAccount(t.Context(), candidate, digest)
			if err != nil {
				t.Fatal(err)
			}
			issued, err := db.BeginNativeGuestCredential(t.Context(), bound, "conn_authorization", time.Now().Add(time.Hour).Truncate(time.Second), digest)
			if err != nil {
				t.Fatal(err)
			}
			switch event {
			case "logout":
				err = db.RevokeNativeSession(t.Context(), digest)
			case "disable-user":
				_, err = db.SetUserDisabled(t.Context(), candidate.UserID, true)
			case "remove-access":
				_, err = db.DeleteDesktopAssignment(t.Context(), "user", candidate.UserID, 9001)
			case "expire-token":
				_, err = db.pool.Exec(t.Context(), `UPDATE native_sessions SET expires_at=now()-interval '1 second'`)
			case "profile-disabled":
				_, err = db.pool.Exec(t.Context(), `UPDATE identity_profiles SET enabled=false WHERE id='managed-local-linux'`)
			case "platform-change":
				_, err = db.pool.Exec(t.Context(), `UPDATE managed_desktops SET os_family='windows' WHERE vmid=9001`)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := db.CompleteNativeGuestAccountOperation(t.Context(), issued); !errors.Is(err, ErrConflict) {
				t.Fatal("stale authorization acknowledged credentials", err)
			}
			pending, err := db.NativeGuestAccountsNeedingRecovery(t.Context(), 100)
			if err != nil || len(pending) != 1 || pending[0].Revision != issued.Revision {
				t.Fatal("uncertain intent disappeared", pending, err)
			}
			closed, err := db.RevokeNativeGuestAccount(t.Context(), issued)
			if err != nil {
				t.Fatal("cleanup incorrectly required active authorization", err)
			}
			closed = completeNativeAccountFixture(t, db, closed)
			if _, err := db.BeginNativeGuestCredential(t.Context(), closed, "conn_unauthorized", time.Now().Add(time.Hour).Truncate(time.Second), digest); !errors.Is(err, ErrConflict) {
				t.Fatal("new issue bypassed authorization", err)
			}
		})
	}
}

func TestNativeGuestSQLCannotClearIdentityRegressOrSkipIntent(t *testing.T) {
	for _, platform := range []string{"linux", "windows"} {
		t.Run(platform, func(t *testing.T) {
			db, candidate, digest := nativeAccountFixture(t, platform)
			bound, err := db.BindNativeGuestAccount(t.Context(), candidate, digest)
			if err != nil {
				t.Fatal(err)
			}
			issued, err := db.BeginNativeGuestCredential(t.Context(), bound, "conn_sql_fenced", time.Now().Add(time.Hour).Truncate(time.Second), digest)
			if err != nil {
				t.Fatal(err)
			}
			for _, change := range []string{
				`guest_uid=0,guest_sid=''`, `guest_uid=1002`, `guest_sid='S-1-5-21-1-2-3-1002'`,
				`guest_username='vcw000000000000'`, `os_family='unknown'`, `revision=0`, `revision=3`,
				`connection_id='conn_wrong_subject'`, `expires_at=expires_at+interval '1 second'`,
				`revision=2,operation='retire',native_session_digest=NULL`,
			} {
				_, err := db.pool.Exec(t.Context(), `UPDATE native_guest_accounts SET `+change)
				var rejected *pgconn.PgError
				if !errors.As(err, &rejected) || rejected.Code != "23514" {
					t.Fatal("SQL bypassed identity/version guard", change, err)
				}
			}
			if _, err := db.pool.Exec(t.Context(), `UPDATE guest_identity_bindings SET guest_username='vcw000000000000'`); err == nil {
				t.Fatal("presentation binding redirected pinned identity")
			}
			_, err = db.pool.Exec(t.Context(), `INSERT INTO native_guest_accounts(desktop_vmid,user_id,guest_username,os_family,guest_uid,guest_sid,revision,operation,state,connection_id,expires_at)
 SELECT desktop_vmid,user_id,guest_username,os_family,guest_uid,guest_sid,1,'issue','applied','conn_forged_insert',expires_at FROM native_guest_accounts`)
			var rejected *pgconn.PgError
			if !errors.As(err, &rejected) || rejected.Code != "23514" {
				t.Fatal("insert bypassed initial unversioned binding", err)
			}
			issued = completeNativeAccountFixture(t, db, issued)
			if _, err := db.pool.Exec(t.Context(), `UPDATE native_guest_accounts SET state='pending'`); err == nil {
				t.Fatal("completed request became pending again")
			}
		})
	}
}

func TestNativeGuestMigrationDoesNotInventLegacyIdentityOrVersion(t *testing.T) {
	db, err := Open(t.Context(), testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if _, err := db.pool.Exec(t.Context(), `CREATE TABLE schema_migrations(name text PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") || entry.Name() >= "028_" {
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
	candidate, _ := populateNativeAccountFixture(t, db, "linux")
	// Model the actual pre-028 writer, not the current API which requires the
	// new tables. The migration must preserve this old row without adopting it.
	deadline := time.Now().Add(time.Hour).Truncate(time.Second)
	if _, err := db.pool.Exec(t.Context(), `INSERT INTO desktop_connection_sessions(id,user_id,desktop_vmid,guest_username,state,expires_at)
 VALUES('conn_legacy_original',$1,9001,$2,'active',$3)`, candidate.UserID, candidate.GuestUsername, deadline); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(t.Context(), `UPDATE guest_identity_bindings SET session_expires_at=$1 WHERE desktop_vmid=9001 AND user_id=$2`, deadline, candidate.UserID); err != nil {
		t.Fatal(err)
	}
	old, err := db.DesktopConnectionByID(t.Context(), "conn_legacy_original")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.Migrate(t.Context()); err != nil {
			t.Fatal(err)
		}
		if _, err := db.NativeGuestAccount(t.Context(), 9001, candidate.UserID); !errors.Is(err, ErrNotFound) {
			t.Fatal("migration inferred a legacy identity", err)
		}
		current, err := db.DesktopConnectionByID(t.Context(), old.ID)
		if err != nil || current.State != old.State || !current.ExpiresAt.Equal(old.ExpiresAt) || current.GuestUsername != old.GuestUsername {
			t.Fatal("additive migration pretended to drain the old Guest", current, err)
		}
	}
}

func nativeFixtureAgent(t *testing.T, db *Store) AgentGuestSession {
	t.Helper()
	if _, err := db.CreateAgentPrincipal(t.Context(), AgentPrincipal{ID: "agent-native-check", DisplayName: "Agent"}, []byte("unused-agent-digest")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutDesktopAssignment(t.Context(), DesktopAssignment{SubjectType: "agent", SubjectID: "agent-native-check", DesktopVMID: 9001}); err != nil {
		t.Fatal(err)
	}
	lease, err := db.CreateDesktopLease(t.Context(), DesktopLease{ID: "lease_native_check", AgentID: "agent-native-check", DesktopID: "9001", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	a, err := db.BeginAgentGuestSession(t.Context(), lease)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestNativeGuestIdentityCannotAliasAgentInEitherDirection(t *testing.T) {
	for _, platform := range []string{"linux", "windows"} {
		for _, nativeFirst := range []bool{true, false} {
			t.Run(platform+map[bool]string{true: "-native-first", false: "-agent-first"}[nativeFirst], func(t *testing.T) {
				db, candidate, digest := nativeAccountFixture(t, platform)
				agent := nativeFixtureAgent(t, db)
				agent.GuestUID, agent.GuestSID = candidate.GuestUID, candidate.GuestSID
				bindNative := func() error { _, err := db.BindNativeGuestAccount(t.Context(), candidate, digest); return err }
				bindAgent := func() error { return db.BindAgentGuestAccount(t.Context(), agent) }
				first, second := bindNative, bindAgent
				if !nativeFirst {
					first, second = second, first
				}
				if err := first(); err != nil {
					t.Fatal(err)
				}
				var conflict *pgconn.PgError
				if err := second(); !errors.As(err, &conflict) || conflict.Code != "23505" {
					t.Fatal("Agent/Native identities aliased", err)
				}
			})
		}
	}
}

func TestNativeGuestReservationAndAgentLeaseExcludeEachOther(t *testing.T) {
	db, candidate, digest := nativeAccountFixture(t, "linux")
	agent := nativeFixtureAgent(t, db)
	bound, err := db.BindNativeGuestAccount(t.Context(), candidate, digest)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Hour).Truncate(time.Second)
	if _, err := db.BeginNativeGuestCredential(t.Context(), bound, "conn_agent_conflict", deadline, digest); !errors.Is(err, ErrConflict) {
		t.Fatal("Native reserved an actively Agent-controlled desktop", err)
	}
	if _, err := db.ReleaseDesktopLease(t.Context(), agent.LeaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.BeginNativeGuestCredential(t.Context(), bound, "conn_agent_closing", deadline, digest); !errors.Is(err, ErrConflict) {
		t.Fatal("Native ignored pending Agent cleanup", err)
	}
	revokeAgentBinding(t, db, agent)
	issued, err := db.BeginNativeGuestCredential(t.Context(), bound, "conn_native_reserve", deadline, digest)
	if err != nil {
		t.Fatal(err)
	}
	for _, applied := range []bool{false, true} {
		if applied {
			issued = completeNativeAccountFixture(t, db, issued)
		}
		if _, err := db.CreateDesktopLease(t.Context(), DesktopLease{ID: "lease_competing_agent", AgentID: agent.AgentID, DesktopID: "9001", ExpiresAt: time.Now().Add(time.Hour)}); !errors.Is(err, ErrAlreadyLeased) {
			t.Fatal("Agent ignored Native reservation before connection insertion", err)
		}
	}
	retired, err := db.RetireNativeGuestCredential(t.Context(), issued)
	if err != nil {
		t.Fatal(err)
	}
	completeNativeAccountFixture(t, db, retired)
	if _, err := db.CreateDesktopLease(t.Context(), DesktopLease{ID: "lease_after_retire", AgentID: agent.AgentID, DesktopID: "9001", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal("settled retirement retained active control", err)
	}
}

func TestNativeGuestRecoveryIncludesExpiredRetainedDesktop(t *testing.T) {
	db, candidate, digest := nativeAccountFixture(t, "linux")
	bound, err := db.BindNativeGuestAccount(t.Context(), candidate, digest)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second).Truncate(time.Second)
	issued, err := db.BeginNativeGuestCredential(t.Context(), bound, "conn_expiry_retire", deadline, digest)
	if err != nil {
		t.Fatal(err)
	}
	issued = completeNativeAccountFixture(t, db, issued)
	retired, err := db.RetireNativeGuestCredential(t.Context(), issued)
	if err != nil {
		t.Fatal(err)
	}
	retired = completeNativeAccountFixture(t, db, retired)
	items, err := db.NativeGuestAccountsNeedingRecovery(t.Context(), 10)
	if err != nil || len(items) != 0 {
		t.Fatal("unexpired retained desktop is not due", items, err)
	}
	// Wait the originally persisted short deadline; do not rewrite the intent.
	time.Sleep(time.Until(deadline.Add(20 * time.Millisecond)))
	items, err = db.NativeGuestAccountsNeedingRecovery(t.Context(), 10)
	if err != nil || len(items) != 1 || items[0].Revision != retired.Revision || items[0].Operation != "retire" {
		t.Fatal("restart recovery lost expired retained desktop", items, err)
	}
	if _, err := db.BeginNativeGuestCredential(t.Context(), retired, "conn_expired_reopen", time.Now().Add(time.Hour).Truncate(time.Second), digest); !errors.Is(err, ErrConflict) {
		t.Fatal("expired retained desktop reopened without Guest revocation", err)
	}
	_, err = db.pool.Exec(t.Context(), `UPDATE native_guest_accounts SET revision=revision+1,operation='issue',state='pending',connection_id='conn_raw_expired_reopen',expires_at=now()+interval '1 hour',native_session_digest=$1`, digest)
	var invalid *pgconn.PgError
	if !errors.As(err, &invalid) || invalid.Code != "23514" {
		t.Fatal("raw SQL reopened expired retained desktop", err)
	}
	closed, err := db.RevokeNativeGuestAccount(t.Context(), retired)
	if err != nil {
		t.Fatal("expired account could not reserve revocation", err)
	}
	closed = completeNativeAccountFixture(t, db, closed)
	if _, err := db.BeginNativeGuestCredential(t.Context(), closed, "conn_after_expired_revoke", time.Now().Add(time.Hour).Truncate(time.Second), digest); err != nil {
		t.Fatal("verified revocation could not start a fresh window", err)
	}
	for _, limit := range []int{0, -1, 1001} {
		if _, err := db.NativeGuestAccountsNeedingRecovery(t.Context(), limit); !errors.Is(err, ErrConflict) {
			t.Fatal("unbounded recovery query", err)
		}
	}
}

func TestNativeGuestSharedDesktopSeparatesUsersAndReservesOneController(t *testing.T) {
	db, a, digestA := nativeAccountFixture(t, "linux")
	if _, err := db.pool.Exec(t.Context(), `UPDATE managed_desktops SET access_mode='shared',owner_user_id=NULL WHERE vmid=9001`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateLocalUser(t.Context(), User{ID: "native-b", Username: "native-b", DisplayName: "B", PasswordHash: "unused"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a.UserID, "native-b"} {
		if _, err := db.PutDesktopAssignment(t.Context(), DesktopAssignment{SubjectType: "user", SubjectID: id, DesktopVMID: 9001}); err != nil {
			t.Fatal(err)
		}
	}
	b := a
	b.UserID, b.GuestUsername = "native-b", NativeGuestUsername("native-b")
	if _, err := db.PutGuestIdentityBinding(t.Context(), GuestIdentityBinding{DesktopVMID: 9001, UserID: b.UserID, GuestUsername: b.GuestUsername, ProfileID: "managed-local-linux", State: "ready"}); err != nil {
		t.Fatal(err)
	}
	digestB := []byte("native-b-test-digest")
	if err := db.CreateNativeSession(t.Context(), digestB, b.UserID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	a, err := db.BindNativeGuestAccount(t.Context(), a, digestA)
	if err != nil {
		t.Fatal(err)
	}
	var duplicate *pgconn.PgError
	if _, err := db.BindNativeGuestAccount(t.Context(), b, digestB); !errors.As(err, &duplicate) || duplicate.Code != "23505" {
		t.Fatal("two Native users adopted one OS identity", err)
	}
	b.GuestUID++
	b, err = db.BindNativeGuestAccount(t.Context(), b, digestB)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, input := range []struct {
		a      NativeGuestAccount
		digest []byte
	}{{a, digestA}, {b, digestB}} {
		go func() {
			<-start
			_, err := db.BeginNativeGuestCredential(t.Context(), input.a, "conn_shared_"+input.a.UserID, time.Now().Add(time.Hour).Truncate(time.Second), input.digest)
			results <- err
		}()
	}
	close(start)
	checkNativeCAS(t, results)
	items, err := db.NativeGuestAccountsNeedingRecovery(t.Context(), 10)
	if err != nil || len(items) != 1 || items[0].Revision != 1 {
		t.Fatal("shared desktop produced multiple pending controllers", items, err)
	}
}

func TestNativeGuestInputValidationDoesNotReachDatabase(t *testing.T) {
	// Nil Store intentionally proves malformed input is rejected before I/O.
	var db *Store
	valid := NativeGuestAccount{DesktopVMID: 9001, UserID: "native-a", GuestUsername: NativeGuestUsername("native-a"), OSFamily: "linux", GuestUID: 1001}
	for _, mutate := range []func(*NativeGuestAccount){
		func(a *NativeGuestAccount) { a.GuestUID = 0 },
		func(a *NativeGuestAccount) { a.GuestSID = "S-1-5-18" },
		func(a *NativeGuestAccount) { a.GuestUsername = "vcw000000000000" },
		func(a *NativeGuestAccount) { a.GuestUsername = "vca000000000000" },
		func(a *NativeGuestAccount) { a.UserID = "" },
		func(a *NativeGuestAccount) { a.DesktopVMID = 0 },
	} {
		bad := valid
		mutate(&bad)
		if _, err := db.BindNativeGuestAccount(t.Context(), bad, []byte("test")); !errors.Is(err, ErrConflict) {
			t.Fatal("invalid identity accepted", err)
		}
	}
	for _, deadline := range []time.Time{time.Now().Add(-time.Second), time.Now().Add(9 * time.Hour), time.Now().Add(time.Hour).Truncate(time.Second).Add(time.Nanosecond)} {
		if _, err := db.BeginNativeGuestCredential(t.Context(), valid, "conn_validation", deadline, []byte("test")); !errors.Is(err, ErrConflict) {
			t.Fatal("invalid expiry accepted", err)
		}
	}
}
