package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/Veritas-Calculus/vc-workspace/internal/testdb"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/auth"
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
)

// This opt-in integration test validates the public IaC authentication boundary
// against PostgreSQL instead of replacing it with an HTTP or store mock.
func TestAPITokenAuthorizationIntegration(t *testing.T) {
	databaseURL := testdb.URL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	database, err := store.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	adminID := "iac-admin-" + suffix
	subjectID := "iac-user-" + suffix
	vmid := 600000000 + int(time.Now().UnixNano()%100000000)
	if err := database.CreateInitialAdmin(ctx, store.User{
		ID: adminID, Username: adminID, DisplayName: "IaC administrator", PasswordHash: "not-used",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateLocalUser(ctx, store.User{
		ID: subjectID, Username: subjectID, DisplayName: "IaC assignment target", PasswordHash: "not-used",
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertManagedDesktop(ctx, store.ManagedDesktop{
		VMID: vmid, DisplayName: "IaC desktop", Node: "node-test", OSFamily: "linux", AccessMode: "personal", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	rawToken := "vcwi_integration_" + suffix
	if _, err := database.CreateAPIToken(ctx, adminID, store.APIToken{
		ID: "token_" + suffix, Name: "Integration", ExpiresAt: time.Now().Add(time.Hour),
	}, auth.TokenDigest(rawToken)); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(New(Dependencies{Store: database, PublicURL: "http://127.0.0.1"}).Handler())
	defer server.Close()
	request := func(method, path string, body []byte) (int, map[string]any) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, server.URL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+rawToken)
		if len(body) > 0 {
			req.Header.Set("Content-Type", "application/json")
		}
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var payload map[string]any
		_ = json.NewDecoder(response.Body).Decode(&payload)
		return response.StatusCode, payload
	}

	if status, payload := request(http.MethodGet, "/api/v1/access-control", nil); status != http.StatusOK {
		t.Fatalf("API token could not read control-plane state: status=%d body=%#v", status, payload)
	}
	assignmentPath := "/api/v1/desktop-assignments/user/" + subjectID + "/" + strconv.Itoa(vmid)
	if status, payload := request(http.MethodPut, assignmentPath, nil); status != http.StatusOK {
		t.Fatalf("API token could not create an assignment without CSRF: status=%d body=%#v", status, payload)
	}
	if status, payload := request(http.MethodPost, "/api/v1/api-tokens", []byte(`{"name":"Nested","expires_in_days":1}`)); status != http.StatusUnauthorized {
		t.Fatalf("API token minted another credential: status=%d body=%#v", status, payload)
	}
	if status, payload := request(http.MethodPost, "/api/v1/users", []byte(`{"username":"out-of-scope","display_name":"Out of scope","password":"Workspace-Test-789!"}`)); status != http.StatusUnauthorized {
		t.Fatalf("API token reached a Web-only administrator route: status=%d body=%#v", status, payload)
	}
}
