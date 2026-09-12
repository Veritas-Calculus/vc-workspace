package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestCloneRecoveryAtomicAuditAndConcurrentCAS(t *testing.T) {
	db, _, _ := nativeAccountFixture(t, "linux")
	if _, err := db.CreateLocalUser(t.Context(), User{ID: "recovery-admin", Username: "recovery-admin", DisplayName: "Admin", PasswordHash: "unused", Role: "platform_admin"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(t.Context(), `UPDATE users SET role='platform_admin' WHERE id='recovery-admin'`); err != nil {
		t.Fatal(err)
	}
	job, _, err := db.ReserveDesktopCloneJob(t.Context(), Job{ID: "recover", IdempotencyKey: "recover", Operation: "pve.template_clone", State: "accepted", SourceVMID: 9202, TargetVMID: 9203, TargetNode: "target", TaskNode: "source", Request: json.RawMessage(`{"name":"check","pve_principal":"admin@pve"}`)})
	if err != nil {
		t.Fatal(err)
	}
	const handle = "UPID:source:1:1:1:qmclone:9202:admin@pve:"
	for _, actor := range []string{"native-a", "missing"} {
		if err := db.CommitCloneRecovery(t.Context(), job, handle, actor, "verified"); !errors.Is(err, ErrConflict) {
			t.Fatalf("unauthorized: %v", err)
		}
	}
	if _, err := db.pool.Exec(t.Context(), `UPDATE users SET disabled=true WHERE id='recovery-admin'`); err != nil {
		t.Fatal(err)
	}
	if err := db.CommitCloneRecovery(t.Context(), job, handle, "recovery-admin", "verified"); !errors.Is(err, ErrConflict) {
		t.Fatalf("disabled admin: %v", err)
	}
	if _, err := db.pool.Exec(t.Context(), `UPDATE users SET disabled=false WHERE id='recovery-admin'`); err != nil {
		t.Fatal(err)
	}
	wrong := job
	wrong.Request = json.RawMessage(`{"name":"changed"}`)
	if err := db.CommitCloneRecovery(t.Context(), wrong, handle, "recovery-admin", "verified"); !errors.Is(err, ErrConflict) {
		t.Fatalf("snapshot mismatch: %v", err)
	}
	_, err = db.pool.Exec(t.Context(), `CREATE FUNCTION reject_clone_recovery_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_type='job.clone_recovered' THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_clone_recovery_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_clone_recovery_audit()`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CommitCloneRecovery(t.Context(), job, handle, "recovery-admin", "verified"); err == nil || !strings.Contains(err.Error(), "injected audit failure") {
		t.Fatalf("did not reach audit failure: %v", err)
	}
	saved, err := db.JobByID(t.Context(), job.ID)
	if err != nil || saved.State != "accepted" || saved.UPID != "" {
		t.Fatalf("partial commit: %+v %v", saved, err)
	}
	if _, err := db.pool.Exec(t.Context(), `DROP TRIGGER reject_clone_recovery_audit ON audit_events`); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 8)
	for i := range 8 {
		go func() {
			<-start
			results <- db.CommitCloneRecovery(t.Context(), job, fmt.Sprintf("UPID:source:%X:1:1:qmclone:9202:admin@pve:", i+1), "recovery-admin", "verified")
		}()
	}
	close(start)
	successes := 0
	for range 8 {
		err := <-results
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("winners=%d", successes)
	}
	saved, err = db.JobByID(t.Context(), job.ID)
	if err != nil || saved.State != "running" || saved.UPID == "" {
		t.Fatalf("missing recovered task: %+v %v", saved, err)
	}
	if err := db.CommitCloneRecovery(t.Context(), job, saved.UPID, "recovery-admin", "verified"); !errors.Is(err, ErrConflict) {
		t.Fatalf("replayed commit: %v", err)
	}
	var count int
	if err := db.pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE event_type='job.clone_recovered' AND target_id=$1`, job.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("audit count=%d %v", count, err)
	}
	var digest string
	if err := db.pool.QueryRow(t.Context(), `SELECT detail->>'task_handle_sha256' FROM audit_events WHERE event_type='job.clone_recovered' AND target_id=$1`, job.ID).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(saved.UPID))
	if digest != hex.EncodeToString(hash[:]) {
		t.Fatal("audit does not identify the winning handle")
	}
	var ready bool
	if err := db.pool.QueryRow(t.Context(), `SELECT desktop_clone_ready($1)`, job.TargetVMID).Scan(&ready); err != nil || ready {
		t.Fatalf("recovery opened access: %v %v", ready, err)
	}
}
