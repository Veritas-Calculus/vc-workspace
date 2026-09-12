package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/auth"
	"github.com/Veritas-Calculus/vc-workspace/internal/computer"
	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
	"github.com/Veritas-Calculus/vc-workspace/internal/testdb"
)

type nativeOrderExecutor struct {
	controlledComputerExecutor
	db            *store.Store
	t             *testing.T
	userID        string
	databaseURL   string
	current       *computer.NativeCredential
	operations    []string
	unsupported   bool
	inspectFailed bool
	failure       string
	onIssue       func()
}

func (e *nativeOrderExecutor) CheckNativeAccountSupport(context.Context, pve.VM) error {
	if e.unsupported {
		return computer.ErrUnavailable
	}
	return nil
}
func (*nativeOrderExecutor) PrepareNativeAccount(_ context.Context, _ pve.VM, username string) (computer.AccountIdentity, error) {
	return computer.AccountIdentity{Username: username, UID: 2001}, nil
}
func (e *nativeOrderExecutor) InspectNativeAccount(_ context.Context, _ pve.VM, username string) (computer.NativeAccountObservation, error) {
	if e.inspectFailed {
		return computer.NativeAccountObservation{}, computer.ErrUnavailable
	}
	absent := e.current == nil || e.current.Phase == "revoked"
	value := computer.NativeAccountObservation{Identity: computer.AccountIdentity{Username: username, UID: 2001},
		Exists: true, Disabled: absent, ProcessesAbsent: absent, LoginWritersAbsent: &absent, Lifecycle: e.current}
	if !absent {
		day := uint64((e.current.ExpiresUnixSeconds + 86399) / 86400)
		value.ExpiryDay = &day
	}
	return value, nil
}
func (e *nativeOrderExecutor) ChangeNativeCredential(ctx context.Context, machine pve.VM, intent computer.NativeCredential, operation string, password []byte) error {
	a, err := e.db.NativeGuestAccount(ctx, machine.VMID, e.userID)
	if err != nil || nativeCredential(a) != intent || a.Operation != operation || (a.State != "pending" && operation != "revoke") {
		e.t.Error("Guest write preceded exact durable identity/intent", err)
		return store.ErrConflict
	}
	if (operation == "issue" && len(password) < 24) || (operation != "issue" && len(password) != 0) {
		e.t.Error("unexpected credential transport")
		return computer.ErrInvalid
	}
	if operation == "issue" {
		if _, err := e.db.DesktopConnectionByID(ctx, intent.ConnectionID); !errors.Is(err, store.ErrNotFound) {
			e.t.Error("connection existed before Guest credential acknowledgement")
			return store.ErrConflict
		}
	}
	e.operations = append(e.operations, operation)
	if e.failure == operation+"-before" {
		return computer.ErrUnavailable
	}
	intent.Phase = map[string]string{"issue": "issued", "retire": "retired", "revoke": "revoked"}[operation]
	e.current = &intent
	if operation == "issue" && e.onIssue != nil {
		e.onIssue()
	}
	if e.failure == operation+"-lost" {
		return computer.ErrUnavailable
	}
	return nil
}

func nativeOrderFixture(t *testing.T) (*Server, *nativeOrderExecutor, store.User, string) {
	return nativeOrderCertificateFixture(t, nil)
}

func nativeOrderCertificateFixture(t *testing.T, certificate *pve.GuestExecResult) (*Server, *nativeOrderExecutor, store.User, string) {
	t.Helper()
	databaseURL := testdb.URL(t)
	db, err := store.Open(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	user := store.User{ID: "native-order-user", Username: "native-order", DisplayName: "Native", PasswordHash: "unused"}
	_, err = db.CreateLocalUser(t.Context(), user)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertManagedDesktop(t.Context(), store.ManagedDesktop{VMID: 9001, Node: "test", OSFamily: "linux", DisplayName: "Native order"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutDesktopAssignment(t.Context(), store.DesktopAssignment{SubjectType: "user", SubjectID: user.ID, DesktopVMID: 9001}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutDesktopAccessPolicy(t.Context(), 9001, "standard", true, false, false, ""); err != nil {
		t.Fatal(err)
	}
	token := "native-wiring-test-token"
	if err := db.CreateNativeSession(t.Context(), auth.TokenDigest(token), user.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var execMu sync.Mutex
	var execResult pve.GuestExecResult
	remote := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/version"):
			_, _ = w.Write([]byte(`{"data":{"version":"test"}}`))
		case strings.HasSuffix(r.URL.Path, "/cluster/status"), strings.HasSuffix(r.URL.Path, "/nodes"), strings.HasSuffix(r.URL.Path, "/storage"):
			_, _ = w.Write([]byte(`{"data":[]}`))
		case strings.HasSuffix(r.URL.Path, "/cluster/resources"):
			_, _ = w.Write([]byte(`{"data":[{"vmid":9001,"type":"qemu","node":"test","status":"running","tags":"vc-workspace"}]}`))
		case strings.HasSuffix(r.URL.Path, "/config"):
			_, _ = w.Write([]byte(`{"data":{"ostype":"l26"}}`))
		case strings.HasSuffix(r.URL.Path, "/agent/network-get-interfaces"):
			_, _ = w.Write([]byte(`{"data":{"result":[{"name":"eth0","ip-addresses":[{"ip-address":"10.0.0.42","ip-address-type":"ipv4","prefix":24}]}]}}`))
		case strings.HasSuffix(r.URL.Path, "/agent/file-read"):
			_, _ = w.Write([]byte(`{"data":{"content":"ready"}}`))
		case strings.HasSuffix(r.URL.Path, "/agent/exec"):
			execMu.Lock()
			defer execMu.Unlock()
			execResult = pve.GuestExecResult{}
			_ = r.ParseForm()
			command := strings.Join(r.Form["command"], " ")
			if certificate != nil && strings.Contains(command, "def discover(port=3389)") {
				if len(r.Form["command"]) != 4 || r.Form["command"][0] != "/usr/bin/python3" || r.Form["command"][1] != "-I" || r.Form.Get("input-data") != "" {
					t.Error("certificate discovery used client-selected command/input")
				}
				execResult = *certificate
			}
			if strings.Contains(command, "usermod --unlock") || strings.Contains(command, "usermod --lock --expiredate") || strings.Contains(command, "useradd --create-home") {
				t.Error("default Native path used legacy username credential/provisioning")
			}
			_, _ = w.Write([]byte(`{"data":{"pid":42}}`))
		case strings.HasSuffix(r.URL.Path, "/agent/exec-status"):
			execMu.Lock()
			defer execMu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"exited": 1, "exitcode": execResult.ExitCode, "out-data": execResult.Stdout, "err-data": execResult.Stderr}})
		default:
			t.Error("unexpected PVE request, including any unfenced password fallback", r.URL.Path)
			http.Error(w, "unexpected", 500)
		}
	}))
	t.Cleanup(remote.Close)
	client, err := pve.New(pve.Config{Endpoint: remote.URL, TokenID: "test@pve!test", TokenSecret: "unused", HTTPClient: remote.Client(), MutationsEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	e := &nativeOrderExecutor{db: db, t: t, userID: user.ID, databaseURL: databaseURL}
	return New(Dependencies{Store: db, PVE: client, Computer: e}), e, user, token
}

func nativeOrderReplica(t *testing.T, s *Server, e *nativeOrderExecutor) *Server {
	t.Helper()
	db, err := store.Open(t.Context(), e.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return New(Dependencies{Store: db, PVE: s.pve, Computer: e})
}

func nativeOrderRequest(s *Server, token, method, path string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/v1/"+path, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func nativeOrderConnect(t *testing.T, s *Server, token string) string {
	t.Helper()
	w := nativeOrderRequest(s, token, "POST", "native/desktops/9001/connections")
	if w.Code != 201 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("Native connection did not complete", w.Code)
	}
	var body struct{ ID, Password string }
	if json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Password) < 24 {
		t.Fatal("descriptor missing bounded credential")
	}
	return body.ID
}

func TestDefaultNativeConnectionVersionedDisconnectReconnectAndLogout(t *testing.T) {
	s, e, user, token := nativeOrderFixture(t)
	first := nativeOrderConnect(t, s, token)
	a, _ := s.store.NativeGuestAccount(t.Context(), 9001, user.ID)
	c, _ := s.store.DesktopConnectionByID(t.Context(), first)
	if a.Revision != 1 || a.Operation != "issue" || a.State != "applied" || c.State != "active" || !a.ExpiresAt.Equal(c.ExpiresAt) {
		t.Fatal("credential/session atomic commit diverged")
	}
	if w := nativeOrderRequest(s, token, "DELETE", "native/connections/"+first); w.Code != 204 {
		t.Fatal("ordinary disconnect failed", w.Code)
	}
	a, _ = s.store.NativeGuestAccount(t.Context(), 9001, user.ID)
	if a.Revision != 2 || a.Operation != "retire" || a.State != "applied" || e.current.Phase != "retired" {
		t.Fatal("disconnect terminated rather than retained desktop")
	}
	second := nativeOrderConnect(t, s, token)
	if second == first || strings.Join(e.operations, ",") != "issue,retire,issue" {
		t.Fatal("reconnect reused connection or revoked retained desktop")
	}
	if w := nativeOrderRequest(s, token, "DELETE", "native/connections/"+first); w.Code != 204 || len(e.operations) != 3 {
		t.Fatal("stale disconnect changed new credential")
	}
	if w := nativeOrderRequest(s, token, "POST", "auth/native/logout"); w.Code != 204 {
		t.Fatal("logout did not durably queue", w.Code)
	}
	s.revokeDueGuestIdentities(t.Context())
	a, _ = s.store.NativeGuestAccount(t.Context(), 9001, user.ID)
	c, _ = s.store.DesktopConnectionByID(t.Context(), second)
	if a.Revision != 4 || a.Operation != "revoke" || a.State != "applied" || c.State != "revoked" || e.current.Phase != "revoked" {
		t.Fatal("logout did not close exact Native account/connection")
	}
	if pending, err := s.store.PendingGuestIdentityRevocations(t.Context(), 9001); err != nil || len(pending) != 0 {
		t.Fatal("Native queue acknowledgement missing", err)
	}
}

func TestNativeGuestAheadOfRestoredDatabaseRejectsNewCredential(t *testing.T) {
	s, e, user, token := nativeOrderFixture(t)
	first := nativeOrderConnect(t, s, token)
	if w := nativeOrderRequest(s, token, "DELETE", "native/connections/"+first); w.Code != 204 {
		t.Fatal("disconnect failed", w.Code)
	}
	before, err := s.store.NativeGuestAccount(t.Context(), 9001, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Model restoring an older database while the same immutable Guest UID
	// has already processed later credentials. Never lower the Guest fence.
	advanced := *e.current
	advanced.Revision += 10
	advanced.Phase = "revoked"
	e.current = &advanced
	operations := len(e.operations)
	w := nativeOrderRequest(s, token, "POST", "native/desktops/9001/connections")
	if w.Code != http.StatusConflict {
		t.Fatalf("restored state did not return a connection conflict: %d", w.Code)
	}
	after, err := s.store.NativeGuestAccount(t.Context(), 9001, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != before.Revision || after.ConnectionID != before.ConnectionID || after.Operation != before.Operation || after.State != before.State {
		t.Fatal("rejected connection advanced the restored database intent")
	}
	if len(e.operations) != operations || *e.current != advanced {
		t.Fatal("rejected connection mutated the newer Guest lifecycle")
	}
}

func TestDefaultNativeLostIssueReceiptIsRevokedByAnotherReplicaWithoutPasswordReplay(t *testing.T) {
	s, e, user, token := nativeOrderFixture(t)
	e.failure = "issue-lost"
	w := nativeOrderRequest(s, token, "POST", "native/desktops/9001/connections")
	if w.Code != 409 || strings.Contains(w.Body.String(), "password") {
		t.Fatal("lost Guest receipt leaked descriptor", w.Code)
	}
	a, _ := s.store.NativeGuestAccount(t.Context(), 9001, user.ID)
	if a.Revision != 2 || a.Operation != "revoke" || a.State != "pending" {
		t.Fatal("uncertain issue lost durable higher cleanup")
	}
	if _, err := s.store.DesktopConnectionByID(t.Context(), a.ConnectionID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("connection committed without acknowledgement", err)
	}
	e.failure = ""
	other := nativeOrderReplica(t, s, e)
	other.recoverDueNativeAccounts(t.Context())
	nativeOrderConnect(t, other, token)
	if strings.Join(e.operations, ",") != "issue,revoke,issue" {
		t.Fatal("recovery replayed an old password", e.operations)
	}
}

func TestDefaultNativeInterruptedRetirementObservedOrRevokedNeverReplayed(t *testing.T) {
	for _, failure := range []string{"retire-lost", "retire-before"} {
		t.Run(failure, func(t *testing.T) {
			s, e, _, token := nativeOrderFixture(t)
			first := nativeOrderConnect(t, s, token)
			e.failure = failure
			if w := nativeOrderRequest(s, token, "DELETE", "native/connections/"+first); w.Code != 503 {
				t.Fatal("uncertain retire was acknowledged", w.Code)
			}
			e.failure = ""
			other := nativeOrderReplica(t, s, e)
			e.inspectFailed = true
			other.recoverDueNativeAccounts(t.Context())
			if len(e.operations) != 2 {
				t.Fatal("observation failure was mistaken for permission to terminate the desktop")
			}
			e.inspectFailed = false
			other.recoverDueNativeAccounts(t.Context())
			nativeOrderConnect(t, other, token)
			want := "issue,retire,issue"
			if failure == "retire-before" {
				want = "issue,retire,revoke,issue"
			}
			if strings.Join(e.operations, ",") != want {
				t.Fatal("retirement recovery changed its proven scope", e.operations)
			}
		})
	}
}

func TestDefaultNativeLogoutDuringGuestWriteCannotCommitDescriptor(t *testing.T) {
	s, e, user, token := nativeOrderFixture(t)
	e.onIssue = func() {
		if err := s.store.RevokeNativeSession(t.Context(), auth.TokenDigest(token)); err != nil {
			t.Fatal(err)
		}
	}
	if w := nativeOrderRequest(s, token, "POST", "native/desktops/9001/connections"); w.Code != 409 {
		t.Fatal("authorization lost during Guest write still returned descriptor", w.Code)
	}
	a, _ := s.store.NativeGuestAccount(t.Context(), 9001, user.ID)
	if _, err := s.store.DesktopConnectionByID(t.Context(), a.ConnectionID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("late credential acknowledgement created connection", err)
	}
	s.revokeDueGuestIdentities(t.Context())
	a, _ = s.store.NativeGuestAccount(t.Context(), 9001, user.ID)
	if a.State != "applied" || a.Operation != "revoke" || strings.Join(e.operations, ",") != "issue,revoke" {
		t.Fatal("failed issue escaped exact identity cleanup")
	}
}

func TestDefaultNativeUpgradeProbePrecedesExistingConnectionRetirement(t *testing.T) {
	s, e, _, token := nativeOrderFixture(t)
	id := nativeOrderConnect(t, s, token)
	e.unsupported = true
	if w := nativeOrderRequest(s, token, "POST", "native/desktops/9001/connections"); w.Code != 409 {
		t.Fatal("unsupported Guest was allowed", w.Code)
	}
	c, err := s.store.DesktopConnectionByID(t.Context(), id)
	if err != nil || c.State != "active" || len(e.operations) != 1 {
		t.Fatal("unsupported reconnect disturbed existing connection", err)
	}
}

func TestDefaultNativeConcurrentReplicasSerializeCredentialAndRetirement(t *testing.T) {
	s, e, user, token := nativeOrderFixture(t)
	other := nativeOrderReplica(t, s, e)
	entered, proceed := make(chan struct{}), make(chan struct{})
	var once, release sync.Once
	t.Cleanup(func() { release.Do(func() { close(proceed) }) })
	e.onIssue = func() { once.Do(func() { close(entered); <-proceed }) }
	first, second := make(chan *httptest.ResponseRecorder, 1), make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- nativeOrderRequest(s, token, "POST", "native/desktops/9001/connections") }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first replica did not reach Guest write")
	}
	go func() { second <- nativeOrderRequest(other, token, "POST", "native/desktops/9001/connections") }()
	select {
	case <-second:
		t.Fatal("second replica bypassed the in-flight VM gate")
	case <-time.After(30 * time.Millisecond):
	}
	release.Do(func() { close(proceed) })
	for _, responses := range []chan *httptest.ResponseRecorder{first, second} {
		select {
		case response := <-responses:
			if response.Code != 201 {
				t.Fatal("serialized replica could not finish", response.Code)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("serialized request did not finish")
		}
	}
	a, err := s.store.NativeGuestAccount(t.Context(), 9001, user.ID)
	if err != nil || a.Revision != 3 || a.State != "applied" || strings.Join(e.operations, ",") != "issue,retire,issue" {
		t.Fatal("cross-replica ordering or account version diverged", err)
	}
}
