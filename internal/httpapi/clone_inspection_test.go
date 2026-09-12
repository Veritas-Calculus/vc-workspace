package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
)

func TestCloneInspectionIsReadOnlyAndDoesNotPromoteMarker(t *testing.T) {
	db, suffix := regressionDB(t)
	user := store.User{ID: suffix, Username: suffix, DisplayName: "Inspector", PasswordHash: "unused", Role: "platform_admin"}
	if _, err := db.CreateLocalUser(t.Context(), user); err != nil {
		t.Fatal(err)
	}
	job, _, err := db.CreateJob(t.Context(), store.Job{ID: "inspect-job", IdempotencyKey: "inspect-job", Operation: "pve.template_clone", State: "accepted", SourceVMID: 9202, TargetVMID: 9204, TargetNode: "test", Request: json.RawMessage(`{"name":"vc-workspace-check"}`)})
	if err != nil {
		t.Fatal(err)
	}
	var mode atomic.Value
	mode.Store("match")
	var mutations atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			mutations.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api2/json/version":
			_, _ = w.Write([]byte(`{"data":{"version":"9.2","release":"9"}}`))
		case "/api2/json/nodes", "/api2/json/storage", "/api2/json/cluster/status":
			_, _ = w.Write([]byte(`{"data":[]}`))
		case "/api2/json/cluster/resources":
			if mode.Load() == "absent" {
				_, _ = w.Write([]byte(`{"data":[]}`))
			} else {
				vm := map[string]any{"vmid": 9204, "node": "test", "type": "qemu"}
				switch mode.Load() {
				case "wrong_node":
					vm["node"] = "other"
				case "container":
					vm["type"] = "lxc"
				case "template":
					vm["template"] = 1
				}
				items := []map[string]any{vm}
				if mode.Load() == "duplicate" {
					items = append(items, vm)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": items})
			}
		case "/api2/json/nodes/test/qemu/9204/config":
			if mode.Load() == "unavailable" {
				w.WriteHeader(503)
				return
			}
			cfg := map[string]string{"name": "vc-workspace-check", "description": "VC Workspace clone job: inspect-job"}
			if mode.Load() == "mismatch" {
				cfg["description"] = "private unrelated description"
			}
			if mode.Load() == "locked" {
				cfg["lock"] = "clone"
			}
			if mode.Load() == "became_template" {
				cfg["template"] = "1"
			}
			if mode.Load() == "wrong_name" {
				cfg["name"] = "unrelated-vm"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": cfg})
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	client, err := pve.New(pve.Config{Endpoint: upstream.URL, TokenID: "test@pve!inspect", TokenSecret: "fixture", HTTPClient: upstream.Client()})
	if err != nil {
		t.Fatal(err)
	}
	server := New(Dependencies{Store: db, PVE: client})
	for _, tc := range []struct {
		mode, role, want string
		code             int
	}{
		{"match", "user", "", 403}, {"match", "platform_admin", "marker_matches", 200},
		{"locked", "platform_admin", "target_locked", 200}, {"mismatch", "platform_admin", "target_mismatch", 200},
		{"absent", "platform_admin", "target_absent", 200}, {"unavailable", "platform_admin", "inspection_unavailable", 503},
		{"wrong_node", "platform_admin", "target_mismatch", 200}, {"container", "platform_admin", "target_mismatch", 200},
		{"template", "platform_admin", "target_mismatch", 200}, {"became_template", "platform_admin", "target_mismatch", 200},
		{"wrong_name", "platform_admin", "target_mismatch", 200}, {"duplicate", "platform_admin", "inspection_unavailable", 503},
	} {
		mode.Store(tc.mode)
		actor := user
		actor.Role = tc.role
		r := httptest.NewRequest("GET", "/api/v1/jobs/inspect-job/clone-inspection", nil)
		r.SetPathValue("id", job.ID)
		w := httptest.NewRecorder()
		server.inspectCloneTarget(w, r, store.Session{User: actor}, "")
		if w.Code != tc.code || !strings.Contains(w.Body.String(), tc.want) || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: %d %s", tc.mode, w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), "private unrelated") || strings.Contains(w.Body.String(), "VC Workspace clone job:") {
			t.Fatal("raw description leaked")
		}
	}
	saved, err := db.JobByID(t.Context(), job.ID)
	if err != nil || saved.State != "accepted" || saved.UPID != "" || mutations.Load() != 0 {
		t.Fatalf("inspection changed job or PVE: %+v %v writes=%d", saved, err, mutations.Load())
	}
	anonymous := httptest.NewRecorder()
	server.Handler().ServeHTTP(anonymous, httptest.NewRequest("GET", "/api/v1/jobs/inspect-job/clone-inspection", nil))
	if anonymous.Code != 401 {
		t.Fatalf("anonymous inspection: %d", anonymous.Code)
	}
}
