package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
	"github.com/Veritas-Calculus/vc-workspace/internal/testdb"
	"github.com/jackc/pgx/v5"
)

func TestPVEJobMaintenanceResumesWithoutRequestWatcher(t *testing.T) {
	t.Run("new_instances", func(t *testing.T) { pveMaintenanceResume(t, false) })
	t.Run("committing_process_exits", func(t *testing.T) { pveMaintenanceResume(t, true) })
}

const maintenanceHandle = "UPID:test:1:1:1:qmclone:9202:test@pve:"

// Invoked only by the parent with its disposable schema. Exit immediately after
// the transaction returns: no watcher, deferred close or graceful shutdown.
func TestCloneRecoveryExitHelper(t *testing.T) {
	if os.Getenv("VCW_RECOVERY_EXIT_HELPER") != "commit-and-exit" {
		t.Skip("subprocess-only helper")
	}
	dsn := os.Getenv("VCW_RECOVERY_EXIT_DATABASE")
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || !regexp.MustCompile(`^vcw_test_[0-9a-f]{24}$`).MatchString(u.Query().Get("search_path")) {
		os.Exit(80)
	}
	db, err := store.Open(t.Context(), dsn)
	if err != nil {
		os.Exit(81)
	}
	job, err := db.JobByID(t.Context(), "resume-clone")
	if err != nil {
		os.Exit(82)
	}
	if err := db.CommitCloneRecovery(t.Context(), job, maintenanceHandle, "exit-admin", "Isolated process exit acceptance"); err != nil {
		os.Exit(83)
	}
	os.Exit(23)
}

func pveMaintenanceResume(t *testing.T, processExit bool) {
	databaseURL := testdb.URL(t)
	db, err := store.Open(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	job, _, err := db.CreateJob(t.Context(), store.Job{ID: "resume-clone", IdempotencyKey: "resume-clone", Operation: "pve.template_clone", State: "accepted", SourceVMID: 9202, TargetVMID: 9204, TargetNode: "test", TaskNode: "test", Request: json.RawMessage(`{"name":"vc-workspace-resume","source_os_family":"linux"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if processExit {
		if err := db.CreateInitialAdmin(t.Context(), store.User{ID: "exit-admin", Username: "exit-admin", PasswordHash: "unused", DisplayName: "Exit test"}); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCloneRecoveryExitHelper$")
		cmd.Env = append(os.Environ(), "VCW_RECOVERY_EXIT_HELPER=commit-and-exit", "VCW_RECOVERY_EXIT_DATABASE="+databaseURL)
		err := cmd.Run()
		var exited *exec.ExitError
		if !errors.As(err, &exited) || exited.ExitCode() != 23 {
			t.Fatalf("child did not exit at committed boundary: %v", err)
		}
		current, err := db.JobByID(t.Context(), job.ID)
		if err != nil || current.State != "running" || current.UPID != maintenanceHandle {
			t.Fatal("committed job did not survive process exit")
		}
	} else if err := db.RecordCloneTaskHandle(t.Context(), job, maintenanceHandle); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.CreateJob(t.Context(), store.Job{ID: "unknown-clone", IdempotencyKey: "unknown-clone", Operation: "pve.template_clone", State: "accepted", SourceVMID: 9202, TargetVMID: 9205, TargetNode: "test", TaskNode: "test", Request: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	var reads atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			switch r.URL.Path {
			case "/api2/json/version":
				_, _ = w.Write([]byte(`{"data":{"version":"9.2"}}`))
				return
			case "/api2/json/nodes", "/api2/json/storage", "/api2/json/cluster/status":
				_, _ = w.Write([]byte(`{"data":[]}`))
				return
			case "/api2/json/cluster/resources":
				_, _ = w.Write([]byte(`{"data":[{"vmid":9204,"node":"test","type":"qemu"}]}`))
				return
			case "/api2/json/nodes/test/qemu/9204/config":
				_, _ = w.Write([]byte(`{"data":{"name":"vc-workspace-resume","description":"VC Workspace clone job: resume-clone"}}`))
				return
			}
		}
		if r.Method != "GET" || r.URL.Path != "/api2/json/nodes/test/tasks/"+maintenanceHandle+"/status" {
			t.Errorf("unexpected maintenance operation: %s %s", r.Method, r.URL)
			w.WriteHeader(500)
			return
		}
		reads.Add(1)
		_, _ = w.Write([]byte(`{"data":{"status":"stopped","exitstatus":"OK"}}`))
	}))
	defer upstream.Close()
	client, err := pve.New(pve.Config{Endpoint: upstream.URL, TokenID: "test@pve!fixture", TokenSecret: "fixture", HTTPClient: upstream.Client()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{}, 2)
	for range 2 {
		replica := New(Dependencies{Store: db, PVE: client})
		go func() { defer func() { done <- struct{}{} }(); replica.RunPVEJobMaintenance(ctx) }()
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		current, err := db.JobByID(t.Context(), job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.State == "succeeded" {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("durable handle did not resume without a watcher or GET")
		case <-ticker.C:
		}
	}
	cancel()
	for range 2 {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("maintenance ignored cancellation")
		}
	}
	if reads.Load() != 1 {
		t.Fatalf("replicas repeated terminal observation: %d", reads.Load())
	}
	unknown, err := db.JobByID(t.Context(), "unknown-clone")
	if err != nil || unknown.State != "accepted" || unknown.UPID != "" {
		t.Fatal("unknown task auto-adopted")
	}
	desktop, err := db.ManagedDesktopByVMID(t.Context(), 9204)
	if err != nil || desktop.OSFamily != "linux" || !desktop.Present {
		t.Fatal("resumed clone not registered")
	}
	if processExit {
		conn, err := pgx.Connect(t.Context(), databaseURL)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(t.Context())
		var count int
		if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE event_type='job.clone_recovered' AND target_id='resume-clone'`).Scan(&count); err != nil || count != 1 {
			t.Fatalf("recovery audit after process exit: %d %v", count, err)
		}
	}
}
