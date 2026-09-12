package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
	"github.com/Veritas-Calculus/vc-workspace/internal/testdb"
	"github.com/jackc/pgx/v5"
)

func TestCloneGPUFailureQuarantinesDesktop(t *testing.T) {
	databaseURL := testdb.URL(t)
	db, err := store.Open(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	fixtureDB, err := pgx.Connect(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer fixtureDB.Close(t.Context())
	suffix := "quarantine"
	var writes atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api2/json/version":
			_, _ = w.Write([]byte(`{"data":{"version":"9.2"}}`))
		case "/api2/json/nodes", "/api2/json/storage", "/api2/json/cluster/status":
			_, _ = w.Write([]byte(`{"data":[]}`))
		case "/api2/json/cluster/resources":
			_, _ = w.Write([]byte(`{"data":[{"vmid":9203,"node":"test","type":"qemu"}]}`))
		case "/api2/json/nodes/test/tasks/fixture-task/status":
			_, _ = w.Write([]byte(`{"data":{"status":"stopped","exitstatus":"OK"}}`))
		case "/api2/json/nodes/test/qemu/9203/config":
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(`{"data":{"name":"vc-workspace-failed-gpu","description":"VC Workspace clone job: gpu-quarantine","digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`))
				return
			}
			writes.Add(1)
			if r.Method != http.MethodPut {
				t.Errorf("unexpected configuration read: %s", r.Method)
			}
			w.WriteHeader(http.StatusInternalServerError)
		default:
			t.Errorf("unexpected PVE request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()
	client, err := pve.New(pve.Config{Endpoint: upstream.URL, TokenID: "test@pve!clone", TokenSecret: "fixture", MutationsEnabled: true, HTTPClient: upstream.Client()})
	if err != nil {
		t.Fatal(err)
	}
	server := New(Dependencies{Store: db, PVE: client})
	request, _ := json.Marshal(desktopCloneRequest{Name: "vc-workspace-failed-gpu", SourceVMID: 9202, SourceOSFamily: "linux", PCIResourceMapping: "test-gpu", MDevType: "i915-GVTg_V5_4"})
	job, _, err := db.CreateJob(t.Context(), store.Job{ID: "gpu-" + suffix, IdempotencyKey: "gpu-" + suffix, Operation: "pve.template_clone", State: "accepted", SourceVMID: 9202, TargetVMID: 9203, TargetNode: "test", TaskNode: "test", Request: request})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateJobTask(t.Context(), job.ID, "running", "fixture-task", ""); err != nil {
		t.Fatal(err)
	}
	// Inventory may discover the target before post-clone configuration ends.
	desktop := store.ManagedDesktop{VMID: 9203, DisplayName: "vc-workspace-failed-gpu", Node: "test", OSFamily: "linux"}
	if err := db.UpsertManagedDesktop(t.Context(), desktop); err != nil {
		t.Fatal(err)
	}
	user := store.User{ID: "gpu-user-" + suffix, Username: "gpu-user-" + suffix, DisplayName: "GPU test", PasswordHash: "unused"}
	if _, err := db.CreateLocalUser(t.Context(), user); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutDesktopAssignment(t.Context(), store.DesktopAssignment{SubjectType: "user", SubjectID: user.ID, DesktopVMID: 9203}); err != nil {
		t.Fatal(err)
	}
	if allowed, err := db.UserCanAccessDesktop(t.Context(), user.ID, 9203); err != nil || allowed {
		t.Fatalf("unfinished clone accessible: %v %v", allowed, err)
	}
	// Make the quarantine write fail after the upstream GPU failure. The
	// original task handle must remain observable, not become a final Job.
	if _, err := fixtureDB.Exec(t.Context(), `CREATE FUNCTION reject_quarantine_test() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NOT NEW.enabled THEN RAISE EXCEPTION 'injected quarantine write failure'; END IF;
 RETURN NEW; END $$;
 CREATE TRIGGER reject_quarantine_test BEFORE UPDATE ON managed_desktops FOR EACH ROW EXECUTE FUNCTION reject_quarantine_test()`); err != nil {
		t.Fatal(err)
	}
	if _, err := server.refreshPVEJob(t.Context(), job.ID); err == nil {
		t.Fatal("quarantine failure was hidden")
	}
	pending, err := db.JobByID(t.Context(), job.ID)
	if err != nil || pending.State != "running" || pending.UPID != "fixture-task" {
		t.Fatalf("failed registry lost pending task: %+v %v", pending, err)
	}
	before, err := db.ManagedDesktopByVMID(t.Context(), 9203)
	if err != nil || !before.Enabled {
		t.Fatalf("injection did not roll back registry: %+v %v", before, err)
	}
	if allowed, err := db.UserCanAccessDesktop(t.Context(), user.ID, 9203); err != nil || allowed {
		t.Fatalf("failed quarantine write reopened access: %v %v", allowed, err)
	}
	if _, err := fixtureDB.Exec(t.Context(), `DROP TRIGGER reject_quarantine_test ON managed_desktops`); err != nil {
		t.Fatal(err)
	}
	// A fresh handler instance resumes exclusively from persisted Job state.
	server = New(Dependencies{Store: db, PVE: client})
	job, err = server.refreshPVEJob(t.Context(), job.ID)
	if err != nil || job.State != "failed" {
		t.Fatalf("GPU failure accepted: %+v %v", job, err)
	}
	if err := db.ReconcileManagedDesktops(t.Context(), []store.ManagedDesktop{desktop}); err != nil {
		t.Fatal(err)
	}
	stored, err := db.ManagedDesktopByVMID(t.Context(), 9203)
	if err != nil || stored.Enabled || !stored.Present {
		t.Fatalf("failed clone escaped quarantine: %+v %v", stored, err)
	}
	if allowed, err := db.UserCanAccessDesktop(t.Context(), user.ID, 9203); err != nil || allowed {
		t.Fatalf("failed clone retained effective access: %v %v", allowed, err)
	}
	settledWrites := writes.Load()
	if _, err := server.refreshPVEJob(t.Context(), job.ID); err != nil {
		t.Fatal(err)
	}
	if writes.Load() != settledWrites {
		t.Fatal("terminal failed job replayed PVE configuration")
	}
}

func TestValidationCloneRequestBoundary(t *testing.T) {
	t.Run("completed", func(t *testing.T) { validationCloneRequestBoundary(t, false, false, false, false) })
	t.Run("lost_upstream_response", func(t *testing.T) { validationCloneRequestBoundary(t, true, false, false, false) })
	t.Run("task_persistence_failure", func(t *testing.T) { validationCloneRequestBoundary(t, false, true, false, false) })
	t.Run("lost_upstream_and_recovery_response", func(t *testing.T) { validationCloneRequestBoundary(t, true, false, true, false) })
	t.Run("task_persistence_failure_and_recovery_response_loss", func(t *testing.T) { validationCloneRequestBoundary(t, false, true, true, false) })
	t.Run("lost_network_response", func(t *testing.T) { validationCloneRequestBoundary(t, false, false, false, true) })
}

func validationCloneRequestBoundary(t *testing.T, loseResponse, failPersistence, loseRecoveryResponse, loseNetworkResponse bool) {
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
	suffix := "validation"
	if failPersistence {
		fixtureDB, err := pgx.Connect(t.Context(), databaseURL)
		if err != nil {
			t.Fatal(err)
		}
		defer fixtureDB.Close(t.Context())
		if _, err := fixtureDB.Exec(t.Context(), `CREATE FUNCTION reject_clone_handle_test() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.operation='pve.template_clone' AND NEW.state='running' THEN RAISE EXCEPTION 'injected task persistence failure'; END IF;
 RETURN NEW; END $$;
 CREATE TRIGGER reject_clone_handle_test BEFORE UPDATE ON pve_jobs FOR EACH ROW EXECUTE FUNCTION reject_clone_handle_test()`); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := db.CreateJob(t.Context(), store.Job{ID: "old-reservation", IdempotencyKey: "old-reservation", Operation: "pve.template_clone", State: "failed", SourceVMID: 9202, TargetVMID: 9203, Request: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	user := store.User{ID: "clone-" + suffix, Username: "clone-" + suffix, DisplayName: "Clone", PasswordHash: "unused"}
	err = db.CreateInitialAdmin(t.Context(), user)
	if err != nil {
		t.Fatal(err)
	}
	user.Role = "platform_admin"
	profile, err := db.ImageProfileByID(t.Context(), "debian-13-xfce")
	if err != nil {
		t.Fatal(err)
	}
	profile.TemplateVMID, profile.BuildStatus, profile.Enabled = 9202, "testing", true
	if _, err := db.UpdateImageProfile(t.Context(), profile); err != nil {
		t.Fatal(err)
	}
	var clones atomic.Int32
	var networkWrites atomic.Int32
	var dhcpApplied atomic.Bool
	t.Cleanup(func() {
		if !t.Failed() && (networkWrites.Load() != 1 || !dhcpApplied.Load()) {
			t.Errorf("clone did not settle through exactly one DHCP write: %d", networkWrites.Load())
		}
	})
	var cloneRecord atomic.Value
	const recoveredHandle = "UPID:test:1:1:1:qmclone:9202:test@pve!clone:"
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api2/json/version":
			_, _ = w.Write([]byte(`{"data":{"version":"9.2","release":"9"}}`))
		case "/api2/json/nodes":
			_, _ = w.Write([]byte(`{"data":[{"node":"test","status":"online"}]}`))
		case "/api2/json/storage", "/api2/json/cluster/status":
			_, _ = w.Write([]byte(`{"data":[]}`))
		case "/api2/json/cluster/resources":
			if clones.Load() > 0 {
				_, _ = w.Write([]byte(`{"data":[{"vmid":9202,"type":"qemu","node":"test","template":1,"name":"template"},{"vmid":9204,"type":"qemu","node":"test","name":"vc-workspace-validation"}]}`))
			} else {
				_, _ = w.Write([]byte(`{"data":[{"vmid":9202,"type":"qemu","node":"test","template":1,"name":"template"}]}`))
			}
		case "/api2/json/nodes/test/qemu/9204/config":
			j := cloneRecord.Load().(store.Job)
			if r.Method == http.MethodPut {
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				if len(r.Form) != 2 || r.Form.Get("ipconfig0") != "ip=dhcp" || r.Form.Get("digest") != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
					t.Errorf("unexpected clone configuration write: %v", r.Form)
				}
				pending, err := db.JobByID(r.Context(), j.ID)
				if err != nil || pending.State != "running" {
					t.Errorf("network write outside pending clone: %+v %v", pending, err)
				}
				networkWrites.Add(1)
				dhcpApplied.Store(true)
				if loseNetworkResponse {
					connection, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = connection.Close()
					return
				}
				_, _ = w.Write([]byte(`{"data":null}`))
				return
			}
			configuration := map[string]any{"name": "vc-workspace-validation", "description": "VC Workspace clone job: " + j.ID, "ostype": "l26", "ide1": "ceph:vm-9204-cloudinit,media=cdrom", "net0": "virtio=AA:BB:CC:DD:EE:FF,bridge=vmbr0", "digest": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
			if dhcpApplied.Load() {
				configuration["ipconfig0"] = "ip=dhcp"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": configuration})
		case "/api2/json/nodes/test/tasks/" + recoveredHandle + "/status":
			j := cloneRecord.Load().(store.Job)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": pve.TaskEvidence{UPID: recoveredHandle, Node: "test", Type: "qmclone", ID: "9202", User: "test@pve", TokenID: "clone", StartTime: j.CreatedAt.Unix(), Status: "stopped", ExitStatus: "OK"}})
		case "/api2/json/nodes/test/tasks/" + recoveredHandle + "/log":
			_, _ = w.Write([]byte(`{"data":[{"n":1,"t":"creating a clone of VM 9202 with ID 9204"}]}`))
		case "/api2/json/cluster/nextid":
			_, _ = w.Write([]byte(`{"data":"9203"}`))
		case "/api2/json/nodes/test/qemu/9202/clone":
			if r.Method != http.MethodPost {
				t.Errorf("clone method %s", r.Method)
			}
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			if r.Form.Get("full") != "1" || r.Form.Get("newid") != "9204" {
				t.Errorf("wrong clone parameters: %v", r.Form)
			}
			marker := strings.TrimPrefix(r.Form.Get("description"), "VC Workspace clone job: ")
			markedJob, err := db.JobByID(r.Context(), marker)
			if err != nil || markedJob.TargetVMID != 9204 || markedJob.State != "accepted" {
				t.Errorf("clone lacks durable job marker: %+v %v", markedJob, err)
			}
			cloneRecord.Store(markedJob)
			clones.Add(1)
			if loseResponse {
				// Accept/read the mutation, then close the real test connection
				// without providing a task handle. No client-side fake error.
				connection, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = connection.Close()
				return
			}
			// Source configuration changes after admission must not relabel
			// the already-admitted Linux clone as Windows at completion.
			changed := profile
			changed.OSFamily = "windows"
			if _, err := db.UpdateImageProfile(r.Context(), changed); err != nil {
				t.Error(err)
			}
			_, _ = w.Write([]byte(`{"data":"fixture-task"}`))
		case "/api2/json/nodes/test/tasks/fixture-task/status":
			_, _ = w.Write([]byte(`{"data":{"status":"stopped","exitstatus":"OK"}}`))
		default:
			t.Errorf("unexpected PVE operation %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()
	client, err := pve.New(pve.Config{Endpoint: upstream.URL, TokenID: "test@pve!clone", TokenSecret: "fixture", MutationsEnabled: true, HTTPClient: upstream.Client()})
	if err != nil {
		t.Fatal(err)
	}
	server := New(Dependencies{Store: db, PVE: client})
	call := func(body, key, role, csrf string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/desktop-instances", strings.NewReader(body))
		r.Header.Set("X-CSRF-Token", csrf)
		r.Header.Set("Idempotency-Key", key+suffix)
		u := user
		u.Role = role
		w := httptest.NewRecorder()
		server.createDesktopInstance(w, r, store.Session{User: u, CSRFToken: "csrf"}, "")
		return w
	}
	ordinary := `{"name":"vc-workspace-validation","source_vmid":9202,"full":true,"gpu_profile_id":"none"}`
	validation := strings.TrimSuffix(ordinary, "}") + `,"validation_image_profile_id":"debian-13-xfce","pci_resource_mapping":"untrusted","mdev_type":"untrusted","source_os_family":"windows","pve_principal":"attacker@pve!spoof"}`
	// No ordering of a ready and testing alias may change source admission.
	alias, err := db.ImageProfileByID(t.Context(), "windows-11")
	if err != nil {
		t.Fatal(err)
	}
	alias.TemplateVMID, alias.BuildStatus, alias.Enabled = 9202, "ready", true
	if _, err := db.UpdateImageProfile(t.Context(), alias); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ImageProfileByTemplateVMID(t.Context(), 9202); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("ambiguous template lookup returned %v", err)
	}
	for i, body := range []string{ordinary, validation} {
		w := call(body, "ambiguous-"+string(rune('a'+i)), "platform_admin", "csrf")
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "image_template_ambiguous") {
			t.Fatalf("ambiguous source accepted: %d %s", w.Code, w.Body)
		}
	}
	alias.TemplateVMID = 0
	if _, err := db.UpdateImageProfile(t.Context(), alias); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ImageProfileByTemplateVMID(t.Context(), 0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("zero VMID selected an unconfigured image: %v", err)
	}
	for _, c := range []struct {
		body, key, role, csrf string
		code                  int
	}{
		{ordinary, "ordinary", "platform_admin", "csrf", http.StatusConflict},
		{validation, "forbidden", "user", "csrf", http.StatusForbidden},
		{validation, "csrf", "platform_admin", "wrong", http.StatusForbidden},
		{strings.Replace(validation, `"full":true`, `"full":false`, 1), "linked", "platform_admin", "csrf", http.StatusConflict},
	} {
		if w := call(c.body, c.key, c.role, c.csrf); w.Code != c.code {
			t.Fatalf("%s: %d %s", c.key, w.Code, w.Body)
		}
	}
	if clones.Load() != 0 {
		t.Fatal("rejected request mutated PVE")
	}
	w := call(validation, "valid", "platform_admin", "csrf")
	var job store.Job
	if loseResponse || failPersistence {
		code, message := http.StatusBadGateway, "pve_clone_uncertain"
		if failPersistence {
			code, message = http.StatusServiceUnavailable, "clone_task_persistence_uncertain"
		}
		if w.Code != code || !strings.Contains(w.Body.String(), message) {
			t.Fatalf("uncertain response: %d %s", w.Code, w.Body)
		}
		// A new handler has no in-memory record of the first mutation.
		server = New(Dependencies{Store: db, PVE: client})
		for range 2 {
			replay := call(validation, "valid", "platform_admin", "csrf")
			if replay.Code != http.StatusAccepted || json.Unmarshal(replay.Body.Bytes(), &job) != nil || job.State != "accepted" || job.UPID != "" || job.TargetVMID != 9204 {
				t.Fatalf("lost reservation: %d %s", replay.Code, replay.Body)
			}
		}
		if clones.Load() != 1 {
			t.Fatalf("uncertain mutation replayed %d times", clones.Load())
		}
		if w := call(strings.Replace(validation, "vc-workspace-validation", "vc-workspace-changed", 1), "valid", "platform_admin", "csrf"); w.Code != http.StatusConflict {
			t.Fatalf("changed uncertain retry accepted: %d", w.Code)
		}
		if failPersistence {
			conn, err := pgx.Connect(t.Context(), databaseURL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = conn.Exec(t.Context(), `DROP TRIGGER reject_clone_handle_test ON pve_jobs`)
			_ = conn.Close(t.Context())
			if err != nil {
				t.Fatal(err)
			}
		}
		r := httptest.NewRequest("POST", "/api/v1/jobs/"+job.ID+"/clone-recovery", strings.NewReader(`{"upid":"`+recoveredHandle+`","reason":"Recover accepted mutation after injected response loss"}`))
		r.SetPathValue("id", job.ID)
		r.Header.Set("X-CSRF-Token", "csrf")
		recovery := httptest.NewRecorder()
		if loseRecoveryResponse {
			lost := &lostRecoveryResponse{ResponseRecorder: recovery}
			server.recoverCloneTask(lost, r, store.Session{User: user, CSRFToken: "csrf"}, "")
			if lost.writes != 1 || recovery.Body.Len() != 0 {
				t.Fatal("recovery response loss was not injected at body write")
			}
		} else {
			server.recoverCloneTask(recovery, r, store.Session{User: user, CSRFToken: "csrf"}, "")
		}
		if recovery.Code != 202 {
			t.Fatalf("uncertain clone recovery: %d %s", recovery.Code, recovery.Body)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			saved, err := db.JobByID(t.Context(), job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.State == "succeeded" {
				if saved.UPID != recoveredHandle {
					t.Fatal("wrong recovered handle")
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("recovery not settled: %s", saved.State)
			}
			time.Sleep(20 * time.Millisecond)
		}
		if clones.Load() != 1 {
			t.Fatalf("recovery issued %d clones", clones.Load())
		}
		// A newly constructed handler must expose the committed outcome without
		// relying on the recovery response or duplicating its audit event.
		server = New(Dependencies{Store: db, PVE: client})
		read := httptest.NewRequest("GET", "/api/v1/jobs/"+job.ID, nil)
		read.SetPathValue("id", job.ID)
		observed := httptest.NewRecorder()
		server.job(observed, read, store.Session{User: user}, "")
		var confirmed store.Job
		if observed.Code != 200 || observed.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(observed.Body.Bytes(), &confirmed) != nil || confirmed.State != "succeeded" || confirmed.UPID != recoveredHandle {
			t.Fatalf("committed recovery not observable: %d %s", observed.Code, observed.Body)
		}
		retry := httptest.NewRequest("POST", "/api/v1/jobs/"+job.ID+"/clone-recovery", strings.NewReader(`{"upid":"`+recoveredHandle+`","reason":"Retry after response loss"}`))
		retry.SetPathValue("id", job.ID)
		retry.Header.Set("X-CSRF-Token", "csrf")
		refused := httptest.NewRecorder()
		server.recoverCloneTask(refused, retry, store.Session{User: user, CSRFToken: "csrf"}, "")
		if refused.Code != 409 {
			t.Fatalf("recovery replay: %d", refused.Code)
		}
		if replay := call(validation, "valid", "platform_admin", "csrf"); replay.Code != 202 || clones.Load() != 1 {
			t.Fatal("post-recovery retry recloned")
		}
		conn, err := pgx.Connect(t.Context(), databaseURL)
		if err != nil {
			t.Fatal(err)
		}
		var auditCount int
		err = conn.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE event_type='job.clone_recovered' AND target_id=$1`, job.ID).Scan(&auditCount)
		_ = conn.Close(t.Context())
		if err != nil || auditCount != 1 {
			t.Fatalf("recovery audit count=%d error=%v", auditCount, err)
		}
		desktop, err := db.ManagedDesktopByVMID(t.Context(), job.TargetVMID)
		if err != nil || desktop.OSFamily != "linux" || !desktop.Present {
			t.Fatalf("recovered registry: %+v %v", desktop, err)
		}
		return
	}
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &job) != nil {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var stored desktopCloneRequest
	if json.Unmarshal(job.Request, &stored) != nil || stored.PCIResourceMapping != "" || stored.MDevType != "" || stored.ValidationImageProfileID != profile.ID || stored.SourceOSFamily != "linux" || stored.PVEPrincipal != "test@pve!clone" {
		t.Fatalf("untrusted placement or lost provenance: %s", job.Request)
	}
	if w := call(validation, "valid", "platform_admin", "csrf"); w.Code != http.StatusAccepted {
		t.Fatalf("replay: %d %s", w.Code, w.Body)
	}
	if clones.Load() != 1 {
		t.Fatalf("duplicate clone: %d", clones.Load())
	}
	if w := call(validation, "valid", "user", "csrf"); w.Code != http.StatusForbidden {
		t.Fatalf("replay bypassed current role: %d %s", w.Code, w.Body)
	}
	if w := call(strings.Replace(validation, "vc-workspace-validation", "vc-workspace-other", 1), "valid", "platform_admin", "csrf"); w.Code != http.StatusConflict {
		t.Fatalf("changed replay accepted: %d %s", w.Code, w.Body)
	}
	if clones.Load() != 1 {
		t.Fatal("rejected replay mutated PVE")
	}
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
			t.Fatalf("job not completed: %+v", current)
		}
		time.Sleep(20 * time.Millisecond)
	}
	profile, err = db.ImageProfileByID(t.Context(), profile.ID)
	if err != nil || profile.BuildStatus != "testing" {
		t.Fatalf("clone promoted image: %+v %v", profile, err)
	}
	desktop, err := db.ManagedDesktopByVMID(t.Context(), 9204)
	if err != nil || desktop.OSFamily != "linux" {
		t.Fatalf("mutable source changed cloned OS: %+v %v", desktop, err)
	}
}
