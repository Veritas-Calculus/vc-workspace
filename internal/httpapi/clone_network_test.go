package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
)

func TestCloneNetworkDefaults(t *testing.T) {
	for _, mode := range []string{"dhcp", "existing", "no_cloudinit", "windows", "no_nic", "no_digest", "wrong_target", "conflict", "response_lost", "not_applied"} {
		t.Run(mode, func(t *testing.T) {
			var writes atomic.Int32
			var applied atomic.Bool
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "windows" {
					t.Error("Windows cloudbase policy was modified")
				}
				if r.URL.Path != "/api2/json/nodes/test/qemu/9204/config" {
					t.Errorf("wrong path: %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodPut {
					writes.Add(1)
					if err := r.ParseForm(); err != nil {
						t.Error(err)
					}
					if len(r.Form) != 2 || r.Form.Get("ipconfig0") != "ip=dhcp" || r.Form.Get("digest") != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
						t.Errorf("unexpected network write: %v", r.Form)
					}
					if mode == "conflict" {
						w.WriteHeader(500)
						return
					}
					if mode != "not_applied" {
						applied.Store(true)
					}
					if mode == "response_lost" {
						w.WriteHeader(503)
						return
					}
					_, _ = w.Write([]byte(`{"data":null}`))
					return
				}
				cfg := map[string]any{"name": "desktop", "description": "VC Workspace clone job: network", "digest": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "ide1": "ceph:vm-9204-cloudinit,media=cdrom", "net0": "virtio=AA:BB:CC:DD:EE:FF,bridge=vmbr0", "ipconfig1": "ip=192.0.2.3/24"}
				if mode == "existing" {
					cfg["ipconfig0"] = "ip=192.0.2.2/24,gw=192.0.2.1"
				}
				if applied.Load() {
					cfg["ipconfig0"] = "ip=dhcp"
				}
				if mode == "no_cloudinit" {
					delete(cfg, "ide1")
				}
				if mode == "no_nic" {
					delete(cfg, "net0")
				}
				if mode == "no_digest" {
					delete(cfg, "digest")
				}
				if mode == "wrong_target" {
					cfg["description"] = "other job"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": cfg})
			}))
			defer upstream.Close()
			client, err := pve.New(pve.Config{Endpoint: upstream.URL, TokenID: "test@pve!fixture", TokenSecret: "fixture", MutationsEnabled: true, HTTPClient: upstream.Client()})
			if err != nil {
				t.Fatal(err)
			}
			server := New(Dependencies{PVE: client})
			request := desktopCloneRequest{Name: "desktop", SourceOSFamily: "linux"}
			if mode == "windows" {
				request.SourceOSFamily = "windows"
			}
			job := store.Job{ID: "network", TargetVMID: 9204, TargetNode: "test"}
			err = server.ensureCloneNetwork(t.Context(), job, request)
			wantError := mode == "no_nic" || mode == "no_digest" || mode == "wrong_target" || mode == "conflict" || mode == "response_lost" || mode == "not_applied"
			if (err != nil) != wantError {
				t.Fatalf("error = %v", err)
			}
			wantWrites := int32(0)
			if mode == "dhcp" || mode == "conflict" || mode == "response_lost" || mode == "not_applied" {
				wantWrites = 1
			}
			if writes.Load() != wantWrites {
				t.Fatalf("writes=%d want=%d", writes.Load(), wantWrites)
			}
			if mode == "dhcp" || mode == "response_lost" {
				if err := server.ensureCloneNetwork(t.Context(), job, request); err != nil {
					t.Fatal(err)
				}
				if writes.Load() != 1 {
					t.Fatal("replayed already applied network config")
				}
			}
		})
	}
}
