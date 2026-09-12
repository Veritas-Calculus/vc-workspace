package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/auth"
	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
	"github.com/Veritas-Calculus/vc-workspace/internal/testdb"
)

func regressionDB(t *testing.T) (*store.Store, string) {
	t.Helper()
	database, err := store.Open(context.Background(), testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	if err := database.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return database, strconv.FormatInt(time.Now().UnixNano(), 36)
}

func TestInfrastructureEmptyCollectionsForScopedAdminAndUnassignedUser(t *testing.T) {
	db, suffix := regressionDB(t)
	user := store.User{ID: "empty-" + suffix, Username: "empty-" + suffix, DisplayName: "Unassigned", PasswordHash: "unused"}
	if _, err := db.CreateLocalUser(t.Context(), user); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api2/json/version":
			_, _ = w.Write([]byte(`{"data":{"version":"9.2","release":"9"}}`))
		case "/api2/json/nodes", "/api2/json/cluster/resources", "/api2/json/cluster/status", "/api2/json/storage":
			_, _ = w.Write([]byte(`{"data":[]}`))
		default:
			t.Errorf("unexpected upstream request %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()
	client, err := pve.New(pve.Config{Endpoint: upstream.URL, TokenID: "test@pve!empty", TokenSecret: "fixture", HTTPClient: upstream.Client()})
	if err != nil {
		t.Fatal(err)
	}
	server := New(Dependencies{Store: db, PVE: client})
	for _, role := range []string{"platform_admin", "user"} {
		t.Run(role, func(t *testing.T) {
			user.Role = role
			w := httptest.NewRecorder()
			server.infrastructure(w, httptest.NewRequest(http.MethodGet, "/api/v1/infrastructure", nil), store.Session{User: user}, "")
			if w.Code != http.StatusOK {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			var response map[string]json.RawMessage
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"nodes", "virtual_machines", "storage"} {
				if string(response[key]) != "[]" {
					t.Errorf("%s must be an empty array, got %s", key, response[key])
				}
			}
		})
	}
}

func TestNativeLogoutDurablyRevokesRetainedGuestWithoutRemoteDependency(t *testing.T) {
	db, suffix := regressionDB(t)
	ctx := t.Context()
	user := store.User{ID: "logout-" + suffix, Username: "logout-" + suffix, DisplayName: "Logout", PasswordHash: "unused"}
	if _, err := db.CreateLocalUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertManagedDesktop(ctx, store.ManagedDesktop{VMID: 9001, DisplayName: "Test", Node: "test", OSFamily: "linux"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutDesktopAssignment(ctx, store.DesktopAssignment{SubjectType: "user", SubjectID: user.ID, DesktopVMID: 9001}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutGuestIdentityBinding(ctx, store.GuestIdentityBinding{DesktopVMID: 9001, UserID: user.ID, GuestUsername: "vcwtestuser", State: "ready"}); err != nil {
		t.Fatal(err)
	}
	token := "native-" + suffix
	if err := db.CreateNativeSession(ctx, auth.TokenDigest(token), user.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	s := New(Dependencies{Store: db}) // No PVE dependency: logout must commit even offline.
	r := httptest.NewRequest("POST", "/api/v1/auth/native/logout", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatalf("logout failed: %d %s", w.Code, w.Body)
	}
	if _, err := db.NativeSessionByToken(ctx, auth.TokenDigest(token)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("token still valid: %v", err)
	}
	items, err := db.PendingGuestIdentityRevocations(ctx, 9001)
	if err != nil || len(items) != 1 || items[0].UserID != user.ID {
		t.Fatalf("retained Guest not queued: %v %v", items, err)
	}

	var attempts atomic.Int32
	fakePVE := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/agent/exec"):
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			command := strings.Join(r.Form["command"], " ")
			if !strings.Contains(command, "usermod --lock --expiredate 1") || !strings.Contains(command, "vcwtestuser") {
				t.Errorf("not account-scoped: %s", command)
			}
			attempts.Add(1)
			_, _ = w.Write([]byte(`{"data":{"pid":42}}`))
		case strings.HasSuffix(r.URL.Path, "/agent/exec-status"):
			exitCode := 0
			if attempts.Load() == 1 {
				exitCode = 1
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"exited": 1, "exitcode": exitCode}})
		default:
			t.Errorf("unexpected Guest mutation: %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer fakePVE.Close()
	client, err := pve.New(pve.Config{Endpoint: fakePVE.URL, TokenID: "test@pve!test", TokenSecret: "unused", MutationsEnabled: true, HTTPClient: fakePVE.Client()})
	if err != nil {
		t.Fatal(err)
	}
	s = New(Dependencies{Store: db, PVE: client})
	s.revokeDueGuestIdentities(ctx)
	if items, err := db.PendingGuestIdentityRevocations(ctx, 9001); err != nil || len(items) != 1 {
		t.Fatalf("failed Guest cleanup lost its retry: %v %v", items, err)
	}
	s.revokeDueGuestIdentities(ctx)
	if items, err := db.PendingGuestIdentityRevocations(ctx, 9001); err != nil || len(items) != 0 {
		t.Fatalf("successful Guest cleanup not acknowledged: %v %v", items, err)
	}
	s.revokeDueGuestIdentities(ctx)
	if attempts.Load() != 2 {
		t.Fatalf("completed work ran again: %d", attempts.Load())
	}
	page, err := db.AuditEvents(ctx, store.AuditEventQuery{Limit: 100, Search: "guest_identity.revoked"})
	if err != nil || len(page.Events) != 1 {
		t.Fatalf("expected one durable completion audit: %v %v", page, err)
	}
	if event := page.Events[0]; event.ActorID != "" || event.TargetID != "9001" || !strings.Contains(string(event.Detail), user.ID) {
		t.Fatalf("Guest worker audit misattributed: %v", event)
	}
}

func TestNativeIdempotencyRejectsUnauthorizedReplay(t *testing.T) {
	db, suffix := regressionDB(t)
	ctx := context.Background()
	victim := store.User{ID: "victim-" + suffix, Username: "victim-" + suffix, DisplayName: "Victim", PasswordHash: "unused"}
	other := store.User{ID: "other-" + suffix, Username: "other-" + suffix, DisplayName: "Other", PasswordHash: "unused"}
	for _, user := range []store.User{victim, other} {
		if _, err := db.CreateLocalUser(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	key := "review-key-" + suffix
	job, _, err := db.CreateJob(ctx, store.Job{ID: "job_" + suffix, IdempotencyKey: key, Operation: "desktop.clone", State: "succeeded", TargetVMID: 991001, TargetNode: "private-node", Request: json.RawMessage(`{"private":"victim-request"}`), CreatedBy: victim.ID})
	if err != nil {
		t.Fatal(err)
	}
	token := "review-native-" + suffix
	if err := db.CreateNativeSession(ctx, auth.TokenDigest(token), other.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/native/desktops/991001/actions/start", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Idempotency-Key", key)
	response := httptest.NewRecorder()
	New(Dependencies{Store: db}).Handler().ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), job.ID) || strings.Contains(response.Body.String(), "victim-request") {
		t.Fatalf("unexpected reproduction: %d %s", response.Code, response.Body.String())
	}
}

func TestStaleRevocationDoesNotRotateNewConnectionPassword(t *testing.T) {
	db, suffix := regressionDB(t)
	ctx := context.Background()
	user := store.User{ID: "revoke-" + suffix, Username: "revoke-" + suffix, DisplayName: "Review", PasswordHash: "unused"}
	if _, err := db.CreateLocalUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	vmid := 700000000 + int(time.Now().UnixNano()%10000000)
	if err := db.UpsertManagedDesktop(ctx, store.ManagedDesktop{VMID: vmid, DisplayName: "Review", Node: "mock-node", OSFamily: "linux", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutDesktopAssignment(ctx, store.DesktopAssignment{SubjectType: "user", SubjectID: user.ID, DesktopVMID: vmid}); err != nil {
		t.Fatal(err)
	}
	first, err := db.CreateDesktopConnectionSession(ctx, store.DesktopConnectionSession{ID: "connection_old_" + suffix, UserID: user.ID, DesktopVMID: vmid, GuestUsername: "vcwreview", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	stale, err := db.BeginRevokeDesktopConnection(ctx, first.ID, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkDesktopConnectionClosed(ctx, first.ID, "revoked"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateDesktopConnectionSession(ctx, store.DesktopConnectionSession{ID: "connection_new_" + suffix, UserID: user.ID, DesktopVMID: vmid, GuestUsername: "vcwreview", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	var rotations atomic.Int32
	fakePVE := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/agent/set-user-password") {
			t.Errorf("unexpected PVE operation: %s", r.URL.Path)
		}
		rotations.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"result":null}}`))
	}))
	defer fakePVE.Close()
	client, err := pve.New(pve.Config{Endpoint: fakePVE.URL, TokenID: "review@pve!test", TokenSecret: "unused", MutationsEnabled: true, HTTPClient: fakePVE.Client()})
	if err != nil {
		t.Fatal(err)
	}
	server := New(Dependencies{Store: db, PVE: client})
	if err := server.releaseDesktopConnection(ctx, stale); err != nil {
		t.Fatal(err)
	}
	if rotations.Load() != 0 {
		t.Fatalf("stale snapshot changed Guest credentials, got %d", rotations.Load())
	}
}
