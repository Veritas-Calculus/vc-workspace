package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Veritas-Calculus/vc-workspace/internal/agentapi"
	"github.com/Veritas-Calculus/vc-workspace/internal/computer"
)

func TestHTTPTransportAuthenticatesAgentAndListsTools(t *testing.T) {
	controlPlane := testControlPlane(t)
	defer controlPlane.Close()
	server, err := newMCPServer(controlPlane.URL, "internal-token", contextAgentIdentity)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := newHTTPHandler(controlPlane.URL, "internal-token", server)
	if err != nil {
		t.Fatal(err)
	}

	for _, authorization := range []string{"", "agent-token", "Bearer invalid-token"} {
		request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		request.Header.Set("Authorization", authorization)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Result().Header.Get("Cache-Control") != "no-store, no-transform" {
			t.Fatal("MCP authentication response must not be cached")
		}
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 for authorization %q, got %d", authorization, response.Code)
		}
	}

	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	request.Header.Set("Authorization", "Bearer agent-token")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Result().Header.Get("Cache-Control") != "no-store, no-transform" {
		t.Fatal("authenticated MCP response must not be cached")
	}
	if response.Code != http.StatusOK {
		t.Fatalf("expected authenticated MCP request to succeed, got %d: %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, `"desktop_list"`) || !strings.Contains(body, `"desktop_release"`) ||
		!strings.Contains(body, `"desktop_screenshot"`) || !strings.Contains(body, `"desktop_accessibility_snapshot"`) ||
		!strings.Contains(body, `"desktop_mouse"`) || !strings.Contains(body, `"desktop_key"`) || !strings.Contains(body, `"desktop_type_text"`) {
		t.Fatalf("expected tool catalogue, got %s", body)
	}
	if strings.Contains(body, `"inputSchema":{"type":"object","properties":{"agent_id"`) {
		t.Fatalf("agent identity must come from authentication, not tool input: %s", body)
	}
}

func TestHTTPTransportRejectsBrowserOrigins(t *testing.T) {
	controlPlane := testControlPlane(t)
	defer controlPlane.Close()
	server, err := newMCPServer(controlPlane.URL, "internal-token", contextAgentIdentity)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := newHTTPHandler(controlPlane.URL, "internal-token", server)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	request.Header.Set("Authorization", "Bearer agent-token")
	request.Header.Set("Origin", "https://untrusted.example")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for browser Origin, got %d", response.Code)
	}
}

func TestComputerToolBindsAuthenticatedAgentAndControlEpoch(t *testing.T) {
	var gotAction agentapi.ComputerAction
	controlPlane := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/agent/authenticate":
			if r.Header.Get("Authorization") != "Bearer internal-token" || r.Header.Get("X-VC-Workspace-Agent-Token") != "agent-token" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"agent-one","display_name":"Agent One","enabled":true}`))
		case "/api/v1/agent/desktop-leases/lease_abcdefghijklmnopqrstuvwxyz/computer-actions":
			if r.Header.Get("Authorization") != "Bearer internal-token" || r.URL.Query().Get("agent_id") != "agent-one" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if err := json.NewDecoder(r.Body).Decode(&gotAction); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(computer.Response{
				SchemaVersion: computer.SchemaVersion,
				RequestID:     "action_test",
				OK:            true,
				Input:         &computer.InputResult{Applied: true},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer controlPlane.Close()

	server, err := newMCPServer(controlPlane.URL, "internal-token", contextAgentIdentity)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := newHTTPHandler(controlPlane.URL, "internal-token", server)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{
		"jsonrpc":"2.0",
		"id":2,
		"method":"tools/call",
		"params":{
			"name":"desktop_type_text",
			"arguments":{
				"lease_id":"lease_abcdefghijklmnopqrstuvwxyz",
				"control_epoch":7,
				"text":"private task input",
				"sensitive":true,
				"timeout_ms":2000
			}
		}
	}`))
	request.Header.Set("Authorization", "Bearer agent-token")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected authenticated computer tool call to succeed, got %d: %s", response.Code, response.Body.String())
	}
	if gotAction.ControlEpoch != 7 || gotAction.Operation != computer.OperationTypeText || gotAction.TimeoutMS != 2000 || gotAction.Text == nil {
		t.Fatalf("unexpected forwarded action: %#v", gotAction)
	}
	if gotAction.Text.Value != "private task input" || !gotAction.Text.Sensitive {
		t.Fatalf("typed text policy was not forwarded: %#v", gotAction.Text)
	}
	if !strings.Contains(response.Body.String(), `"applied":true`) {
		t.Fatalf("expected computer action result, got %s", response.Body.String())
	}
}

func TestHTTPReadinessFollowsControlPlane(t *testing.T) {
	controlPlane := testControlPlane(t)
	defer controlPlane.Close()
	server, err := newMCPServer(controlPlane.URL, "internal-token", contextAgentIdentity)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := newHTTPHandler(controlPlane.URL, "internal-token", server)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/ready", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
}

func TestStdioIdentityComesFromAgentCredential(t *testing.T) {
	controlPlane := testControlPlane(t)
	defer controlPlane.Close()
	identity, err := newStdioAgentIdentity(controlPlane.URL, "internal-token", "agent-token")
	if err != nil {
		t.Fatal(err)
	}
	agentID, err := identity(t.Context())
	if err != nil || agentID != "agent-one" {
		t.Fatalf("agentID=%q err=%v", agentID, err)
	}
	invalidIdentity, err := newStdioAgentIdentity(controlPlane.URL, "internal-token", "invalid-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := invalidIdentity(t.Context()); err == nil {
		t.Fatal("expected invalid Agent credential to be rejected")
	}
}

func TestStdioIdentityRevalidatesCredentialForEveryToolCall(t *testing.T) {
	valid := true
	requests := 0
	controlPlane := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agent/authenticate" || r.Header.Get("Authorization") != "Bearer internal-token" || r.Header.Get("X-VC-Workspace-Agent-Token") != "agent-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		requests++
		if !valid {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"agent-one","display_name":"Agent One","enabled":true}`))
	}))
	defer controlPlane.Close()

	identity, err := newStdioAgentIdentity(controlPlane.URL, "internal-token", "agent-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := identity(t.Context()); err != nil {
		t.Fatal(err)
	}
	valid = false
	if _, err := identity(t.Context()); err == nil {
		t.Fatal("expected a rotated or revoked credential to fail without restarting stdio")
	}
	if requests != 2 {
		t.Fatalf("expected credential validation for both calls, got %d requests", requests)
	}
}

func testControlPlane(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/ready":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ready"}`))
		case "/api/v1/agent/authenticate":
			if r.Header.Get("Authorization") != "Bearer internal-token" || r.Header.Get("X-VC-Workspace-Agent-Token") != "agent-token" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":           "agent-one",
				"display_name": "Agent One",
				"enabled":      true,
			})
		default:
			http.NotFound(w, r)
		}
	}))
}
