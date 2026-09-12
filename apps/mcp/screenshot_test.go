package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/agentapi"
	"github.com/Veritas-Calculus/vc-workspace/internal/computer"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func screenshotFixture(t *testing.T) computer.Response {
	t.Helper()
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 640, 360)), nil); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded.Bytes())
	return computer.Response{
		SchemaVersion: computer.SchemaVersion, RequestID: "action_01234567890123456789", OK: true,
		Screenshot: &computer.ScreenshotResult{
			ContentType: "image/jpeg", Data: base64.StdEncoding.EncodeToString(encoded.Bytes()),
			Width: 640, Height: 360, SHA256: hex.EncodeToString(digest[:]),
			DesktopBounds: &computer.DesktopBounds{X: 100, Y: 50, Width: 1600, Height: 900},
		},
	}
}

func TestScreenshotResultRejectsMalformedObservations(t *testing.T) {
	for name, change := range map[string]func(*computer.Response){
		"failed":         func(r *computer.Response) { r.OK = false; r.Error = "private payload" },
		"missing":        func(r *computer.Response) { r.Screenshot = nil },
		"old_guest":      func(r *computer.Response) { r.Screenshot.DesktopBounds = nil },
		"mime":           func(r *computer.Response) { r.Screenshot.ContentType = "text/html" },
		"base64":         func(r *computer.Response) { r.Screenshot.Data = "private payload" },
		"digest":         func(r *computer.Response) { r.Screenshot.SHA256 = strings.Repeat("0", 64) },
		"wrong_width":    func(r *computer.Response) { r.Screenshot.Width++ },
		"wrong_height":   func(r *computer.Response) { r.Screenshot.Height++ },
		"huge_payload":   func(r *computer.Response) { r.Screenshot.Data = strings.Repeat("A", 12*1024*1024+1) },
		"missing_bounds": func(r *computer.Response) { r.Screenshot.DesktopBounds.Width = 0 },
		"huge_bounds":    func(r *computer.Response) { r.Screenshot.DesktopBounds.X = 20000 },
		"schema":         func(r *computer.Response) { r.SchemaVersion++ },
		"not_jpeg": func(r *computer.Response) {
			data := []byte("private payload")
			r.Screenshot.Data = base64.StdEncoding.EncodeToString(data)
			digest := sha256.Sum256(data)
			r.Screenshot.SHA256 = hex.EncodeToString(digest[:])
		},
	} {
		t.Run(name, func(t *testing.T) {
			response := screenshotFixture(t)
			change(&response)
			result, _, err := screenshotToolResult(response)
			if err == nil || result != nil || strings.Contains(err.Error(), "private payload") {
				t.Fatal("invalid screenshot was accepted or private data was exposed")
			}
		})
	}
}

// Uses actual SDK initialization and TCP HTTP, not a direct tools/call handler.
func TestScreenshotMCPWireImageMetadataAndCredentialRevalidation(t *testing.T) {
	fixture := screenshotFixture(t)
	var enabled atomic.Bool
	enabled.Store(true)
	var calls atomic.Int32
	var malformed atomic.Bool
	controlPlane := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer internal-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/agent/authenticate":
			if !enabled.Load() || r.Header.Get("X-VC-Workspace-Agent-Token") != "agent-token" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"id":"agent-one","enabled":true}`))
		case "/api/v1/agent/desktop-leases/lease_01234567890123456789/computer-actions":
			var action agentapi.ComputerAction
			if r.URL.Query().Get("agent_id") != "agent-one" || json.NewDecoder(r.Body).Decode(&action) != nil || action.Operation != computer.OperationScreenshot || action.ControlEpoch != 7 {
				t.Error("incorrect forwarded identity, operation or epoch")
				http.Error(w, "invalid", http.StatusBadRequest)
				return
			}
			calls.Add(1)
			response, shot := fixture, *fixture.Screenshot
			if malformed.Load() {
				shot.Data = "private invalid screenshot payload"
			}
			response.Screenshot = &shot
			_ = json.NewEncoder(w).Encode(response)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(controlPlane.Close)
	endpoint := testMCPHTTPServer(t, controlPlane.URL, "internal-token")
	session := testMCPClient(t, endpoint.URL+"/mcp", "agent-token")
	catalogue, err := session.ListTools(t.Context(), nil)
	if err != nil || len(catalogue.Tools) != 10 {
		t.Fatal("initialized MCP tool catalogue", err)
	}
	params := &mcp.CallToolParams{Name: "desktop_screenshot", Arguments: map[string]any{
		"lease_id": "lease_01234567890123456789", "control_epoch": 7, "max_width": 640,
	}}
	result, err := session.CallTool(t.Context(), params)
	if err != nil || result.IsError {
		t.Fatal("MCP screenshot failed", err)
	}
	metadata := checkMCPScreenshot(t, result)
	if metadata.Screenshot.DesktopBounds != *fixture.Screenshot.DesktopBounds {
		t.Fatal("native input coordinates did not survive the MCP wire")
	}
	malformed.Store(true)
	result, err = session.CallTool(t.Context(), params)
	if err != nil || !result.IsError {
		t.Fatal("malformed screenshot did not produce an MCP tool error", err)
	}
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); !ok || strings.Contains(text.Text, "private invalid") {
			t.Fatal("malformed image bytes leaked into MCP content")
		}
	}
	enabled.Store(false)
	result, err = session.CallTool(t.Context(), params)
	if err == nil && !result.IsError {
		t.Fatal("initialized session retained access after token revocation")
	}
	if calls.Load() != 2 {
		t.Fatal("revoked token reached computer action")
	}
}

func TestMCPWireCancellationReachesControlPlane(t *testing.T) {
	started, canceled := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	controlPlane := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/agent/authenticate":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"agent-one","enabled":true}`))
		case "/api/v1/agent/desktop-leases/lease_01234567890123456789/computer-actions":
			_, _ = io.Copy(io.Discard, r.Body)
			if calls.Add(1) != 1 {
				t.Error("canceled input was retried")
				return
			}
			close(started)
			select {
			case <-r.Context().Done():
			case <-t.Context().Done():
				return
			}
			close(canceled)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(controlPlane.Close)
	endpoint := testMCPHTTPServer(t, controlPlane.URL, "internal-token")
	session := testMCPClient(t, endpoint.URL+"/mcp", "agent-token")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "desktop_mouse", Arguments: mouseInput{
			LeaseID: "lease_01234567890123456789", ControlEpoch: 1, Action: "move", X: 50, Y: 50,
		}})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("input never reached the control plane")
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		controlPlane.CloseClientConnections()
		t.Fatal("MCP cancellation did not cancel the downstream request")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled call succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("MCP client did not finish cancellation")
	}
}

func checkMCPScreenshot(t *testing.T, result *mcp.CallToolResult) screenshotOutput {
	t.Helper()
	if result.IsError || len(result.Content) != 2 {
		t.Fatal("expected image plus compact text metadata")
	}
	im, ok := result.Content[0].(*mcp.ImageContent)
	if !ok || im.MIMEType != "image/jpeg" || len(im.Data) < 100 {
		t.Fatal("MCP did not return a native image content block")
	}
	text, ok := result.Content[1].(*mcp.TextContent)
	if !ok || len(text.Text) > 2048 || strings.Contains(text.Text, `"data"`) {
		t.Fatal("image payload leaked into text content")
	}
	data, err := json.Marshal(result.StructuredContent)
	var metadataText any
	if err != nil || json.Unmarshal([]byte(text.Text), &metadataText) != nil {
		t.Fatal("invalid screenshot metadata")
	}
	canonical, _ := json.Marshal(metadataText)
	if !bytes.Equal(data, canonical) {
		t.Fatal("structured and text screenshot metadata differ")
	}
	var metadata screenshotOutput
	if json.Unmarshal(data, &metadata) != nil || !metadata.OK || metadata.Screenshot.DesktopBounds.Width < 1 {
		t.Fatal("missing screenshot coordinate metadata")
	}
	config, err := jpeg.DecodeConfig(bytes.NewReader(im.Data))
	digest := sha256.Sum256(im.Data)
	if err != nil || config.Width != metadata.Screenshot.Width || config.Height != metadata.Screenshot.Height || hex.EncodeToString(digest[:]) != metadata.Screenshot.SHA256 {
		t.Fatal("MCP screenshot dimensions or digest mismatch")
	}
	return metadata
}

type mcpBearerTransport struct {
	base  http.RoundTripper
	token string
	t     *testing.T
}

func (transport mcpBearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	copy.Header.Set("Authorization", "Bearer "+transport.token)
	response, err := transport.base.RoundTrip(copy)
	if err == nil && response.Header.Get("Cache-Control") != "no-store, no-transform" {
		transport.t.Error("MCP wire response permits private data storage")
	}
	return response, err
}

func testMCPHTTPServer(t *testing.T, apiURL, internalToken string) *httptest.Server {
	t.Helper()
	server, err := newMCPServer(apiURL, internalToken, contextAgentIdentity)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := newHTTPHandler(apiURL, internalToken, server)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := httptest.NewServer(handler)
	t.Cleanup(endpoint.Close)
	return endpoint
}

func testMCPClient(t *testing.T, endpoint, token string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "vc-workspace-acceptance", Version: "0.1.0"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: endpoint, DisableStandaloneSSE: true, MaxRetries: -1,
		HTTPClient: &http.Client{Timeout: 75 * time.Second, Transport: mcpBearerTransport{base: http.DefaultTransport, token: token, t: t}},
	}, nil)
	if err != nil {
		t.Fatal("initialize authenticated MCP session", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}
