package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
)

// This opt-in acceptance has exactly one allowed mutation. Its durable journal
// prevents re-running after an uncertain result. It never starts/deletes a VM.
type cloneLossTransport struct {
	base     http.RoundTripper
	mu       sync.Mutex
	attempts int
	journal  string
	upid     string
}

func (p *cloneLossTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "pve.infra.plz.ac" || r.URL.Scheme != "https" {
		return nil, errors.New("unexpected live endpoint")
	}
	if r.Method == "GET" {
		// PVE validates the explicit isolated allocation, not a fabricated response.
		if r.URL.Path == "/api2/json/cluster/nextid" {
			r = r.Clone(r.Context())
			u := *r.URL
			u.RawQuery = "vmid=9204"
			r.URL = &u
		}
		return p.base.RoundTrip(r)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if r.Method != "POST" || r.URL.Path != "/api2/json/nodes/infra-node6/qemu/9202/clone" || p.attempts != 0 {
		return nil, errors.New("live mutation denied")
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	_ = r.Body.Close()
	form, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, err
	}
	if form.Get("newid") != "9204" || form.Get("full") != "1" || form.Get("name") != "vc-workspace-clone-recovery-check" || !strings.HasPrefix(form.Get("description"), "VC Workspace clone job: ") {
		return nil, errors.New("live clone scope mismatch")
	}
	record := map[string]any{"phase": "submitting", "target": 9204, "source": 9202, "job_id": strings.TrimPrefix(form.Get("description"), "VC Workspace clone job: "), "submitted_at": time.Now().UTC()}
	data, _ := json.Marshal(record)
	f, err := os.OpenFile(p.journal, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	_ = f.Close()
	if err != nil {
		return nil, err
	}
	p.attempts++
	r = r.Clone(r.Context())
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	response, err := p.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, errors.New("live clone request rejected; inspect PVE and journal")
	}
	var envelope struct {
		Data string `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) != nil || !strings.HasPrefix(envelope.Data, "UPID:") {
		return nil, errors.New("live clone returned no task")
	}
	p.upid = envelope.Data
	record["upid"] = p.upid
	record["phase"] = "response-dropped"
	data, _ = json.Marshal(record)
	if err := os.WriteFile(p.journal, data, 0600); err != nil {
		return nil, err
	}
	return nil, io.ErrUnexpectedEOF
}

func TestLiveCloneRecoveryResponseLoss(t *testing.T) {
	if os.Getenv("VC_WORKSPACE_LIVE_CLONE_RECOVERY") != "9202:9204" {
		t.Skip("explicit isolated live clone acceptance")
	}
	journal := os.Getenv("VC_WORKSPACE_LIVE_CLONE_JOURNAL")
	if journal == "" {
		t.Fatal("durable journal required")
	}
	if _, err := os.Stat(journal); !os.IsNotExist(err) {
		t.Fatal("existing journal: inspect, never repeat clone")
	}
	db, _ := regressionDB(t)
	user := store.User{ID: "live-clone-admin", Username: "live-clone-admin", DisplayName: "Live clone", PasswordHash: "unused", Role: "platform_admin"}
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
	if _, err = db.UpdateImageProfile(t.Context(), profile); err != nil {
		t.Fatal(err)
	}
	transport := &cloneLossTransport{base: http.DefaultTransport.(*http.Transport).Clone(), journal: journal}
	client, err := pve.New(pve.Config{Endpoint: "https://pve.infra.plz.ac", TokenID: os.Getenv("VC_WORKSPACE_LIVE_TASK_TOKEN_ID"), TokenSecret: os.Getenv("VC_WORKSPACE_LIVE_TASK_TOKEN_SECRET"), MutationsEnabled: true, HTTPClient: &http.Client{Transport: transport, Timeout: 30 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Minute)
	defer cancel()
	cfg, err := client.VMConfiguration(ctx, "infra-node6", 9202)
	if err != nil || !cfg.Template || cfg.Name != "vc-workspace-debian-13-xfce" || cfg.Lock != "" {
		t.Fatal("source template preflight failed")
	}
	s := New(Dependencies{Store: db, PVE: client})
	create := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/desktop-instances", strings.NewReader(`{"name":"vc-workspace-clone-recovery-check","source_vmid":9202,"target_node":"infra-node6","storage":"ceph-pve","full":true,"gpu_profile_id":"none","validation_image_profile_id":"debian-13-xfce"}`)).WithContext(ctx)
		r.Header.Set("X-CSRF-Token", "live-csrf")
		r.Header.Set("Idempotency-Key", "live-clone-recovery-9204")
		w := httptest.NewRecorder()
		s.createDesktopInstance(w, r, store.Session{User: user, CSRFToken: "live-csrf"}, "")
		return w
	}
	w := create()
	if w.Code != 502 || !strings.Contains(w.Body.String(), "pve_clone_uncertain") {
		t.Fatalf("expected uncertain response, got %d", w.Code)
	}
	s = New(Dependencies{Store: db, PVE: client})
	w = create()
	var job store.Job
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &job) != nil || job.State != "accepted" || job.UPID != "" || job.TargetVMID != 9204 {
		t.Fatal("restart/retry lost original reservation")
	}
	for {
		status, err := client.InspectTask(ctx, "infra-node6", transport.upid)
		if err != nil {
			t.Fatal("read live clone status:", err)
		}
		if status.Status == "stopped" {
			if status.ExitStatus != "OK" {
				t.Fatal("live clone did not succeed")
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("live clone still pending; inspect saved task")
		case <-time.After(time.Second):
		}
	}
	r := httptest.NewRequest("POST", "/api/v1/jobs/"+job.ID+"/clone-recovery", strings.NewReader(`{"upid":"`+transport.upid+`","reason":"Isolated real PVE response-loss acceptance"}`)).WithContext(ctx)
	r.SetPathValue("id", job.ID)
	r.Header.Set("X-CSRF-Token", "live-csrf")
	w = httptest.NewRecorder()
	s.recoverCloneTask(w, r, store.Session{User: user, CSRFToken: "live-csrf"}, "")
	if w.Code != 202 {
		t.Fatalf("live recovery refused: %d %s", w.Code, w.Body)
	}
	for {
		saved, err := db.JobByID(ctx, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if saved.State == "succeeded" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("recovered job did not settle")
		case <-time.After(100 * time.Millisecond):
		}
	}
	transport.mu.Lock()
	count := transport.attempts
	transport.mu.Unlock()
	if count != 1 {
		t.Fatal("recovery repeated clone")
	}
	if w := create(); w.Code != 202 {
		t.Fatal("post recovery replay failed")
	}
	final, err := client.VMConfiguration(ctx, "infra-node6", 9204)
	if err != nil || final.Lock != "" || final.Name != "vc-workspace-clone-recovery-check" || final.Description != "VC Workspace clone job: "+job.ID {
		t.Fatal("final target mismatch")
	}
	record, _ := json.Marshal(map[string]any{"phase": "recovered", "source": 9202, "target": 9204, "job_id": job.ID, "upid": transport.upid, "clone_count": count, "completed_at": time.Now().UTC()})
	if err := os.WriteFile(journal, record, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("real PVE clone response dropped; restarted handler retained reservation; recovery API and normal reconciliation succeeded with one clone; no VM start requested")
}
