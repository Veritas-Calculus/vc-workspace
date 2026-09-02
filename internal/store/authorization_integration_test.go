package store

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestAuthorizationLifecycleIntegration(t *testing.T) {
	databaseURL := os.Getenv("VC_VDI_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("VC_VDI_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	userID := "test-user-" + suffix
	agentID := "test-agent-" + suffix
	leaseID := "test-lease-" + suffix
	vmid := 800000000 + int(time.Now().UnixNano()%100000000)
	defer func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = store.pool.Exec(cleanup, `DELETE FROM desktop_leases WHERE id=$1`, leaseID)
		_, _ = store.pool.Exec(cleanup, `DELETE FROM agent_principals WHERE id=$1`, agentID)
		_, _ = store.pool.Exec(cleanup, `DELETE FROM users WHERE id=$1`, userID)
		_, _ = store.pool.Exec(cleanup, `DELETE FROM managed_desktops WHERE vmid=$1`, vmid)
	}()

	user, err := store.CreateLocalUser(ctx, User{ID: userID, Username: userID, DisplayName: "Test User", PasswordHash: "not-used"})
	if err != nil || user.IdentityKind != "local" || user.Role != "user" {
		t.Fatalf("create local user: user=%#v err=%v", user, err)
	}
	if err := store.UpsertManagedDesktop(ctx, ManagedDesktop{VMID: vmid, DisplayName: "Test Desktop", Node: "node-test"}); err != nil {
		t.Fatal(err)
	}
	agent, err := store.CreateAgentPrincipal(ctx, AgentPrincipal{ID: agentID, DisplayName: "Test Agent", CreatedBy: userID}, []byte("first-token-digest-"+suffix))
	if err != nil || !agent.Enabled {
		t.Fatalf("create agent: agent=%#v err=%v", agent, err)
	}

	for _, assignment := range []DesktopAssignment{
		{SubjectType: "user", SubjectID: userID, DesktopVMID: vmid, CreatedBy: userID},
		{SubjectType: "agent", SubjectID: agentID, DesktopVMID: vmid, CreatedBy: userID},
	} {
		created, err := store.PutDesktopAssignment(ctx, assignment)
		if err != nil || !created {
			t.Fatalf("put assignment %#v: created=%v err=%v", assignment, created, err)
		}
	}
	if allowed, err := store.UserCanAccessDesktop(ctx, userID, vmid); err != nil || !allowed {
		t.Fatalf("user access: allowed=%v err=%v", allowed, err)
	}
	nativeDigest := []byte("native-token-digest-" + suffix)
	if err := store.CreateNativeSession(ctx, nativeDigest, userID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	disabledUser, err := store.SetUserDisabled(ctx, userID, true)
	if err != nil || !disabledUser.Disabled {
		t.Fatalf("disable user: user=%#v err=%v", disabledUser, err)
	}
	if _, err := store.NativeSessionByToken(ctx, nativeDigest); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled user retained native session: %v", err)
	}
	if allowed, err := store.UserCanAccessDesktop(ctx, userID, vmid); err != nil || allowed {
		t.Fatalf("disabled user retained access: allowed=%v err=%v", allowed, err)
	}
	if _, err := store.SetUserDisabled(ctx, userID, false); err != nil {
		t.Fatal(err)
	}
	if allowed, err := store.AgentCanAccessDesktop(ctx, agentID, vmid); err != nil || !allowed {
		t.Fatalf("agent access: allowed=%v err=%v", allowed, err)
	}

	lease, err := store.CreateDesktopLease(ctx, DesktopLease{ID: leaseID, AgentID: agentID, DesktopID: strconv.Itoa(vmid), ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil || lease.State != "active" {
		t.Fatalf("create lease: lease=%#v err=%v", lease, err)
	}
	deleted, err := store.DeleteDesktopAssignment(ctx, "agent", agentID, vmid)
	if err != nil || !deleted {
		t.Fatalf("delete assignment: deleted=%v err=%v", deleted, err)
	}
	revoked, err := store.DesktopLeaseByID(ctx, leaseID)
	if err != nil || revoked.State != "revoked" || revoked.ControlEpoch != lease.ControlEpoch+1 {
		t.Fatalf("assignment removal did not revoke lease: lease=%#v err=%v", revoked, err)
	}
	if allowed, err := store.AgentCanAccessDesktop(ctx, agentID, vmid); err != nil || allowed {
		t.Fatalf("agent retained access: allowed=%v err=%v", allowed, err)
	}

	secondDigest := []byte("second-token-digest-" + suffix)
	if _, err := store.RotateAgentToken(ctx, agentID, secondDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AgentByTokenDigest(ctx, []byte("first-token-digest-"+suffix)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old credential still authenticates: %v", err)
	}
	if authenticated, err := store.AgentByTokenDigest(ctx, secondDigest); err != nil || authenticated.ID != agentID {
		t.Fatalf("rotated credential failed: agent=%#v err=%v", authenticated, err)
	}
}
