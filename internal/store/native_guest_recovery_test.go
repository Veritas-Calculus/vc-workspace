package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestNativeRecoveryInterruptedWriteLeavesNoPartialState(t *testing.T) {
	for _, mode := range []string{"request_cancel", "backend_disconnect"} {
		t.Run(mode, func(t *testing.T) {
			db, a := closedNativeRecoveryFixture(t)
			// Sequence changes survive rollback, providing independent evidence
			// that interruption happened after the version/fence write, not
			// merely before the transaction started.
			_, err := db.pool.Exec(t.Context(), `CREATE SEQUENCE recovery_interrupt_pid;
 CREATE FUNCTION pause_recovery_test() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.revision=23 THEN
  PERFORM setval('recovery_interrupt_pid',pg_backend_pid());
  PERFORM pg_sleep(30);
 END IF;
 RETURN NEW; END $$;
 CREATE TRIGGER pause_recovery_test AFTER UPDATE ON native_guest_accounts FOR EACH ROW EXECUTE FUNCTION pause_recovery_test()`)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			result := make(chan error, 1)
			go func() {
				_, err := db.CommitNativeGuestRecovery(ctx, a, 23, "conn_recovery_interrupt", a.UserID, "interrupt-receipt", "Interrupted transaction test")
				result <- err
			}()
			var pid int
			for {
				var reached bool
				if err := db.pool.QueryRow(ctx, `SELECT last_value,is_called FROM recovery_interrupt_pid`).Scan(&pid, &reached); err != nil {
					t.Fatal(err)
				}
				if reached {
					break
				}
				select {
				case err := <-result:
					t.Fatalf("recovery ended before injection: %v", err)
				case <-ctx.Done():
					t.Fatal("write injection point not reached")
				case <-time.After(10 * time.Millisecond):
				}
			}
			if mode == "request_cancel" {
				cancel()
			} else {
				// Only terminate the backend PID published by our own isolated
				// trigger; never target the database server or another session.
				var terminated bool
				if err := db.pool.QueryRow(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE pid=$1 AND datname=current_database() AND query LIKE 'UPDATE native_guest_accounts SET revision=%'`, pid).Scan(&terminated); err != nil || !terminated {
					t.Fatalf("terminate exact recovery backend: %v %v", terminated, err)
				}
			}
			select {
			case err := <-result:
				if err == nil {
					t.Fatal("interrupted recovery succeeded")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("interrupted recovery did not return")
			}
			// A fresh query/DDL waits for server-side cancellation to finish.
			if _, err := db.pool.Exec(t.Context(), `DROP TRIGGER pause_recovery_test ON native_guest_accounts`); err != nil {
				t.Fatal(err)
			}
			var receipts, fences int
			var revision int64
			err = db.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM native_guest_recoveries),
 (SELECT count(*) FROM native_guest_credential_ids WHERE connection_id='conn_recovery_interrupt'),revision
 FROM native_guest_accounts WHERE desktop_vmid=$1 AND user_id=$2`, a.DesktopVMID, a.UserID).Scan(&receipts, &fences, &revision)
			if err != nil || receipts != 0 || fences != 0 || revision != a.Revision {
				t.Fatalf("partial interrupted state: receipts=%d fences=%d revision=%d error=%v", receipts, fences, revision, err)
			}
			if _, err := db.CommitNativeGuestRecovery(t.Context(), a, 23, "conn_recovery_interrupt", a.UserID, "interrupt-receipt", "Retry after independent closed-state inspection"); err != nil {
				t.Fatalf("retry after interruption: %v", err)
			}
		})
	}
}

func closedNativeRecoveryFixture(t *testing.T) (*Store, NativeGuestAccount) {
	t.Helper()
	db, identity, digest := nativeAccountFixture(t, "linux")
	a, err := db.BindNativeGuestAccount(t.Context(), identity, digest)
	if err != nil {
		t.Fatal(err)
	}
	a, err = db.BeginNativeGuestCredential(t.Context(), a, "conn_recovery_initial", time.Now().Add(time.Hour).Truncate(time.Second), digest)
	if err != nil {
		t.Fatal(err)
	}
	a = completeNativeAccountFixture(t, db, a)
	a, err = db.RevokeNativeGuestAccount(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	a = completeNativeAccountFixture(t, db, a)
	if _, err := db.pool.Exec(t.Context(), `UPDATE users SET role='platform_admin' WHERE id=$1`, a.UserID); err != nil {
		t.Fatal(err)
	}
	return db, a
}

func TestNativeRecoveryConcurrentCASHasOneReceipt(t *testing.T) {
	db, a := closedNativeRecoveryFixture(t)
	const attempts = 8
	start := make(chan struct{})
	results := make(chan error, attempts)
	for i := range attempts {
		go func() {
			<-start
			_, err := db.CommitNativeGuestRecovery(t.Context(), a, 23, "conn_recovery_race", a.UserID, fmt.Sprintf("race-%d", i), "Concurrent recovery test")
			results <- err
		}()
	}
	close(start)
	successes := 0
	for range attempts {
		err := <-results
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrConflict) {
			t.Errorf("unexpected recovery error: %v", err)
		}
	}
	var receipts, accounts int
	err := db.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM native_guest_recoveries), (SELECT count(*) FROM native_guest_accounts WHERE revision=23 AND operation='revoke' AND state='applied')`).Scan(&receipts, &accounts)
	if err != nil || successes != 1 || receipts != 1 || accounts != 1 {
		t.Fatalf("concurrent CAS: successes=%d receipts=%d accounts=%d error=%v", successes, receipts, accounts, err)
	}
}

func TestNativeRecoveryWriteFailureRollsBackReceiptAndCredentialFence(t *testing.T) {
	db, a := closedNativeRecoveryFixture(t)
	// Fail after the version trigger has inserted the new credential fence.
	// The receipt, account change and fence must all roll back together.
	_, err := db.pool.Exec(t.Context(), `CREATE FUNCTION reject_recovery_test() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.revision=23 THEN RAISE EXCEPTION 'injected recovery write failure'; END IF;
 RETURN NEW; END $$;
 CREATE TRIGGER reject_recovery_test AFTER UPDATE ON native_guest_accounts FOR EACH ROW EXECUTE FUNCTION reject_recovery_test()`)
	if err != nil {
		t.Fatal(err)
	}
	commit := func() error {
		_, err := db.CommitNativeGuestRecovery(t.Context(), a, 23, "conn_recovery_retry", a.UserID, "recovery-retry", "Interrupted write test")
		return err
	}
	if err := commit(); err == nil {
		t.Fatal("injected write failure was ignored")
	}
	var receipts int
	var revision int64
	err = db.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM native_guest_recoveries),revision FROM native_guest_accounts WHERE desktop_vmid=$1 AND user_id=$2`, a.DesktopVMID, a.UserID).Scan(&receipts, &revision)
	if err != nil || receipts != 0 || revision != a.Revision {
		t.Fatalf("partial commit: receipts=%d revision=%d error=%v", receipts, revision, err)
	}
	if _, err := db.pool.Exec(t.Context(), `DROP TRIGGER reject_recovery_test ON native_guest_accounts`); err != nil {
		t.Fatal(err)
	}
	// Reuse the exact receipt and connection identifiers: a leaked fence or
	// receipt would reject this retry even though the account remained old.
	if err := commit(); err != nil {
		t.Fatalf("rolled-back recovery cannot retry: %v", err)
	}
}

func TestNativeRecoveryAtomicReceiptAndForwardOnlyVersion(t *testing.T) {
	db, identity, digest := nativeAccountFixture(t, "linux")
	a, err := db.BindNativeGuestAccount(t.Context(), identity, digest)
	if err != nil {
		t.Fatal(err)
	}
	a, err = db.BeginNativeGuestCredential(t.Context(), a, "conn_recovery_first", time.Now().Add(time.Hour).Truncate(time.Second), digest)
	if err != nil {
		t.Fatal(err)
	}
	a = completeNativeAccountFixture(t, db, a)
	a, err = db.RevokeNativeGuestAccount(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	a = completeNativeAccountFixture(t, db, a)
	if _, err := db.pool.Exec(t.Context(), `UPDATE native_guest_accounts SET revision=23,connection_id='conn_guest_recovered' WHERE desktop_vmid=9001`); err == nil {
		t.Fatal("unreceipted jump accepted")
	}
	commit := func(expected NativeGuestAccount, id string) (NativeGuestAccount, error) {
		return db.CommitNativeGuestRecovery(t.Context(), expected, 23, "conn_guest_recovered", identity.UserID, id, "Restore isolated checkpoint after verified Guest inspection")
	}
	if _, err := commit(a, "unauthorized"); !errors.Is(err, ErrConflict) {
		t.Fatalf("user recovery: %v", err)
	}
	if _, err := db.pool.Exec(t.Context(), `UPDATE users SET role='platform_admin' WHERE id=$1`, identity.UserID); err != nil {
		t.Fatal(err)
	}
	stale := a
	stale.Revision--
	if _, err := commit(stale, "stale"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale recovery: %v", err)
	}
	var count int
	if err := db.pool.QueryRow(t.Context(), `SELECT count(*) FROM native_guest_recoveries`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed CAS retained receipt: %d %v", count, err)
	}
	updated, err := commit(a, "valid")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 23 || updated.Operation != "revoke" || updated.State != "applied" || updated.GuestUID != a.GuestUID || updated.ConnectionID != "conn_guest_recovered" {
		t.Fatalf("bad recovered state: %+v", updated)
	}
	if _, err := commit(a, "repeat"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale retry accepted: %v", err)
	}
	if err := db.pool.QueryRow(t.Context(), `SELECT count(*) FROM native_guest_recoveries WHERE id='valid' AND previous_revision=$1 AND recovered_revision=23`, a.Revision).Scan(&count); err != nil || count != 1 {
		t.Fatalf("missing receipt: %d %v", count, err)
	}
	for _, query := range []string{`UPDATE native_guest_recoveries SET reason='changed'`, `DELETE FROM native_guest_recoveries`} {
		if _, err := db.pool.Exec(t.Context(), query); err == nil {
			t.Fatal("receipt mutation accepted")
		}
	}
	if _, err := db.pool.Exec(t.Context(), `UPDATE native_guest_accounts SET revision=22 WHERE desktop_vmid=9001`); err == nil {
		t.Fatal("version rollback accepted")
	}
	next, err := db.BeginNativeGuestCredential(t.Context(), updated, "conn_recovery_next", time.Now().Add(time.Hour).Truncate(time.Second), digest)
	if err != nil || next.Revision != 24 {
		t.Fatalf("new issue after recovery: %+v %v", next, err)
	}
}
