package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
	"github.com/Veritas-Calculus/vc-workspace/internal/testdb"
)

func TestCloneFinalizationRejectsChangedTarget(t *testing.T) {
	for _, mode := range []string{"absent", "node", "template", "name", "marker", "locked", "unavailable", "missing_digest", "invalid_digest", "changed_after_gpu"} {
		t.Run(mode, func(t *testing.T) {
			db, err := store.Open(t.Context(), testdb.URL(t))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(db.Close)
			if err := db.Migrate(t.Context()); err != nil {
				t.Fatal(err)
			}
			request, _ := json.Marshal(desktopCloneRequest{Name: "clone-check", SourceOSFamily: "linux", PCIResourceMapping: "gpu"})
			job, _, err := db.CreateJob(t.Context(), store.Job{ID: "finalize", IdempotencyKey: "finalize", Operation: "pve.template_clone", State: "accepted", SourceVMID: 9202, TargetVMID: 9204, TargetNode: "test", TaskNode: "test", Request: request})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.RecordCloneTaskHandle(t.Context(), job, "fixture-task"); err != nil {
				t.Fatal(err)
			}
			var writes atomic.Int32
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api2/json/version":
					_, _ = w.Write([]byte(`{"data":{"version":"9.2"}}`))
				case "/api2/json/nodes", "/api2/json/storage", "/api2/json/cluster/status":
					_, _ = w.Write([]byte(`{"data":[]}`))
				case "/api2/json/nodes/test/tasks/fixture-task/status":
					_, _ = w.Write([]byte(`{"data":{"status":"stopped","exitstatus":"OK"}}`))
				case "/api2/json/cluster/resources":
					vms := []map[string]any{}
					if mode != "absent" {
						vm := map[string]any{"vmid": 9204, "node": "test", "type": "qemu"}
						if mode == "node" {
							vm["node"] = "other"
						}
						if mode == "template" {
							vm["template"] = 1
						}
						vms = append(vms, vm)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"data": vms})
				case "/api2/json/nodes/test/qemu/9204/config":
					if r.Method == http.MethodPut {
						if err := r.ParseForm(); err != nil {
							t.Error(err)
						}
						if r.Form.Get("digest") != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
							t.Error("GPU write lost verified digest")
						}
						writes.Add(1)
						_, _ = w.Write([]byte(`{"data":null}`))
						return
					}
					if mode == "unavailable" {
						w.WriteHeader(503)
						return
					}
					cfg := map[string]any{"name": "clone-check", "description": "VC Workspace clone job: finalize", "digest": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
					if mode == "missing_digest" {
						delete(cfg, "digest")
					}
					if mode == "invalid_digest" {
						cfg["digest"] = "invalid"
					}
					if mode == "name" {
						cfg["name"] = "replacement"
					}
					if mode == "marker" || (mode == "changed_after_gpu" && writes.Load() > 0) {
						cfg["description"] = "replacement"
					}
					if mode == "locked" {
						cfg["lock"] = "backup"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"data": cfg})
				default:
					t.Errorf("unexpected PVE operation: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer upstream.Close()
			client, err := pve.New(pve.Config{Endpoint: upstream.URL, TokenID: "test@pve!fixture", TokenSecret: "fixture", MutationsEnabled: true, HTTPClient: upstream.Client()})
			if err != nil {
				t.Fatal(err)
			}
			server := New(Dependencies{Store: db, PVE: client})
			for range 2 {
				if _, err := server.refreshPVEJob(t.Context(), job.ID); err == nil {
					t.Fatal("unverified target accepted")
				}
			}
			wantWrites := int32(0)
			if mode == "changed_after_gpu" {
				wantWrites = 1
			}
			if writes.Load() != wantWrites {
				t.Fatalf("unexpected GPU writes: %d", writes.Load())
			}
			current, err := db.JobByID(t.Context(), job.ID)
			if err != nil || current.State != "running" || current.UPID != "fixture-task" {
				t.Fatalf("job lost pending identity: %+v %v", current, err)
			}
			if _, err := db.ManagedDesktopByVMID(t.Context(), 9204); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("unverified desktop registered: %v", err)
			}
		})
	}
}
