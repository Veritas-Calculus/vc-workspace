package store

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/Veritas-Calculus/vc-workspace/internal/testdb"
	"time"
)

func TestLocalPasswordChangeRevokesOtherSessionsIntegration(t *testing.T) {
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
	userID := "password-user-" + suffix
	oidcUserID := "password-oidc-" + suffix
	vmid := 700000000 + int(time.Now().UnixNano()%100000000)
	retainedWeb := []byte("retained-web-" + suffix)
	revokedWeb := []byte("revoked-web-" + suffix)
	native := []byte("native-" + suffix)
	code := []byte("native-code-" + suffix)
	connectionID := "password-connection-" + suffix
	defer func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = database.pool.Exec(cleanup, `DELETE FROM users WHERE id=ANY($1)`, []string{userID, oidcUserID})
		_, _ = database.pool.Exec(cleanup, `DELETE FROM managed_desktops WHERE vmid=$1`, vmid)
	}()

	if _, err := database.CreateLocalUser(ctx, User{ID: userID, Username: userID, DisplayName: "Password User", PasswordHash: "old-hash"}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.pool.Exec(ctx, `INSERT INTO users(id,username,username_normalized,display_name,password_hash,role) VALUES($1,$2,$2,$3,NULL,'user')`, oidcUserID, oidcUserID, "OIDC User"); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateSession(ctx, retainedWeb, userID, "retained-csrf", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateSession(ctx, revokedWeb, userID, "revoked-csrf", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateNativeSession(ctx, native, userID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateNativeAuthCode(ctx, code, userID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertManagedDesktop(ctx, ManagedDesktop{VMID: vmid, DisplayName: "Password desktop", Node: "node-test", OSFamily: "linux"}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.PutDesktopAssignment(ctx, DesktopAssignment{SubjectType: "user", SubjectID: userID, DesktopVMID: vmid}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateDesktopConnectionSession(ctx, DesktopConnectionSession{ID: connectionID, UserID: userID, DesktopVMID: vmid, GuestUsername: "vcwpassword1", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	updated, err := database.SetUserPassword(ctx, userID, "new-hash", retainedWeb)
	if err != nil || updated.IdentityKind != "local" {
		t.Fatalf("set password: user=%#v err=%v", updated, err)
	}
	retained, err := database.SessionByToken(ctx, retainedWeb)
	if err != nil || retained.User.PasswordHash != "new-hash" {
		t.Fatalf("retained Web session did not see the new credential: session=%#v err=%v", retained, err)
	}
	if _, err := database.SessionByToken(ctx, revokedWeb); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other Web session was not revoked: %v", err)
	}
	if _, err := database.NativeSessionByToken(ctx, native); !errors.Is(err, ErrNotFound) {
		t.Fatalf("native session was not revoked: %v", err)
	}
	if _, err := database.ConsumeNativeAuthCode(ctx, code); !errors.Is(err, ErrNotFound) {
		t.Fatalf("native authorization code was not revoked: %v", err)
	}
	due, err := database.DesktopConnectionsDueForRevocation(ctx, 100)
	if err != nil || !containsDesktopConnection(due, connectionID) {
		t.Fatalf("desktop session was not queued for credential revocation: sessions=%#v err=%v", due, err)
	}
	if _, err := database.SetUserPassword(ctx, oidcUserID, "new-hash", nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("OIDC password ownership was not enforced: %v", err)
	}
	apiDigest := []byte("iac-token-" + suffix)
	apiToken, err := database.CreateAPIToken(ctx, userID, APIToken{ID: "token_" + suffix, Name: "Terraform", ExpiresAt: time.Now().Add(time.Hour)}, apiDigest)
	if err != nil {
		t.Fatal(err)
	}
	apiSession, err := database.SessionByAPIToken(ctx, apiDigest)
	if err != nil || apiSession.User.ID != userID || apiSession.AuthKind != "api_token" || apiSession.CredentialID != apiToken.ID {
		t.Fatalf("API credential did not authenticate: session=%#v err=%v", apiSession, err)
	}
	listed, err := database.APITokens(ctx, userID)
	if err != nil || len(listed) == 0 || listed[0].LastUsedAt == nil {
		t.Fatalf("API credential use was not listed: tokens=%#v err=%v", listed, err)
	}
	deleted, err := database.DeleteAPIToken(ctx, userID, apiToken.ID)
	if err != nil || !deleted {
		t.Fatalf("API credential was not revoked: deleted=%v err=%v", deleted, err)
	}
	if _, err := database.SessionByAPIToken(ctx, apiDigest); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked API credential still authenticated: %v", err)
	}
}
