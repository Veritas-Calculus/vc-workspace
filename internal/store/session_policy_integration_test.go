package store

import (
	"context"
	"strconv"
	"testing"

	"github.com/Veritas-Calculus/vc-workspace/internal/testdb"
	"time"
)

func TestDesktopSessionPolicyRevisionLifecycleIntegration(t *testing.T) {
	databaseURL := testdb.URL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	database, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	userID := "policy-user-" + suffix
	vmid := 600000000 + int(time.Now().UnixNano()%100000000)
	defer func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = database.pool.Exec(cleanup, `DELETE FROM managed_desktops WHERE vmid=$1`, vmid)
		_, _ = database.pool.Exec(cleanup, `DELETE FROM users WHERE id=$1`, userID)
	}()

	if _, err := database.CreateLocalUser(ctx, User{ID: userID, Username: userID, DisplayName: "Policy User", PasswordHash: "not-used"}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertManagedDesktop(ctx, ManagedDesktop{VMID: vmid, DisplayName: "Policy desktop", Node: "node-test", OSFamily: "linux"}); err != nil {
		t.Fatal(err)
	}
	initial, err := database.EnsureDesktopAccessPolicy(ctx, vmid)
	if err != nil {
		t.Fatal(err)
	}
	if initial.DesiredRevision != 1 || initial.AppliedRevision != 0 || !initial.ClipboardRedirection || initial.DriveRedirection || !initial.ManagedBackground {
		t.Fatalf("unexpected secure-compatible defaults: %#v", initial)
	}
	unchanged, err := database.PutDesktopAccessPolicy(ctx, vmid, "standard", true, false, true, userID)
	if err != nil || unchanged.DesiredRevision != 1 {
		t.Fatalf("an unchanged policy must keep its revision: policy=%#v err=%v", unchanged, err)
	}
	applied, err := database.MarkDesktopAccessPolicyApplied(ctx, vmid, unchanged.DesiredRevision, "linux")
	if err != nil || applied.State != "applied" || applied.AppliedRevision != 1 {
		t.Fatalf("apply initial policy: policy=%#v err=%v", applied, err)
	}
	changed, err := database.PutDesktopAccessPolicy(ctx, vmid, "standard", false, false, true, userID)
	if err != nil || changed.DesiredRevision != 2 || changed.AppliedRevision != 1 || changed.State != "pending" {
		t.Fatalf("a control change must increment the desired revision: policy=%#v err=%v", changed, err)
	}
	if changed.ClipboardRedirection {
		t.Fatal("clipboard denial was not persisted")
	}
}
