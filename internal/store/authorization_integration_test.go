package store

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/Veritas-Calculus/vc-workspace/internal/testdb"
	"time"
)

func TestAuthorizationLifecycleIntegration(t *testing.T) {
	databaseURL := testdb.URL(t)
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
	secondUserID := "test-user-2-" + suffix
	groupID := "test-group-" + suffix
	secondGroupID := "test-group-2-" + suffix
	agentID := "test-agent-" + suffix
	leaseID := "test-lease-" + suffix
	releaseLeaseID := "test-release-lease-" + suffix
	assignmentLeaseID := "test-assignment-lease-" + suffix
	vmid := 800000000 + int(time.Now().UnixNano()%100000000)
	defer func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = store.pool.Exec(cleanup, `DELETE FROM desktop_leases WHERE id=ANY($1)`, []string{leaseID, releaseLeaseID, assignmentLeaseID})
		_, _ = store.pool.Exec(cleanup, `DELETE FROM audit_events WHERE actor_id=$1`, agentID)
		_, _ = store.pool.Exec(cleanup, `DELETE FROM agent_principals WHERE id=$1`, agentID)
		_, _ = store.pool.Exec(cleanup, `DELETE FROM users WHERE id=$1`, userID)
		_, _ = store.pool.Exec(cleanup, `DELETE FROM users WHERE id=$1`, secondUserID)
		_, _ = store.pool.Exec(cleanup, `DELETE FROM identity_groups WHERE id=$1`, groupID)
		_, _ = store.pool.Exec(cleanup, `DELETE FROM identity_groups WHERE id=$1`, secondGroupID)
		_, _ = store.pool.Exec(cleanup, `DELETE FROM managed_desktops WHERE vmid=$1`, vmid)
	}()

	user, err := store.CreateLocalUser(ctx, User{ID: userID, Username: userID, DisplayName: "Test User", PasswordHash: "not-used"})
	if err != nil || user.IdentityKind != "local" || user.Role != "user" {
		t.Fatalf("create local user: user=%#v err=%v", user, err)
	}
	if err := store.UpsertManagedDesktop(ctx, ManagedDesktop{VMID: vmid, DisplayName: "Test Desktop", Node: "node-test", OSFamily: "linux"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertManagedDesktop(ctx, ManagedDesktop{VMID: vmid, DisplayName: "Test Desktop", Node: "node-test"}); err != nil {
		t.Fatal(err)
	}
	if desktop, err := store.ManagedDesktopByVMID(ctx, vmid); err != nil || desktop.OSFamily != "linux" {
		t.Fatalf("unknown inventory refresh replaced known desktop OS: desktop=%#v err=%v", desktop, err)
	}
	agent, err := store.CreateAgentPrincipal(ctx, AgentPrincipal{ID: agentID, DisplayName: "Test Agent", CreatedBy: userID}, []byte("first-token-digest-"+suffix))
	if err != nil || !agent.Enabled {
		t.Fatalf("create agent: agent=%#v err=%v", agent, err)
	}
	if err := store.Audit(ctx, agentID, "agent.computer_action_succeeded", "desktop_lease", leaseID, map[string]any{"operation": "screenshot"}); err != nil {
		t.Fatal(err)
	}
	auditPage, err := store.AuditEvents(ctx, AuditEventQuery{Limit: 10, Search: agentID})
	if err != nil || len(auditPage.Events) != 1 || auditPage.Events[0].ActorID != agentID || auditPage.Events[0].ActorDisplayName != agent.DisplayName {
		t.Fatalf("agent audit identity was not resolved: page=%#v err=%v", auditPage, err)
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
	connectionID := "test-connection-" + suffix
	if _, err := store.CreateDesktopConnectionSession(ctx, DesktopConnectionSession{ID: connectionID, UserID: userID, DesktopVMID: vmid, GuestUsername: "vcwtestuser1", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
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
	due, err := store.DesktopConnectionsDueForRevocation(ctx, 10)
	if err != nil || !containsDesktopConnection(due, connectionID) {
		t.Fatalf("disabled user connection was not queued for credential revocation: sessions=%#v err=%v", due, err)
	}
	if err := store.MarkDesktopConnectionClosed(ctx, connectionID, "revoked"); err != nil {
		t.Fatal(err)
	}
	terminalConnection, err := store.BeginRevokeDesktopConnection(ctx, connectionID, userID)
	if err != nil || terminalConnection.State != "revoked" {
		t.Fatalf("repeated connection release was not idempotent: connection=%#v err=%v", terminalConnection, err)
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
	activeLeases, err := store.ActiveDesktopLeases(ctx)
	if err != nil || !containsDesktopLease(activeLeases, lease.ID) {
		t.Fatalf("active lease was not exposed to administrators: leases=%#v err=%v", activeLeases, err)
	}
	revokedByHuman, err := store.RevokeDesktopLeases(ctx, vmid)
	if err != nil || len(revokedByHuman) != 1 || revokedByHuman[0].State != "revoked" || revokedByHuman[0].ControlEpoch != lease.ControlEpoch+1 {
		t.Fatalf("human takeover did not revoke lease and advance epoch: leases=%#v err=%v", revokedByHuman, err)
	}
	activeLeases, err = store.ActiveDesktopLeases(ctx)
	if err != nil || containsDesktopLease(activeLeases, lease.ID) {
		t.Fatalf("revoked lease remained in administrator status: leases=%#v err=%v", activeLeases, err)
	}

	releaseLease, err := store.CreateDesktopLease(ctx, DesktopLease{ID: releaseLeaseID, AgentID: agentID, DesktopID: strconv.Itoa(vmid), ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	released, err := store.ReleaseDesktopLease(ctx, releaseLeaseID)
	if err != nil || released.State != "released" || released.ControlEpoch != releaseLease.ControlEpoch+1 {
		t.Fatalf("explicit release did not advance epoch: lease=%#v err=%v", released, err)
	}

	assignmentLease, err := store.CreateDesktopLease(ctx, DesktopLease{ID: assignmentLeaseID, AgentID: agentID, DesktopID: strconv.Itoa(vmid), ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := store.DeleteDesktopAssignment(ctx, "agent", agentID, vmid)
	if err != nil || !deleted {
		t.Fatalf("delete assignment: deleted=%v err=%v", deleted, err)
	}
	revoked, err := store.DesktopLeaseByID(ctx, assignmentLeaseID)
	if err != nil || revoked.State != "revoked" || revoked.ControlEpoch != assignmentLease.ControlEpoch+1 {
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

	if _, err := store.CreateLocalUser(ctx, User{ID: secondUserID, Username: secondUserID, DisplayName: "Second User", PasswordHash: "not-used"}); err != nil {
		t.Fatal(err)
	}
	if created, err := store.PutDesktopAssignment(ctx, DesktopAssignment{SubjectType: "user", SubjectID: secondUserID, DesktopVMID: vmid, CreatedBy: userID}); !errors.Is(err, ErrConflict) || created {
		t.Fatalf("personal desktop accepted a second owner: created=%v err=%v", created, err)
	}
	if _, err := store.UpdateManagedDesktopIdentity(ctx, vmid, "shared", "", "managed-local-linux"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutIdentityGroup(ctx, IdentityGroup{ID: groupID, DisplayName: "Test Group", Source: "local", Enabled: true, CreatedBy: userID}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutIdentityGroup(ctx, IdentityGroup{ID: secondGroupID, DisplayName: "Second Local Group", Source: "local", Enabled: true, CreatedBy: userID}); err != nil {
		t.Fatalf("multiple local groups must not collide on empty provider identity: %v", err)
	}
	if _, err := store.PutIdentityGroup(ctx, IdentityGroup{ID: secondGroupID, DisplayName: "Duplicate", Source: "local", Enabled: true, CreatedBy: userID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate local group ID should conflict, got %v", err)
	}
	if _, err := store.PutIdentityGroupMembership(ctx, IdentityGroupMembership{GroupID: groupID, UserID: secondUserID, Source: "local"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutDesktopAssignment(ctx, DesktopAssignment{SubjectType: "group", SubjectID: groupID, DesktopVMID: vmid, CreatedBy: userID}); err != nil {
		t.Fatal(err)
	}
	if allowed, err := store.UserCanAccessDesktop(ctx, secondUserID, vmid); err != nil || !allowed {
		t.Fatalf("shared desktop group assignment did not authorize its member: allowed=%v err=%v", allowed, err)
	}
}

func containsDesktopLease(leases []DesktopLease, id string) bool {
	for _, lease := range leases {
		if lease.ID == id {
			return true
		}
	}
	return false
}

func containsDesktopConnection(connections []DesktopConnectionSession, id string) bool {
	for _, connection := range connections {
		if connection.ID == id {
			return true
		}
	}
	return false
}
