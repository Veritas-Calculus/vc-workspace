package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/Veritas-Calculus/vc-workspace/internal/auth"
	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
)

func TestImageCandidatesCannotEnterDesktopDataPlane(t *testing.T) {
	db, suffix := regressionDB(t)
	ctx := t.Context()
	user := store.User{ID: "images-" + suffix, Username: "images-" + suffix, DisplayName: "Images", PasswordHash: "unused"}
	if _, err := db.CreateLocalUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	agent, err := db.CreateAgentPrincipal(ctx, store.AgentPrincipal{ID: "agent-" + suffix, DisplayName: "Images"}, auth.TokenDigest("fixture-"+suffix))
	if err != nil {
		t.Fatal(err)
	}
	profile, err := db.ImageProfileByID(ctx, "debian-13-xfce")
	if err != nil {
		t.Fatal(err)
	}
	profile.TemplateVMID = 9202
	if _, err := db.UpdateImageProfile(ctx, profile); err != nil {
		t.Fatal(err)
	}
	// Old targets stay isolated after a profile switches to another VMID.
	for i, state := range []string{"accepted", "running", "failed"} {
		id := "build-" + state + "-" + suffix
		if _, _, err := db.CreateJob(ctx, store.Job{ID: id, IdempotencyKey: id, Operation: "image.build", State: state,
			TargetVMID: 9204 + i, TargetNode: "test", CreatedBy: user.ID,
			Request: json.RawMessage(fmt.Sprintf(`{"image_profile_id":%q}`, state))}); err != nil {
			t.Fatal(err)
		}
	}
	for _, vmid := range []int{9202, 9203, 9204, 9205, 9206} {
		if err := db.UpsertManagedDesktop(ctx, store.ManagedDesktop{VMID: vmid, DisplayName: "fixture", Node: "test", OSFamily: "linux", Enabled: true, Present: true}); err != nil {
			t.Fatal(err)
		}
		// Deliberately model stale assignments from the original faulty inventory.
		for _, subject := range []struct{ kind, id string }{{"user", user.ID}, {"agent", agent.ID}} {
			if _, err := db.PutDesktopAssignment(ctx, store.DesktopAssignment{SubjectType: subject.kind, SubjectID: subject.id, DesktopVMID: vmid}); err != nil {
				t.Fatal(err)
			}
		}
	}
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			t.Errorf("image candidate caused PVE mutation: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		switch r.URL.Path {
		case "/api2/json/version":
			_, _ = w.Write([]byte(`{"data":{"version":"9.2","release":"9"}}`))
		case "/api2/json/nodes", "/api2/json/cluster/status", "/api2/json/storage":
			_, _ = w.Write([]byte(`{"data":[]}`))
		case "/api2/json/nodes/test/qemu/9203/config":
			_, _ = w.Write([]byte(`{"data":{"ostype":"l26"}}`))
		case "/api2/json/cluster/resources":
			var machines []map[string]any
			for _, vmid := range []int{9202, 9203, 9204, 9205, 9206} {
				machines = append(machines, map[string]any{"vmid": vmid, "type": "qemu", "node": "test", "name": "fixture", "status": "running", "tags": "vc-workspace;template;rdp"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": machines})
		default:
			t.Errorf("unexpected PVE request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()
	client, err := pve.New(pve.Config{Endpoint: upstream.URL, TokenID: "test@pve!images", TokenSecret: "fixture", MutationsEnabled: true, HTTPClient: upstream.Client()})
	if err != nil {
		t.Fatal(err)
	}
	server := New(Dependencies{Store: db, PVE: client})
	summary, err := server.desktopInventory(ctx)
	if err != nil || len(summary.VMs) != 5 {
		t.Fatalf("infrastructure resources lost: %#v %v", summary, err)
	}
	for _, vm := range summary.VMs {
		if vm.Managed != (vm.VMID == 9203) {
			t.Errorf("wrong eligibility for %d: %v", vm.VMID, vm.Managed)
		}
	}
	for _, vmid := range []int{9202, 9204, 9205, 9206} {
		if _, err := server.managedVirtualMachine(ctx, vmid); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("reserved VM %d entered shared data-plane lookup: %v", vmid, err)
		}
		request := httptest.NewRequest(http.MethodPost, "/api/v1/virtual-machines/"+strconv.Itoa(vmid)+"/actions/stop", nil)
		request.SetPathValue("vmid", strconv.Itoa(vmid))
		request.SetPathValue("action", "stop")
		request.Header.Set("X-CSRF-Token", "csrf")
		request.Header.Set("Idempotency-Key", "stop-"+strconv.Itoa(vmid)+"-"+suffix)
		admin := user
		admin.Role = "platform_admin"
		response := httptest.NewRecorder()
		server.changeVirtualMachinePower(response, request, store.Session{User: admin, CSRFToken: "csrf"}, "")
		if response.Code != http.StatusNotFound {
			t.Fatalf("reserved VM power status: %d %s", response.Code, response.Body)
		}
	}
	for _, audience := range []string{"native", "agent", "web"} {
		t.Run(audience, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/?agent_id="+agent.ID, nil)
			switch audience {
			case "native":
				server.nativeDesktops(response, request, user, "")
			case "agent":
				server.agentDesktops(response, request)
			case "web":
				user.Role = "user"
				server.infrastructure(response, request, store.Session{User: user}, "")
			}
			var body struct {
				Desktops []pve.VM `json:"desktops"`
				VMs      []pve.VM `json:"virtual_machines"`
			}
			if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &body) != nil {
				t.Fatalf("desktop inventory response: %d %s", response.Code, response.Body)
			}
			desktops := append(body.Desktops, body.VMs...)
			if len(desktops) != 1 || desktops[0].VMID != 9203 || !desktops[0].Managed {
				t.Fatalf("builder exposed or legitimate template-tagged clone hidden: %#v", desktops)
			}
		})
	}
	// Reconciliation also retires old registry entries; it must not re-adopt
	// an installer merely because Packer already applied workspace tags.
	admin := user
	admin.Role = "platform_admin"
	request := httptest.NewRequest(http.MethodPost, "/api/v1/access-control/reconcile", nil)
	request.Header.Set("X-CSRF-Token", "csrf")
	response := httptest.NewRecorder()
	server.reconcileAccessControl(response, request, store.Session{User: admin, CSRFToken: "csrf"}, "")
	if response.Code != http.StatusOK {
		t.Fatalf("reconcile failed: %d %s", response.Code, response.Body)
	}
	desktops, err := db.ManagedDesktops(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, desktop := range desktops {
		if desktop.Present != (desktop.VMID == 9203) {
			t.Errorf("image candidate remained in present desktop registry: %#v", desktop)
		}
	}
}
