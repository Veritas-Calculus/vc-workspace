package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/auth"
	"github.com/Veritas-Calculus/vc-workspace/internal/computer"
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
	"github.com/jackc/pgx/v5"
)

func TestNativeRecoveryComparison(t *testing.T) {
	a := store.NativeGuestAccount{UserID: "user", GuestUsername: "vcw123456789abc", GuestUID: 1003, Revision: 7, Operation: "revoke", State: "applied", ConnectionID: "conn_123456789abc"}
	absent := true
	for _, tc := range []struct {
		name, want string
		mutate     func(*computer.NativeAccountObservation)
	}{
		{"aligned", "aligned_closed", func(o *computer.NativeAccountObservation) {}},
		{"ahead", "guest_ahead_closed", func(o *computer.NativeAccountObservation) { o.Lifecycle.Revision = 23 }},
		{"behind", "lifecycle_mismatch", func(o *computer.NativeAccountObservation) { o.Lifecycle.Revision = 6 }},
		{"different_connection", "lifecycle_mismatch", func(o *computer.NativeAccountObservation) { o.Lifecycle.ConnectionID = "conn_other123456" }},
		{"identity", "identity_mismatch", func(o *computer.NativeAccountObservation) { o.Identity.UID++ }},
		{"missing", "identity_mismatch", func(o *computer.NativeAccountObservation) { o.Exists = false }},
		{"unversioned", "unversioned", func(o *computer.NativeAccountObservation) { o.Lifecycle = nil }},
		{"writer_unknown", "lifecycle_mismatch", func(o *computer.NativeAccountObservation) { o.LoginWritersAbsent = nil }},
		{"processes", "lifecycle_mismatch", func(o *computer.NativeAccountObservation) { o.ProcessesAbsent = false }},
		{"enabled", "lifecycle_mismatch", func(o *computer.NativeAccountObservation) { o.Disabled = false }},
		{"in_progress", "lifecycle_mismatch", func(o *computer.NativeAccountObservation) { o.Lifecycle.Phase = "issuing" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fence := nativeCredential(a)
			fence.Phase = "revoked"
			o := computer.NativeAccountObservation{Identity: fence.Identity, Exists: true, Disabled: true, ProcessesAbsent: true, LoginWritersAbsent: &absent, Lifecycle: &fence}
			tc.mutate(&o)
			if got := compareNativeRecovery(a, o); got.Status != tc.want {
				t.Fatalf("%+v", got)
			}
		})
	}
}

type lostRecoveryResponse struct {
	*httptest.ResponseRecorder
	writes int
}

func (w *lostRecoveryResponse) Write(_ []byte) (int, error) {
	w.writes++
	return 0, io.ErrClosedPipe
}

func TestNativeRecoveryCommitRechecksGuestAndResumesWithNewVersion(t *testing.T) {
	t.Run("delivered", func(t *testing.T) { nativeRecoveryCommitAndResume(t, false) })
	t.Run("response_write_lost", func(t *testing.T) { nativeRecoveryCommitAndResume(t, true) })
}

func nativeRecoveryCommitAndResume(t *testing.T, loseResponse bool) {
	t.Helper()
	s, e, user, token := nativeOrderFixture(t)
	nativeOrderConnect(t, s, token)
	if w := nativeOrderRequest(s, token, "POST", "auth/native/logout"); w.Code != 204 {
		t.Fatal(w.Code)
	}
	s.revokeDueGuestIdentities(t.Context())
	a, err := s.store.NativeGuestAccount(t.Context(), 9001, user.ID)
	if err != nil || a.Operation != "revoke" || a.State != "applied" {
		t.Fatal("fixture not revoked", err)
	}
	admin := store.User{ID: "recovery-admin", Username: "recovery-admin", DisplayName: "Recovery", PasswordHash: "unused", Role: "platform_admin"}
	if _, err := s.store.CreateLocalUser(t.Context(), admin); err != nil {
		t.Fatal(err)
	}
	fixtureDB, err := pgx.Connect(t.Context(), e.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer fixtureDB.Close(t.Context())
	if _, err := fixtureDB.Exec(t.Context(), `UPDATE users SET role='platform_admin' WHERE id=$1`, admin.ID); err != nil {
		t.Fatal(err)
	}
	advanced := *e.current
	advanced.Revision += 10
	advanced.ConnectionID = "conn_recovery_guest"
	e.current = &advanced
	operations := len(e.operations)
	dropNextResponse := false
	var dropped *lostRecoveryResponse
	call := func(role, csrf string, guestRevision int64) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"database_revision": a.Revision, "guest_revision": guestRevision, "reason": "Verified isolated backup recovery"})
		r := httptest.NewRequest(http.MethodPost, "/api/v1/managed-desktops/9001/native-recovery/"+user.ID, strings.NewReader(string(body)))
		r.SetPathValue("vmid", "9001")
		r.SetPathValue("user_id", user.ID)
		r.Header.Set("X-CSRF-Token", csrf)
		w := httptest.NewRecorder()
		actor := admin
		actor.Role = role
		if dropNextResponse {
			dropNextResponse = false
			dropped = &lostRecoveryResponse{ResponseRecorder: w}
			s.recoverNativeAccount(dropped, r, store.Session{User: actor, CSRFToken: "csrf"}, "")
		} else {
			s.recoverNativeAccount(w, r, store.Session{User: actor, CSRFToken: "csrf"}, "")
		}
		return w
	}
	for _, tc := range []struct {
		role, csrf string
		version    int64
		code       int
	}{
		{"user", "csrf", advanced.Revision, 403}, {"platform_admin", "wrong", advanced.Revision, 403}, {"platform_admin", "csrf", advanced.Revision + 1, 409},
	} {
		if w := call(tc.role, tc.csrf, tc.version); w.Code != tc.code {
			t.Fatalf("denial: %d %s", w.Code, w.Body)
		}
	}
	e.current.Phase = "issued"
	if w := call("platform_admin", "csrf", advanced.Revision); w.Code != 409 {
		t.Fatalf("active Guest accepted: %d", w.Code)
	}
	e.current.Phase = "revoked"
	dropNextResponse = loseResponse
	w := call("platform_admin", "csrf", advanced.Revision)
	if w.Code != 200 || (!loseResponse && !strings.Contains(w.Body.String(), "aligned_closed")) {
		t.Fatalf("recovery: %d %s", w.Code, w.Body)
	}
	if loseResponse && (dropped == nil || dropped.writes != 1 || w.Body.Len() != 0) {
		t.Fatal("response loss was not injected")
	}
	// A caller with an uncertain POST result must inspect again, not infer
	// failure or reset the Guest. This GET observes the committed state.
	inspect := httptest.NewRequest(http.MethodGet, "/api/v1/managed-desktops/9001/native-recovery", nil)
	inspect.SetPathValue("vmid", "9001")
	observed := httptest.NewRecorder()
	s.inspectNativeRecovery(observed, inspect, store.Session{User: admin}, "")
	var report struct {
		Accounts []nativeRecoveryReport `json:"accounts"`
	}
	if observed.Code != 200 || json.Unmarshal(observed.Body.Bytes(), &report) != nil || len(report.Accounts) != 1 || report.Accounts[0].Status != "aligned_closed" || report.Accounts[0].DatabaseRevision != advanced.Revision {
		t.Fatalf("uncertain recovery cannot be inspected: %d %s", observed.Code, observed.Body)
	}
	if len(e.operations) != operations || *e.current != advanced {
		t.Fatal("recovery wrote Guest credentials")
	}
	if w := call("platform_admin", "csrf", advanced.Revision); w.Code != 409 {
		t.Fatalf("stale repeat: %d", w.Code)
	}
	var receipts int
	if err := fixtureDB.QueryRow(t.Context(), `SELECT count(*) FROM native_guest_recoveries`).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("recovery replay duplicated receipt: %d %v", receipts, err)
	}
	if err := s.store.CreateNativeSession(t.Context(), auth.TokenDigest(token), user.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	nativeOrderConnect(t, s, token)
	if e.current.Revision != advanced.Revision+1 || e.current.Phase != "issued" {
		t.Fatal("new connection did not advance recovered fence")
	}
}

func TestNativeRecoveryInspectionIsAdminOnlyAndDoesNotChangeGuest(t *testing.T) {
	s, e, user, token := nativeOrderFixture(t)
	anonymous := httptest.NewRecorder()
	s.Handler().ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, "/api/v1/managed-desktops/9001/native-recovery", nil))
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("route missing authentication: %d", anonymous.Code)
	}
	first := nativeOrderConnect(t, s, token)
	if w := nativeOrderRequest(s, token, "DELETE", "native/connections/"+first); w.Code != 204 {
		t.Fatal(w.Code)
	}
	advanced := *e.current
	advanced.Revision += 10
	advanced.Phase = "revoked"
	advanced.ExpiresUnixSeconds = 0
	e.current = &advanced
	before, err := s.store.NativeGuestAccount(t.Context(), 9001, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	operations := len(e.operations)
	for _, role := range []string{"user", "platform_admin"} {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/managed-desktops/9001/native-recovery", nil)
		r.SetPathValue("vmid", "9001")
		w := httptest.NewRecorder()
		u := user
		u.Role = role
		s.inspectNativeRecovery(w, r, store.Session{User: u}, "")
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("cacheable inspection")
		}
		if role == "user" {
			if w.Code != 403 {
				t.Fatal("inspection authorization missing")
			}
			continue
		}
		var body struct {
			Accounts []nativeRecoveryReport `json:"accounts"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Accounts) != 1 || body.Accounts[0].Status != "guest_ahead_closed" {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		for _, secret := range []string{advanced.ConnectionID, advanced.Identity.Username, "password", "native_session_digest"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("inspection exposed unnecessary credential metadata")
			}
		}
	}
	after, err := s.store.NativeGuestAccount(t.Context(), 9001, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Revision != after.Revision || before.Operation != after.Operation || before.State != after.State || len(e.operations) != operations || *e.current != advanced {
		t.Fatal("inspection changed account lifecycle")
	}
	e.inspectFailed = true
	r := httptest.NewRequest(http.MethodGet, "/api/v1/managed-desktops/9001/native-recovery", nil)
	r.SetPathValue("vmid", "9001")
	w := httptest.NewRecorder()
	user.Role = "platform_admin"
	s.inspectNativeRecovery(w, r, store.Session{User: user}, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "inspection_unavailable") || strings.Contains(w.Body.String(), "guest_revision") {
		t.Fatalf("failed inspection fabricated a Guest version: %d %s", w.Code, w.Body)
	}
}
