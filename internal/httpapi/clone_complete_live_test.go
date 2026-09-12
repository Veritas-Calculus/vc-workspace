package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
)

type completeCloneTransport struct {
	base                http.RoundTripper
	mu                  sync.Mutex
	clone, network, gpu int
	journal             string
}

func (p *completeCloneTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" || r.URL.Host != "pve.infra.plz.ac" {
		return nil, errors.New("unexpected live endpoint")
	}
	if r.Method == "GET" {
		if r.URL.Path == "/api2/json/cluster/nextid" {
			r = r.Clone(r.Context())
			u := *r.URL
			u.RawQuery = "vmid=9205"
			r.URL = &u
		}
		return p.base.RoundTrip(r)
	}
	if r.Method == "POST" && r.URL.Path == "/api2/json/access/ticket" {
		return p.base.RoundTrip(r)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	_ = r.Body.Close()
	form, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, err
	}
	switch {
	case r.Method == "POST" && r.URL.Path == "/api2/json/nodes/infra-node6/qemu/9202/clone" && p.clone == 0:
		if form.Get("newid") != "9205" || form.Get("name") != "vc-workspace-complete-clone-check" || form.Get("full") != "1" || !strings.HasPrefix(form.Get("description"), "VC Workspace clone job: ") {
			return nil, errors.New("clone scope mismatch")
		}
		file, err := os.OpenFile(p.journal, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		record, _ := json.Marshal(map[string]any{"phase": "submitting", "target": 9205, "source": 9202, "job_id": strings.TrimPrefix(form.Get("description"), "VC Workspace clone job: ")})
		_, err = file.Write(record)
		if err == nil {
			err = file.Sync()
		}
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		p.clone++
	case r.Method == "PUT" && r.URL.Path == "/api2/json/nodes/infra-node6/qemu/9205/config" && len(form) == 2 && pve.ValidConfigurationDigest(form.Get("digest")):
		if form.Get("ipconfig0") == "ip=dhcp" && p.network == 0 {
			p.network++
		} else if form.Get("hostpci0") == "mapping=vc-vdi-intel-gvtg,mdev=i915-GVTg_V5_4" && p.gpu == 0 {
			p.gpu++
		} else {
			return nil, errors.New("config mutation outside scope")
		}
	default:
		return nil, errors.New("live mutation denied")
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return p.base.RoundTrip(r)
}

// livePVECredentialFile locates the explicitly provided PVE credential file.
// The path is operator-specific, so it never gets a default.
func livePVECredentialFile(t *testing.T) string {
	t.Helper()
	path := os.Getenv("VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE")
	if path == "" {
		t.Fatal("VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE unset")
	}
	return path
}

func TestLiveCompleteClone(t *testing.T) {
	if os.Getenv("VC_WORKSPACE_LIVE_COMPLETE_CLONE") != "9202:9205" {
		t.Skip("explicit isolated clone acceptance")
	}
	journal := filepath.Join("..", "..", ".cache", "image-bootstrap-lab", "complete-clone-attempt.json")
	if _, err := os.Stat(journal); !os.IsNotExist(err) {
		t.Fatal("existing submission: inspect before any retry")
	}
	data, err := os.ReadFile(livePVECredentialFile(t))
	if err != nil {
		t.Fatal("credential file unavailable")
	}
	fields := bytes.Fields(data)
	if len(fields) != 3 {
		t.Fatal("unexpected credential format")
	}
	transport := &completeCloneTransport{base: http.DefaultTransport.(*http.Transport).Clone(), journal: journal}
	client, err := pve.New(pve.Config{Endpoint: "https://pve.infra.plz.ac", Username: string(fields[0]), Password: string(fields[2]), MutationsEnabled: true, HTTPClient: &http.Client{Transport: transport, Timeout: 30 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	summary, err := client.Summary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, vm := range summary.VMs {
		if vm.VMID == 9205 {
			t.Fatal("target already exists")
		}
	}
	cfg, err := client.VMConfiguration(t.Context(), "infra-node6", 9202)
	if err != nil || !cfg.Template || cfg.Lock != "" || cfg.Name != "vc-workspace-debian-13-xfce" {
		t.Fatal("template preflight failed")
	}
	db, _ := regressionDB(t)
	user := store.User{ID: "live-complete-admin", Username: "live-complete-admin", DisplayName: "Live clone", PasswordHash: "unused", Role: "platform_admin"}
	if err := db.CreateInitialAdmin(t.Context(), user); err != nil {
		t.Fatal(err)
	}
	profile, err := db.ImageProfileByID(t.Context(), "debian-13-xfce")
	if err != nil {
		t.Fatal(err)
	}
	profile.TemplateVMID = 9202
	profile.Enabled = true
	profile.BuildStatus = "testing"
	if _, err := db.UpdateImageProfile(t.Context(), profile); err != nil {
		t.Fatal(err)
	}
	gpu, err := db.GPUProfileByID(t.Context(), "intel-gvtg-v5-4")
	if err != nil {
		t.Fatal(err)
	}
	gpu.Enabled = true
	gpu.ResourceMapping = "vc-vdi-intel-gvtg"
	if _, err := db.UpdateGPUProfile(t.Context(), gpu); err != nil {
		t.Fatal(err)
	}
	server := New(Dependencies{Store: db, PVE: client})
	create := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/desktop-instances", strings.NewReader(`{"name":"vc-workspace-complete-clone-check","source_vmid":9202,"target_node":"infra-node6","storage":"ceph-pve","full":true,"gpu_profile_id":"intel-gvtg-v5-4","validation_image_profile_id":"debian-13-xfce"}`))
		r.Header.Set("X-CSRF-Token", "live-csrf")
		r.Header.Set("Idempotency-Key", "live-complete-9205")
		w := httptest.NewRecorder()
		server.createDesktopInstance(w, r, store.Session{User: user, CSRFToken: "live-csrf"}, "")
		return w
	}
	w := create()
	var job store.Job
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &job) != nil || job.TargetVMID != 9205 {
		t.Fatalf("clone admission failed: %d %s", w.Code, w.Body)
	}
	deadline := time.NewTimer(4 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		current, err := db.JobByID(t.Context(), job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.State == "succeeded" {
			job = current
			break
		}
		if current.State == "failed" {
			t.Fatalf("clone failed: %s", current.Error)
		}
		select {
		case <-deadline.C:
			t.Fatal("clone completion timeout; inspect retained job journal")
		case <-ticker.C:
		}
	}
	if replay := create(); replay.Code != 202 {
		t.Fatal("idempotent retry failed")
	}
	final, err := client.VMConfiguration(t.Context(), "infra-node6", 9205)
	if err != nil || final.IPConfigs["ipconfig0"] != "ip=dhcp" || !pve.MatchesGPUResourceMapping(final.PCIHostDevices["hostpci0"], "vc-vdi-intel-gvtg", "i915-GVTg_V5_4") {
		t.Fatal("final network/GPU configuration missing")
	}
	state, err := client.VMPowerState(t.Context(), "infra-node6", 9205)
	if err != nil || state != "stopped" {
		t.Fatal("clone power changed")
	}
	desktop, err := db.ManagedDesktopByVMID(t.Context(), 9205)
	if err != nil || !desktop.Enabled || !desktop.Present || desktop.OSFamily != "linux" {
		t.Fatal("desktop not registered")
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.clone != 1 || transport.network != 1 || transport.gpu != 1 {
		t.Fatal("unexpected mutation counts")
	}
	record, _ := json.Marshal(map[string]any{"phase": "configured", "target": 9205, "source": 9202, "job_id": job.ID, "upid": job.UPID, "clone_writes": 1, "network_writes": 1, "gpu_writes": 1, "vm_status": state})
	if err := os.WriteFile(journal, record, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("real clone succeeded: one clone, one DHCP write, one GPU write, desktop registered, target stopped")
}
