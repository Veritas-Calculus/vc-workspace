package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
	"github.com/Veritas-Calculus/vc-workspace/internal/testdb"
	"github.com/jackc/pgx/v5"
)

func TestCloneRegistryRetryDoesNotReplayGPUWrite(t *testing.T) {
	for _, mdev := range []string{"", "i915-GVTg_V5_4"} {
		t.Run("mdev="+mdev, func(t *testing.T) {
			dsn := testdb.URL(t)
			db, err := store.Open(t.Context(), dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(db.Close)
			if err := db.Migrate(t.Context()); err != nil {
				t.Fatal(err)
			}
			conn, err := pgx.Connect(t.Context(), dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close(t.Context())
			request, _ := json.Marshal(desktopCloneRequest{Name: "resume-gpu", SourceOSFamily: "linux", PCIResourceMapping: "gpu", MDevType: mdev})
			job, _, err := db.CreateJob(t.Context(), store.Job{ID: "resume-gpu", IdempotencyKey: "resume-gpu", Operation: "pve.template_clone", State: "accepted", SourceVMID: 9202, TargetVMID: 9204, TargetNode: "test", TaskNode: "test", Request: request})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.RecordCloneTaskHandle(t.Context(), job, "task"); err != nil {
				t.Fatal(err)
			}
			// Inventory can discover the clone while post-clone setup is pending.
			if err := db.UpsertManagedDesktop(t.Context(), store.ManagedDesktop{VMID: 9204, DisplayName: "resume-gpu", Node: "test", OSFamily: "linux", Present: true, Enabled: true}); err != nil {
				t.Fatal(err)
			}
			user, err := db.CreateLocalUser(t.Context(), store.User{ID: "gpu-user", Username: "gpu-user", DisplayName: "GPU test", PasswordHash: "unused"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.PutDesktopAssignment(t.Context(), store.DesktopAssignment{SubjectType: "user", SubjectID: user.ID, DesktopVMID: 9204}); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.Exec(t.Context(), `CREATE FUNCTION fail_gpu_registry() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'registry unavailable'; END $$; CREATE TRIGGER fail_gpu_registry BEFORE INSERT ON managed_desktops FOR EACH ROW EXECUTE FUNCTION fail_gpu_registry()`); err != nil {
				t.Fatal(err)
			}
			var writes atomic.Int32
			var applied atomic.Value
			applied.Store("")
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api2/json/version":
					_, _ = w.Write([]byte(`{"data":{"version":"9.2"}}`))
				case "/api2/json/nodes", "/api2/json/storage", "/api2/json/cluster/status":
					_, _ = w.Write([]byte(`{"data":[]}`))
				case "/api2/json/cluster/resources":
					_, _ = w.Write([]byte(`{"data":[{"vmid":9204,"node":"test","type":"qemu"}]}`))
				case "/api2/json/nodes/test/tasks/task/status":
					_, _ = w.Write([]byte(`{"data":{"status":"stopped","exitstatus":"OK"}}`))
				case "/api2/json/nodes/test/qemu/9204/config":
					if r.Method == http.MethodPut {
						if err := r.ParseForm(); err != nil {
							t.Error(err)
						}
						if r.Form.Get("digest") != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
							t.Error("missing verified digest")
						}
						writes.Add(1)
						// A configuration read may serialize properties in a
						// different order from our request. Resume by meaning.
						parts := strings.Split(r.Form.Get("hostpci0"), ",")
						slices.Reverse(parts)
						applied.Store(strings.Join(parts, ","))
						_, _ = w.Write([]byte(`{"data":null}`))
					} else {
						_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"name": "resume-gpu", "description": "VC Workspace clone job: resume-gpu", "digest": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "hostpci0": applied.Load()}})
					}
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer upstream.Close()
			client, err := pve.New(pve.Config{Endpoint: upstream.URL, TokenID: "test@pve!fixture", TokenSecret: "fixture", MutationsEnabled: true, HTTPClient: upstream.Client()})
			if err != nil {
				t.Fatal(err)
			}
			server := New(Dependencies{Store: db, PVE: client})
			if _, err := server.refreshPVEJob(t.Context(), job.ID); err == nil {
				t.Fatal("registry fault was not reached")
			}
			if writes.Load() != 1 {
				t.Fatal("GPU write did not happen exactly once")
			}
			current, err := db.JobByID(t.Context(), job.ID)
			if err != nil || current.State != "running" {
				t.Fatal("registry failure lost pending job")
			}
			if allowed, err := db.UserCanAccessDesktop(t.Context(), user.ID, 9204); err != nil || allowed {
				t.Fatalf("pending clone accessible: %v %v", allowed, err)
			}
			if _, err := conn.Exec(t.Context(), `DROP TRIGGER fail_gpu_registry ON managed_desktops`); err != nil {
				t.Fatal(err)
			}
			server = New(Dependencies{Store: db, PVE: client})
			current, err = server.refreshPVEJob(t.Context(), job.ID)
			if err != nil || current.State != "succeeded" {
				t.Fatalf("registry resume failed: %+v %v", current, err)
			}
			if writes.Load() != 1 {
				t.Fatal("registry retry replayed GPU configuration")
			}
			if allowed, err := db.UserCanAccessDesktop(t.Context(), user.ID, 9204); err != nil || !allowed {
				t.Fatalf("completed clone not accessible: %v %v", allowed, err)
			}
		})
	}
}
