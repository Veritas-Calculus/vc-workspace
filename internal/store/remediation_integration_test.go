package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/testdb"
)

func TestConcurrentMigrationsAndPolicyUpgrade(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	u := testdb.URL(t)
	first, err := Open(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	var wg sync.WaitGroup
	for _, db := range []*Store{first, second} {
		wg.Add(1)
		go func(db *Store) {
			defer wg.Done()
			if err := db.Migrate(ctx); err != nil {
				t.Error(err)
			}
		}(db)
	}
	wg.Wait()
	if t.Failed() {
		return
	}
	if _, err := first.pool.Exec(ctx, `INSERT INTO managed_desktops(vmid,display_name,node) VALUES(9001,'Upgrade test','test');
		INSERT INTO desktop_access_policies(vmid,desired_revision,applied_revision,state)
		VALUES(9001,5,5,'applied');
		DELETE FROM schema_migrations WHERE name='019_reconcile_session_policy_upgrade.sql'`); err != nil {
		t.Fatal(err)
	}
	if err := first.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var desired, applied int
	var state string
	if err := first.pool.QueryRow(ctx, `SELECT desired_revision,applied_revision,state FROM desktop_access_policies WHERE vmid=9001`).Scan(&desired, &applied, &state); err != nil {
		t.Fatal(err)
	}
	if desired != 6 || applied != 5 || state != "pending" {
		t.Fatalf("upgrade falsely applied: %d/%d %s", desired, applied, state)
	}
	if err := second.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := first.pool.QueryRow(ctx, `SELECT desired_revision FROM desktop_access_policies WHERE vmid=9001`).Scan(&desired); err != nil {
		t.Fatal(err)
	}
	if desired != 6 {
		t.Fatal("migration is not one-shot")
	}
}

func TestAccessChangesQueueExistingCredentials(t *testing.T) {
	cases := []string{"personal-owner", "shared-user", "shared-group", "group-disabled", "member-removed", "user-disabled", "inventory-removed", "redundant-grant"}
	for _, kind := range cases {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			db, err := Open(ctx, testdb.URL(t))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := db.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := db.CreateLocalUser(ctx, User{ID: "user-a", Username: "user-a", DisplayName: "A", PasswordHash: "unused"}); err != nil {
				t.Fatal(err)
			}
			if err := db.UpsertManagedDesktop(ctx, ManagedDesktop{VMID: 9001, DisplayName: "Test", Node: "test", OSFamily: "linux"}); err != nil {
				t.Fatal(err)
			}
			if kind != "personal-owner" {
				if _, err := db.UpdateManagedDesktopIdentity(ctx, 9001, "shared", "", ""); err != nil {
					t.Fatal(err)
				}
			}
			groupGrant := strings.Contains(kind, "group") || kind == "member-removed" || kind == "redundant-grant"
			if groupGrant {
				if _, err := db.PutIdentityGroup(ctx, IdentityGroup{ID: "group-a", DisplayName: "A", Source: "local", Enabled: true}); err != nil {
					t.Fatal(err)
				}
				if _, err := db.PutIdentityGroupMembership(ctx, IdentityGroupMembership{GroupID: "group-a", UserID: "user-a", Source: "local"}); err != nil {
					t.Fatal(err)
				}
				if _, err := db.PutDesktopAssignment(ctx, DesktopAssignment{SubjectType: "group", SubjectID: "group-a", DesktopVMID: 9001}); err != nil {
					t.Fatal(err)
				}
			}
			if !groupGrant || kind == "redundant-grant" {
				if _, err := db.PutDesktopAssignment(ctx, DesktopAssignment{SubjectType: "user", SubjectID: "user-a", DesktopVMID: 9001}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.CreateDesktopConnectionSession(ctx, DesktopConnectionSession{ID: "connection-a", UserID: "user-a", DesktopVMID: 9001, GuestUsername: "vcwtestuser", ExpiresAt: time.Now().Add(8 * time.Hour)}); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "group-disabled":
				_, err = db.SetIdentityGroupEnabled(ctx, "group-a", false)
			case "member-removed":
				_, err = db.DeleteIdentityGroupMembership(ctx, "group-a", "user-a")
			case "shared-group":
				_, err = db.DeleteDesktopAssignment(ctx, "group", "group-a", 9001)
			case "user-disabled":
				_, err = db.SetUserDisabled(ctx, "user-a", true)
			case "inventory-removed":
				err = db.ReconcileManagedDesktops(ctx, nil)
			default:
				_, err = db.DeleteDesktopAssignment(ctx, "user", "user-a", 9001)
			}
			if err != nil {
				t.Fatal(err)
			}
			current, err := db.DesktopConnectionByID(ctx, "connection-a")
			if err != nil {
				t.Fatal(err)
			}
			if kind == "redundant-grant" {
				if current.State != "active" {
					t.Fatal("remaining group grant was ignored")
				}
				return
			}
			if current.State != "revoking" {
				t.Fatal("credential retained after revocation")
			}
			if required, err := db.DesktopConnectionTerminationRequired(ctx, current.ID); err != nil || !required {
				t.Fatalf("Guest logoff not queued: %v %v", required, err)
			}
			if _, err := db.CreateDesktopConnectionSession(ctx, DesktopConnectionSession{ID: "connection-b", UserID: "user-a", DesktopVMID: 9001, GuestUsername: "vcwtestuser", ExpiresAt: time.Now().Add(time.Hour)}); !errors.Is(err, ErrNotFound) {
				t.Fatalf("revoked user issued credential: %v", err)
			}
		})
	}
}

func TestConcurrentJobFingerprintConflict(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			_, _, err := db.CreateJob(ctx, Job{ID: fmt.Sprintf("job-%d", i), IdempotencyKey: "same-key", RequestFingerprint: fmt.Sprint(i), Operation: "test", State: "accepted", Request: []byte(`{}`)})
			results <- err
		}(i)
	}
	success, conflict := 0, 0
	for i := 0; i < 2; i++ {
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
		t.Fatalf("requests were not isolated: successes=%d conflicts=%d", success, conflict)
	}
}
