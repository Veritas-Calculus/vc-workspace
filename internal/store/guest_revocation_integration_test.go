package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/testdb"
)

func guestRevocationFixture(t *testing.T) (*Store, DesktopConnectionSession, []byte) {
	t.Helper()
	ctx := t.Context()
	db, err := Open(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateLocalUser(ctx, User{ID: "user-a", Username: "user-a", DisplayName: "A", PasswordHash: "old-hash"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertManagedDesktop(ctx, ManagedDesktop{VMID: 9001, DisplayName: "Test", Node: "test", OSFamily: "linux"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutDesktopAssignment(ctx, DesktopAssignment{SubjectType: "user", SubjectID: "user-a", DesktopVMID: 9001}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutGuestIdentityBinding(ctx, GuestIdentityBinding{DesktopVMID: 9001, UserID: "user-a", GuestUsername: "vcwtestuser", State: "ready"}); err != nil {
		t.Fatal(err)
	}
	digest := []byte("test-native-digest")
	if err := db.CreateNativeSession(ctx, digest, "user-a", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	connection, err := db.CreateNativeDesktopConnectionSession(ctx, DesktopConnectionSession{ID: "connection-a", UserID: "user-a", DesktopVMID: 9001, GuestUsername: "vcwtestuser", ExpiresAt: time.Now().Add(8 * time.Hour)}, digest)
	if err != nil {
		t.Fatal(err)
	}
	return db, connection, digest
}

func closeGuestFixtureConnection(t *testing.T, db *Store, connection DesktopConnectionSession) {
	t.Helper()
	if _, err := db.BeginRevokeDesktopConnection(t.Context(), connection.ID, connection.UserID); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkDesktopConnectionClosed(t.Context(), connection.ID, "revoked"); err != nil {
		t.Fatal(err)
	}
}

func pendingGuestFixture(t *testing.T, db *Store) GuestIdentityRevocation {
	t.Helper()
	items, err := db.PendingGuestIdentityRevocations(t.Context(), 9001)
	if err != nil || len(items) != 1 {
		t.Fatalf("expected one durable revocation: %v %v", items, err)
	}
	return items[0]
}

func TestRetainedGuestSessionRevokedAfterOrdinaryDisconnect(t *testing.T) {
	for _, event := range []string{"assignment-removed", "password-reset", "logout", "user-disabled", "expiry", "inventory-removed"} {
		t.Run(event, func(t *testing.T) {
			db, connection, digest := guestRevocationFixture(t)
			ctx := t.Context()
			closeGuestFixtureConnection(t, db, connection)
			if items, err := db.PendingGuestIdentityRevocations(ctx, 9001); err != nil || len(items) != 0 {
				t.Fatalf("ordinary disconnect must retain Guest: %v %v", items, err)
			}
			var err error
			switch event {
			case "assignment-removed":
				_, err = db.DeleteDesktopAssignment(ctx, "user", connection.UserID, 9001)
			case "password-reset":
				_, err = db.SetUserPassword(ctx, connection.UserID, "new-hash", nil)
			case "logout":
				err = db.RevokeNativeSession(ctx, digest)
			case "user-disabled":
				_, err = db.SetUserDisabled(ctx, connection.UserID, true)
			case "inventory-removed":
				err = db.ReconcileManagedDesktops(ctx, nil)
			case "expiry":
				_, err = db.pool.Exec(ctx, `UPDATE guest_identity_bindings SET session_expires_at=now()-interval '1 second'`)
				if err == nil {
					err = db.QueueExpiredGuestIdentities(ctx)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			item := pendingGuestFixture(t, db)
			if item.UserID != connection.UserID || item.GuestUsername != connection.GuestUsername {
				t.Fatal("incorrect revocation identity")
			}
			var bindingState string
			if err := db.pool.QueryRow(ctx, `SELECT state FROM guest_identity_bindings WHERE desktop_vmid=9001`).Scan(&bindingState); err != nil || bindingState != "disabled" {
				t.Fatalf("binding not disabled: %s %v", bindingState, err)
			}
			if event == "logout" || event == "password-reset" || event == "user-disabled" {
				if _, err := db.NativeSessionByToken(ctx, digest); !errors.Is(err, ErrNotFound) {
					t.Fatalf("native token survived: %v", err)
				}
			}
			connection.ID = "connection-b"
			if _, err := db.CreateNativeDesktopConnectionSession(ctx, connection, digest); !errors.Is(err, ErrNotFound) {
				t.Fatalf("pending cleanup issued connection: %v", err)
			}
			if err := db.QueueExpiredGuestIdentities(ctx); err != nil {
				t.Fatal(err)
			}
			if again := pendingGuestFixture(t, db); again.Revision != item.Revision {
				t.Fatal("unchanged disabled binding generated a new revocation")
			}
		})
	}
}

func TestGuestRevocationRevisionAndReconnect(t *testing.T) {
	db, connection, digest := guestRevocationFixture(t)
	ctx := t.Context()
	if _, err := db.SetUserPassword(ctx, connection.UserID, "new-hash", nil); err != nil {
		t.Fatal(err)
	}
	stale := pendingGuestFixture(t, db)
	if _, err := db.SetUserPassword(ctx, connection.UserID, "newer-hash", nil); err != nil {
		t.Fatal(err)
	}
	current := pendingGuestFixture(t, db)
	if current.Revision != stale.Revision+1 {
		t.Fatal("revocation revision not advanced")
	}
	if err := db.CompleteGuestIdentityRevocation(ctx, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("old worker acknowledged new event: %v", err)
	}
	if err := db.RecordGuestIdentityRevocationFailure(ctx, current); err != nil {
		t.Fatal(err)
	}
	if retry := pendingGuestFixture(t, db); retry.Revision != current.Revision {
		t.Fatal("failure lost queued event")
	}
	if err := db.WithDesktopConnectionLock(ctx, 9001, func() error { return db.CompleteGuestIdentityRevocation(ctx, current) }); err != nil {
		t.Fatal(err)
	}
	closed, err := db.DesktopConnectionByID(ctx, connection.ID)
	if err != nil || closed.State != "revoked" {
		t.Fatalf("connection not closed: %v %v", closed, err)
	}
	connection.ID = "connection-b"
	if _, err := db.CreateNativeDesktopConnectionSession(ctx, connection, digest); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale token issued after cleanup: %v", err)
	}
	newDigest := []byte("fresh-native-digest")
	if err := db.CreateNativeSession(ctx, newDigest, connection.UserID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutGuestIdentityBinding(ctx, GuestIdentityBinding{DesktopVMID: 9001, UserID: connection.UserID, GuestUsername: connection.GuestUsername, State: "ready"}); err != nil {
		t.Fatal(err)
	}
	if err := db.QueueExpiredGuestIdentities(ctx); err != nil {
		t.Fatal(err)
	}
	if items, err := db.PendingGuestIdentityRevocations(ctx, 9001); err != nil || len(items) != 0 {
		t.Fatalf("old deadline revoked the preparing account again: %v %v", items, err)
	}
	connection.ExpiresAt = time.Now().Add(9 * time.Hour)
	if _, err := db.CreateNativeDesktopConnectionSession(ctx, connection, newDigest); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteGuestIdentityRevocation(ctx, current); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale worker closed new connection: %v", err)
	}
	active, err := db.DesktopConnectionByID(ctx, connection.ID)
	if err != nil || active.State != "active" {
		t.Fatalf("new connection revoked: %v %v", active, err)
	}
	var expiry time.Time
	if err := db.pool.QueryRow(ctx, `SELECT session_expires_at FROM guest_identity_bindings WHERE desktop_vmid=9001`).Scan(&expiry); err != nil || expiry.Sub(connection.ExpiresAt).Abs() > time.Millisecond {
		t.Fatalf("retained session deadline not renewed: %v %v", expiry, err)
	}
}

func TestInFlightNativeIssuanceSerializedWithLogout(t *testing.T) {
	db, connection, digest := guestRevocationFixture(t)
	closeGuestFixtureConnection(t, db, connection)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	tx, err := db.beginAccessChange(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM native_sessions WHERE token_digest=$1`, digest); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	connection.ID = "connection-delayed"
	go func() { _, err := db.CreateNativeDesktopConnectionSession(ctx, connection, digest); result <- err }()
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, ErrNotFound) {
		t.Fatalf("request authenticated before logout issued a credential: %v", err)
	}
}

func TestVerifiedLoginCannotIssueAfterPasswordReset(t *testing.T) {
	db, connection, _ := guestRevocationFixture(t)
	ctx := t.Context()
	verified, err := db.UserByUsername(ctx, connection.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetUserPassword(ctx, verified.ID, "new-hash", nil); err != nil {
		t.Fatal(err)
	}
	for _, native := range []bool{false, true} {
		err := db.createVerifiedLoginSession(ctx, []byte("old-verified"), verified, "csrf", time.Now().Add(time.Hour), native)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("stale verification issued login, native=%v: %v", native, err)
		}
	}
	verified.PasswordHash = "new-hash"
	if err := db.CreateSessionForVerifiedUser(ctx, []byte("web-verified"), verified, "csrf", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateNativeSessionForVerifiedUser(ctx, []byte("native-verified"), verified, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetUserDisabled(ctx, verified.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateNativeSessionForVerifiedUser(ctx, []byte("disabled-verified"), verified, time.Now().Add(time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled account issued login: %v", err)
	}
}

func TestGuestRevocationUpgradeIncludesClosedConnections(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		t.Run(fmt.Sprint(revoked), func(t *testing.T) {
			db, connection, _ := guestRevocationFixture(t)
			ctx := t.Context()
			closeGuestFixtureConnection(t, db, connection)
			if revoked {
				if _, err := db.DeleteDesktopAssignment(ctx, "user", connection.UserID, 9001); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := db.pool.Exec(ctx, `UPDATE desktop_connection_sessions SET expires_at=now()-interval '1 hour'`); err != nil {
					t.Fatal(err)
				}
			}
			// Recreate the pre-022 shape inside this test's isolated schema only.
			if _, err := db.pool.Exec(ctx, `DROP TABLE guest_identity_revocations;
				ALTER TABLE guest_identity_bindings DROP COLUMN session_expires_at;
				DELETE FROM schema_migrations WHERE name='022_guest_identity_revocation_queue.sql'`); err != nil {
				t.Fatal(err)
			}
			if err := db.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			item := pendingGuestFixture(t, db)
			expected := "expired"
			if revoked {
				expected = "access_removed"
			}
			if item.Reason != expected {
				t.Fatalf("incorrect upgrade reason: %s", item.Reason)
			}
			if err := db.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			if again := pendingGuestFixture(t, db); again.Revision != item.Revision {
				t.Fatal("upgrade repeated cleanup")
			}
		})
	}
}

func TestExpiredGuestDeadlineClearedBeforeReprovisioning(t *testing.T) {
	db, connection, digest := guestRevocationFixture(t)
	ctx := t.Context()
	closeGuestFixtureConnection(t, db, connection)
	if _, err := db.pool.Exec(ctx, `UPDATE guest_identity_bindings SET session_expires_at=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueueExpiredGuestIdentitiesForDesktop(ctx, 9001); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteGuestIdentityRevocation(ctx, pendingGuestFixture(t, db)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutGuestIdentityBinding(ctx, GuestIdentityBinding{DesktopVMID: 9001, UserID: connection.UserID, GuestUsername: connection.GuestUsername, State: "provisioning"}); err != nil {
		t.Fatal(err)
	}
	if err := db.QueueExpiredGuestIdentities(ctx); err != nil {
		t.Fatal(err)
	}
	connection.ID = "renewed-connection"
	if _, err := db.CreateNativeDesktopConnectionSession(ctx, connection, digest); err != nil {
		t.Fatalf("old expiry made reconnect impossible: %v", err)
	}
}
