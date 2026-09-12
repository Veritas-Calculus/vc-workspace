package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
)

func TestCloneCandidatesUsesJobScopeAndNeverAdopts(t *testing.T) {
	db, suffix := regressionDB(t)
	user := store.User{ID: suffix, Username: suffix, DisplayName: "Admin", PasswordHash: "unused", Role: "platform_admin"}
	if err := db.CreateInitialAdmin(t.Context(), user); err != nil {
		t.Fatal(err)
	}
	job, _, err := db.CreateJob(t.Context(), store.Job{ID: "discover", IdempotencyKey: "discover", Operation: "pve.template_clone", State: "accepted", SourceVMID: 9202, TargetVMID: 9204, TargetNode: "target", TaskNode: "source", Request: json.RawMessage(`{"pve_principal":"admin@pve","name":"clone-check"}`)})
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	var fail atomic.Bool
	var mode atomic.Value
	mode.Store("match")
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != "GET" {
			t.Errorf("inspection mutated PVE: %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/log") {
			if mode.Load() == "log_unavailable" {
				w.WriteHeader(503)
				return
			}
			line := "creating a clone of VM 9202 with ID 9204"
			if mode.Load() == "log_mismatch" {
				line = "creating a clone of VM 9202 with ID 9999"
			}
			if mode.Load() == "log_unknown" {
				line = "older log format"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"n": 1, "t": line}}})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/status") && strings.Contains(r.URL.Path, "/tasks/") {
			if strings.Contains(r.URL.Path, "!other") {
				t.Error("inspected another principal's task")
			}
			e := pve.TaskEvidence{UPID: "UPID:source:1:1:1:qmclone:9202:admin@pve:", Node: "source", Type: "qmclone", ID: "9202", User: "admin@pve", StartTime: job.CreatedAt.Unix(), Status: "stopped", ExitStatus: "OK"}
			switch mode.Load() {
			case "task_unavailable":
				w.WriteHeader(503)
				return
			case "wrong_source":
				e.ID = "9999"
			case "wrong_principal":
				e.TokenID = "other"
			case "wrong_start":
				e.StartTime++
			case "outside_window":
				e.StartTime += 3600
			case "running":
				e.Status = "running"
				e.ExitStatus = ""
			case "failed":
				e.ExitStatus = "clone failed"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": e})
			return
		}
		switch r.URL.Path {
		case "/api2/json/version":
			_, _ = w.Write([]byte(`{"data":{"version":"9.2","release":"9"}}`))
			return
		case "/api2/json/nodes", "/api2/json/storage", "/api2/json/cluster/status":
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		case "/api2/json/cluster/resources":
			if mode.Load() == "absent" {
				_, _ = w.Write([]byte(`{"data":[]}`))
			} else {
				_, _ = w.Write([]byte(`{"data":[{"vmid":9204,"node":"target","type":"qemu"}]}`))
			}
			return
		case "/api2/json/nodes/target/qemu/9204/config":
			if mode.Load() == "target_unavailable" {
				w.WriteHeader(503)
				return
			}
			cfg := map[string]string{"name": "clone-check", "description": "VC Workspace clone job: discover"}
			if mode.Load() == "mismatch" {
				cfg["description"] = "another job"
			}
			if mode.Load() == "locked" {
				cfg["lock"] = "clone"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": cfg})
			return
		}
		q := r.URL.Query()
		if r.Method != "GET" || r.URL.Path != "/api2/json/nodes/source/tasks" || q.Get("vmid") != "9202" || q.Get("since") != strconv.FormatInt(job.CreatedAt.Add(-30*time.Second).Unix(), 10) || q.Get("until") != strconv.FormatInt(job.CreatedAt.Add(15*time.Minute).Unix(), 10) {
			t.Errorf("caller escaped persisted scope: %s %s", r.Method, r.URL)
		}
		if fail.Load() {
			w.WriteHeader(503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []pve.CloneTaskCandidate{
			{UPID: "UPID:source:1:1:1:qmclone:9202:admin@pve:", Node: "source", ID: "9202", Type: "qmclone", User: "admin@pve", StartTime: job.CreatedAt.Unix()},
			{UPID: "UPID:source:2:2:1:qmclone:9202:admin@pve!other:", Node: "source", ID: "9202", Type: "qmclone", User: "admin@pve", TokenID: "other", StartTime: job.CreatedAt.Unix()},
		}})
	}))
	defer upstream.Close()
	client, err := pve.New(pve.Config{Endpoint: upstream.URL, TokenID: "test@pve!discovery", TokenSecret: "fixture", HTTPClient: upstream.Client()})
	if err != nil {
		t.Fatal(err)
	}
	s := New(Dependencies{Store: db, PVE: client})
	call := func(role string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/v1/jobs/discover/clone-candidates?since=1&vmid=9999", nil)
		r.SetPathValue("id", job.ID)
		actor := user
		actor.Role = role
		w := httptest.NewRecorder()
		s.cloneTaskCandidates(w, r, store.Session{User: actor}, "")
		return w
	}
	if w := call("user"); w.Code != 403 || requests.Load() != 0 {
		t.Fatal("non-admin discovery reached PVE")
	}
	w := call("platform_admin")
	var result struct {
		JobID        string `json:"job_id"`
		TargetStatus string `json:"target_status"`
		Candidates   []struct {
			UPID            string `json:"upid"`
			Status          string `json:"status"`
			ExitStatus      string `json:"exit_status"`
			LogTargetStatus string `json:"log_target_status"`
		} `json:"candidates"`
	}
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.JobID != job.ID || len(result.Candidates) != 1 {
		t.Fatalf("discovery: %d %s", w.Code, w.Body)
	}
	if result.TargetStatus != "marker_matches" || result.Candidates[0].Status != "stopped" || result.Candidates[0].ExitStatus != "OK" {
		t.Fatalf("missing joint evidence: %+v", result)
	}
	if result.Candidates[0].LogTargetStatus != "matches" {
		t.Fatal("missing task target declaration")
	}
	for _, tc := range []struct {
		mode, want string
		code       int
	}{
		{"log_mismatch", "mismatch", 200}, {"log_unknown", "unrecognized", 200}, {"log_unavailable", "", 503},
	} {
		mode.Store(tc.mode)
		w := call("platform_admin")
		if w.Code != tc.code {
			t.Fatalf("%s: %d", tc.mode, w.Code)
		}
		if tc.code == 200 && (json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Candidates[0].LogTargetStatus != tc.want) {
			t.Fatalf("%s: %s", tc.mode, w.Body)
		}
	}
	for _, tc := range []struct {
		mode, target, status, exit string
		code                       int
	}{
		{"wrong_source", "", "", "", 503}, {"wrong_principal", "", "", "", 503},
		{"wrong_start", "", "", "", 503}, {"task_unavailable", "", "", "", 503},
		{"target_unavailable", "", "", "", 503},
		{"running", "marker_matches", "running", "", 200},
		{"failed", "marker_matches", "stopped", "clone failed", 200},
		{"mismatch", "target_mismatch", "stopped", "OK", 200},
		{"locked", "target_locked", "stopped", "OK", 200},
		{"absent", "target_absent", "stopped", "OK", 200},
	} {
		mode.Store(tc.mode)
		w := call("platform_admin")
		if w.Code != tc.code {
			t.Fatalf("%s: %d %s", tc.mode, w.Code, w.Body)
		}
		if tc.code == 200 {
			result.Candidates = nil
			if json.Unmarshal(w.Body.Bytes(), &result) != nil || result.TargetStatus != tc.target || len(result.Candidates) != 1 || result.Candidates[0].Status != tc.status || result.Candidates[0].ExitStatus != tc.exit {
				t.Fatalf("%s: %s", tc.mode, w.Body)
			}
		} else if strings.Contains(w.Body.String(), "candidates") {
			t.Fatalf("%s returned partial evidence", tc.mode)
		}
	}
	mode.Store("match")
	recoverCall := func(role, csrf string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/jobs/discover/clone-recovery", strings.NewReader(`{"upid":"UPID:source:1:1:1:qmclone:9202:admin@pve:","reason":"Verified isolated clone evidence"}`))
		r.SetPathValue("id", job.ID)
		r.Header.Set("X-CSRF-Token", csrf)
		actor := user
		actor.Role = role
		w := httptest.NewRecorder()
		s.recoverCloneTask(w, r, store.Session{User: actor, CSRFToken: "fixture-csrf"}, "")
		return w
	}
	for _, tc := range []struct{ role, csrf string }{{"user", "fixture-csrf"}, {"platform_admin", ""}} {
		count := requests.Load()
		if w := recoverCall(tc.role, tc.csrf); w.Code != 403 || requests.Load() != count {
			t.Fatalf("recovery authorization: %d", w.Code)
		}
	}
	for _, tc := range []struct {
		mode string
		code int
	}{
		{"running", 409}, {"failed", 409}, {"wrong_source", 409}, {"wrong_principal", 409}, {"outside_window", 409},
		{"log_unknown", 409}, {"log_mismatch", 409}, {"locked", 409}, {"mismatch", 409}, {"absent", 409},
		{"task_unavailable", 503}, {"log_unavailable", 503}, {"target_unavailable", 503},
	} {
		mode.Store(tc.mode)
		if w := recoverCall("platform_admin", "fixture-csrf"); w.Code != tc.code {
			t.Fatalf("recover %s: %d %s", tc.mode, w.Code, w.Body)
		}
	}
	mode.Store("match")
	fail.Store(true)
	if w := call("platform_admin"); w.Code != 503 {
		t.Fatalf("failure hidden: %d", w.Code)
	}
	saved, err := db.JobByID(t.Context(), job.ID)
	if err != nil || saved.State != "accepted" || saved.UPID != "" {
		t.Fatalf("candidate adopted: %+v %v", saved, err)
	}
	fail.Store(false)
	if w := recoverCall("platform_admin", "fixture-csrf"); w.Code != 202 {
		t.Fatalf("recovery: %d %s", w.Code, w.Body)
	}
	// The normal watcher must finish registration rather than the recovery
	// transaction directly marking the desktop successful.
	deadline := time.Now().Add(5 * time.Second)
	for {
		current, err := db.JobByID(t.Context(), job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.State == "succeeded" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("recovered clone did not settle: %s", current.State)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if w := recoverCall("platform_admin", "fixture-csrf"); w.Code != 409 {
		t.Fatalf("recovery replay: %d", w.Code)
	}
	count := requests.Load()
	if w := call("platform_admin"); w.Code != 409 || requests.Load() != count {
		t.Fatal("settled task was searched again")
	}
	job, _, err = db.CreateJob(t.Context(), store.Job{ID: "legacy", IdempotencyKey: "legacy", Operation: "pve.template_clone", State: "accepted", SourceVMID: 9202, TargetVMID: 9205, TaskNode: "source", Request: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if w := call("platform_admin"); w.Code != 409 || requests.Load() != count {
		t.Fatal("legacy job guessed the current PVE principal")
	}
	anon := httptest.NewRecorder()
	s.Handler().ServeHTTP(anon, httptest.NewRequest("GET", "/api/v1/jobs/discover/clone-candidates", nil))
	if anon.Code != 401 {
		t.Fatalf("anonymous status %d", anon.Code)
	}
	anon = httptest.NewRecorder()
	s.Handler().ServeHTTP(anon, httptest.NewRequest("POST", "/api/v1/jobs/discover/clone-recovery", strings.NewReader(`{}`)))
	if anon.Code != 401 {
		t.Fatalf("anonymous recovery status %d", anon.Code)
	}
}
