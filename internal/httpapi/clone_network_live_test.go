package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
)

type liveNetworkTransport struct {
	base   http.RoundTripper
	mu     sync.Mutex
	writes int
}

func (p *liveNetworkTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" || r.URL.Host != "pve.infra.plz.ac" {
		return nil, errors.New("unexpected live endpoint")
	}
	path := "/api2/json/nodes/infra-node6/qemu/9204"
	if (r.Method == "POST" && r.URL.Path == "/api2/json/access/ticket") || (r.Method == "GET" && (r.URL.Path == path+"/config" || r.URL.Path == path+"/status/current")) {
		return p.base.RoundTrip(r)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if r.Method != "PUT" || r.URL.Path != path+"/config" || p.writes != 0 {
		return nil, errors.New("live network mutation denied")
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
	if len(form) != 2 || form.Get("ipconfig0") != "ip=dhcp" || !pve.ValidConfigurationDigest(form.Get("digest")) {
		return nil, errors.New("live network write outside scope")
	}
	p.writes++
	r.Body = io.NopCloser(bytes.NewReader(body))
	return p.base.RoundTrip(r)
}

// Opt-in only: applies DHCP to the stopped, already-isolated VM9204. The
// exclusive journal blocks repeats; the lab runner restores the original config.
func TestLiveCloneNetworkDefaults(t *testing.T) {
	if os.Getenv("VC_WORKSPACE_LIVE_NETWORK") != "stopped-9204" {
		t.Skip("isolated network acceptance only")
	}
	fields, err := os.ReadFile(livePVECredentialFile(t))
	if err != nil {
		t.Fatal("credential file unavailable")
	}
	var username, password string
	parts := bytes.Fields(fields)
	if len(parts) != 3 {
		t.Fatal("unexpected credential format")
	}
	username, password = string(parts[0]), string(parts[2])
	labRoot := filepath.Join("..", "..", ".cache", "image-bootstrap-lab")
	data, err := os.ReadFile(filepath.Join(labRoot, "clone-recovery-attempt.json"))
	if err != nil {
		t.Fatal(err)
	}
	var attempt struct {
		Phase  string `json:"phase"`
		Target int    `json:"target"`
		JobID  string `json:"job_id"`
	}
	if json.Unmarshal(data, &attempt) != nil || attempt.Phase != "recovered" || attempt.Target != 9204 || attempt.JobID == "" {
		t.Fatal("invalid lab provenance")
	}
	transport := &liveNetworkTransport{base: http.DefaultTransport.(*http.Transport).Clone()}
	client, err := pve.New(pve.Config{Endpoint: "https://pve.infra.plz.ac", Username: username, Password: password, MutationsEnabled: true, HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	state, err := client.VMPowerState(t.Context(), "infra-node6", 9204)
	if err != nil || state != "stopped" {
		t.Fatal("target must be stopped")
	}
	before, err := client.VMConfiguration(t.Context(), "infra-node6", 9204)
	if err != nil {
		t.Fatal(err)
	}
	if before.Name != "vc-workspace-clone-recovery-check" || before.Description != "VC Workspace clone job: "+attempt.JobID || before.Lock != "" || before.Template || before.IPConfigs["ipconfig0"] != "" || !before.HasCloudInitDisk() {
		t.Fatal("target precondition failed")
	}
	journal, err := os.OpenFile(filepath.Join(labRoot, "live-network-submission.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = journal.WriteString(`{"vmid":9204,"phase":"submission-armed"}`)
	if err == nil {
		err = journal.Sync()
	}
	_ = journal.Close()
	if err != nil {
		t.Fatal(err)
	}
	server := New(Dependencies{PVE: client})
	job := store.Job{ID: attempt.JobID, TargetVMID: 9204, TargetNode: "infra-node6"}
	request := desktopCloneRequest{Name: before.Name, SourceOSFamily: "linux"}
	for range 2 {
		if err := server.ensureCloneNetwork(t.Context(), job, request); err != nil {
			t.Fatal(err)
		}
	}
	after, err := client.VMConfiguration(t.Context(), "infra-node6", 9204)
	if err != nil || after.IPConfigs["ipconfig0"] != "ip=dhcp" {
		t.Fatal("DHCP not observed")
	}
	if transport.writes != 1 {
		t.Fatal("network write repeated")
	}
	state, err = client.VMPowerState(t.Context(), "infra-node6", 9204)
	if err != nil || state != "stopped" {
		t.Fatal("target power changed")
	}
	t.Log("real clone network helper: DHCP observed, repeated reconciliation writes once, VM stopped; runner must restore ipconfig0")
}
