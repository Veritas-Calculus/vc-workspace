package httpapi

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Veritas-Calculus/vc-workspace/internal/store"
)

func TestJobReplayScopesAndFingerprint(t *testing.T) {
	db, _ := regressionDB(t)
	s := New(Dependencies{Store: db})
	prepare := func(principal, path, body string) (string, string, bool, int) {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Idempotency-Key", "replay-scope-key")
		w := httptest.NewRecorder()
		key, fingerprint, proceed := s.prepareJobRequest(w, r, principal)
		return key, fingerprint, proceed, w.Code
	}
	key, fingerprint, proceed, _ := prepare("user:a", "/api/jobs", `{"a":1,"b":2}`)
	if !proceed {
		t.Fatal("new request rejected")
	}
	if _, _, err := db.CreateJob(context.Background(), store.Job{ID: "job-a", IdempotencyKey: key, RequestFingerprint: fingerprint, Operation: "test", State: "accepted", Request: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if _, _, proceed, status := prepare("user:a", "/api/jobs", ` {"b": 2, "a": 1} `); proceed || status != 202 {
		t.Fatalf("canonical replay failed: %v %d", proceed, status)
	}
	if _, _, proceed, status := prepare("user:a", "/api/jobs", `{"a":3,"b":2}`); proceed || status != 409 {
		t.Fatalf("changed payload replayed: %v %d", proceed, status)
	}
	for _, scope := range []struct{ principal, path string }{{"user:b", "/api/jobs"}, {"agent:a", "/api/jobs"}, {"user:a", "/api/other"}} {
		if scoped, _, proceed, _ := prepare(scope.principal, scope.path, `{"a":1,"b":2}`); !proceed || scoped == key {
			t.Fatalf("scope leaked: %#v", scope)
		}
	}
}

func TestGuestTerminationIsAccountScoped(t *testing.T) {
	for _, os := range []string{"linux", "windows"} {
		for _, user := range []string{"root", "administrator", "vdi", "vcw'; reboot", "vcw$(id)", "vcw\nroot"} {
			if _, err := terminateGuestSessionCommand(os, user); err == nil {
				t.Errorf("accepted unsafe %s account %q", os, user)
			}
			if _, err := enableGuestAccountCommand(os, user); err == nil {
				t.Errorf("accepted unsafe activation account %q", user)
			}
		}
		command, err := terminateGuestSessionCommand(os, "vcw0123456789ab")
		if err != nil || !strings.Contains(command[len(command)-1], "vcw0123456789ab") {
			t.Fatalf("missing exact managed account: %v", err)
		}
	}
}

func TestGuestProvisioningDoesNotReenableOldCredential(t *testing.T) {
	for _, os := range []string{"linux", "windows"} {
		command, err := ensureGuestUserCommand(os, "vcw0123456789ab")
		if err != nil {
			t.Fatal(err)
		}
		script := strings.Join(command, " ")
		for _, forbidden := range []string{"Enable-LocalUser", "passwd -u", "--unlock", "--expiredate ''"} {
			if strings.Contains(script, forbidden) {
				t.Fatalf("provisioning revived the old credential: %s", os)
			}
		}
		if _, err := enableGuestAccountCommand(os, "vcw0123456789ab"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestManagedGuestGPUAccessIsRenderOnly(t *testing.T) {
	command, err := ensureGuestUserCommand("linux", "vcw0123456789ab")
	if err != nil {
		t.Fatal(err)
	}
	script := command[len(command)-1]
	for _, required := range []string{"[ -c /dev/dri/renderD128 ]", "getent group render", `usermod -a -G render "$username"`} {
		if !strings.Contains(script, required) {
			t.Fatalf("missing managed GPU access guard: %s", required)
		}
	}
	for _, forbidden := range []string{"chmod 0666", "-G video", "-G sudo", "systemctl restart"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("GPU provisioning widens privilege or restarts sessions: %s", forbidden)
		}
	}
}

func TestLegacyRawJobKeyDoesNotRepeatMutationAfterUpgrade(t *testing.T) {
	db, _ := regressionDB(t)
	if _, _, err := db.CreateJob(context.Background(), store.Job{ID: "legacy-job", IdempotencyKey: "legacy-raw-key", Operation: "test", State: "accepted", Request: []byte(`{"private":"do-not-disclose"}`)}); err != nil {
		t.Fatal(err)
	}
	s := New(Dependencies{Store: db})
	r := httptest.NewRequest("POST", "/api/jobs", nil)
	r.Header.Set("Idempotency-Key", "legacy-raw-key")
	w := httptest.NewRecorder()
	_, _, proceed := s.prepareJobRequest(w, r, "user:a")
	if proceed || w.Code != 409 || strings.Contains(w.Body.String(), "do-not-disclose") {
		t.Fatalf("legacy request was repeated or leaked: %d %s", w.Code, w.Body.String())
	}
}
