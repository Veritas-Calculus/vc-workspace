package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientDesktopAssignmentLifecycle(t *testing.T) {
	wanted := assignment{SubjectType: "group", SubjectID: "engineering", DesktopVMID: 158}
	assigned := false
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer vcwi_test" {
			t.Errorf("missing API token: %q", request.Header.Get("Authorization"))
		}
		switch request.Method + " " + request.URL.Path {
		case "PUT /api/v1/desktop-assignments/group/engineering/158":
			assigned = true
			response.Header().Set("Content-Type", "application/json")
			fmt.Fprint(response, `{"subject_type":"group","subject_id":"engineering","desktop_vmid":158}`)
		case "GET /api/v1/access-control":
			response.Header().Set("Content-Type", "application/json")
			if assigned {
				fmt.Fprint(response, `{"assignments":[{"subject_type":"group","subject_id":"engineering","desktop_vmid":158}],"desktops":[{"vmid":158,"display_name":"Debian","node":"node-1","os_family":"linux","present":true,"enabled":true,"access_mode":"shared"}]}`)
			} else {
				fmt.Fprint(response, `{"assignments":[],"desktops":[]}`)
			}
		case "DELETE /api/v1/desktop-assignments/group/engineering/158":
			assigned = false
			response.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	api, err := newClient(server.URL, "vcwi_test")
	if err != nil {
		t.Fatal(err)
	}
	if err := api.putAssignment(context.Background(), wanted); err != nil {
		t.Fatal(err)
	}
	current, err := api.accessControl(context.Background())
	if err != nil || len(current.Assignments) != 1 || len(current.Desktops) != 1 {
		t.Fatalf("unexpected read: state=%#v err=%v", current, err)
	}
	if err := api.deleteAssignment(context.Background(), wanted); err != nil {
		t.Fatal(err)
	}
	if assigned {
		t.Fatal("assignment was not removed")
	}
}

func TestValidateAssignment(t *testing.T) {
	valid := assignment{SubjectType: "agent", SubjectID: "research:agent.one", DesktopVMID: 201}
	if err := validateAssignment(valid); err != nil {
		t.Fatalf("valid assignment rejected: %v", err)
	}
	for _, invalid := range []assignment{
		{SubjectType: "service", SubjectID: "agent", DesktopVMID: 201},
		{SubjectType: "agent", SubjectID: "", DesktopVMID: 201},
		{SubjectType: "agent", SubjectID: "bad/path", DesktopVMID: 201},
		{SubjectType: "agent", SubjectID: "agent", DesktopVMID: 0},
	} {
		if err := validateAssignment(invalid); err == nil {
			t.Fatalf("invalid assignment accepted: %#v", invalid)
		}
	}
}

func TestNewClientRejectsWrongCredentialKind(t *testing.T) {
	if _, err := newClient("https://workspace.example.com", "vcwa_agent-token"); err == nil {
		t.Fatal("Agent credential was accepted as an IaC API credential")
	}
	configured, err := newClient("https://workspace.example.com/", "  vcwi_test  ")
	if err != nil || configured.endpoint != "https://workspace.example.com" || configured.token != "vcwi_test" {
		t.Fatalf("valid provider configuration was not normalized: client=%#v err=%v", configured, err)
	}
}
