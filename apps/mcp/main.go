package main

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/agentapi"
	"github.com/Veritas-Calculus/vc-workspace/internal/auth"
	"github.com/Veritas-Calculus/vc-workspace/internal/computer"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type noInput struct{}

type acquireInput struct {
	DesktopVMID int `json:"desktop_vmid" jsonschema:"PVE VMID returned by desktop_list"`
	TTLSeconds  int `json:"ttl_seconds,omitempty" jsonschema:"lease duration from 60 to 3600 seconds; defaults to 900"`
}

type leaseInput struct {
	LeaseID string `json:"lease_id" jsonschema:"lease identifier returned by desktop_acquire"`
}

type powerInput struct {
	LeaseID string `json:"lease_id" jsonschema:"active lease identifier"`
	Action  string `json:"action" jsonschema:"power action: start or stop"`
}

type screenshotInput struct {
	LeaseID      string `json:"lease_id" jsonschema:"active lease identifier"`
	ControlEpoch int64  `json:"control_epoch" jsonschema:"current control_epoch returned with the lease"`
	MaxWidth     int    `json:"max_width,omitempty" jsonschema:"maximum screenshot width in pixels, 320 to 3840; defaults to 1600"`
	TimeoutMS    int    `json:"timeout_ms,omitempty" jsonschema:"bounded action timeout in milliseconds, 250 to 15000"`
}

type accessibilityInput struct {
	LeaseID      string `json:"lease_id" jsonschema:"active lease identifier"`
	ControlEpoch int64  `json:"control_epoch" jsonschema:"current control_epoch returned with the lease"`
	MaxDepth     int    `json:"max_depth,omitempty" jsonschema:"maximum accessibility tree depth, 1 to 10; defaults to 6"`
	MaxNodes     int    `json:"max_nodes,omitempty" jsonschema:"maximum returned accessibility nodes, 1 to 500; defaults to 300"`
	TimeoutMS    int    `json:"timeout_ms,omitempty" jsonschema:"bounded action timeout in milliseconds, 250 to 15000"`
}

type mouseInput struct {
	LeaseID      string `json:"lease_id" jsonschema:"active lease identifier"`
	ControlEpoch int64  `json:"control_epoch" jsonschema:"current control_epoch returned with the lease"`
	Action       string `json:"action" jsonschema:"mouse action: move, click, or scroll"`
	X            int    `json:"x,omitempty" jsonschema:"absolute desktop x coordinate for move or click"`
	Y            int    `json:"y,omitempty" jsonschema:"absolute desktop y coordinate for move or click"`
	Button       string `json:"button,omitempty" jsonschema:"click button: left, right, or middle"`
	DeltaX       int    `json:"delta_x,omitempty" jsonschema:"horizontal scroll steps from -100 to 100"`
	DeltaY       int    `json:"delta_y,omitempty" jsonschema:"vertical scroll steps from -100 to 100"`
	TimeoutMS    int    `json:"timeout_ms,omitempty" jsonschema:"bounded action timeout in milliseconds, 250 to 15000"`
}

type keyInput struct {
	LeaseID      string   `json:"lease_id" jsonschema:"active lease identifier"`
	ControlEpoch int64    `json:"control_epoch" jsonschema:"current control_epoch returned with the lease"`
	Key          string   `json:"key" jsonschema:"allowlisted non-printable key, or one ASCII letter/digit when used with a modifier for a shortcut"`
	Modifiers    []string `json:"modifiers,omitempty" jsonschema:"zero or more unique modifiers: control, alt, shift, meta"`
	TimeoutMS    int      `json:"timeout_ms,omitempty" jsonschema:"bounded action timeout in milliseconds, 250 to 15000"`
}

type typeTextInput struct {
	LeaseID      string `json:"lease_id" jsonschema:"active lease identifier"`
	ControlEpoch int64  `json:"control_epoch" jsonschema:"current control_epoch returned with the lease"`
	Text         string `json:"text" jsonschema:"UTF-8 text to type, at most 4096 bytes; the value is never placed in audit logs"`
	Sensitive    bool   `json:"sensitive,omitempty" jsonschema:"mark text as sensitive for downstream redaction and policy"`
	TimeoutMS    int    `json:"timeout_ms,omitempty" jsonschema:"bounded action timeout in milliseconds, 250 to 15000"`
}

type agentIdentity func(context.Context) (string, error)

type agentContextKey struct{}

func main() {
	apiURL := workspaceEnv("VC_WORKSPACE_API_URL", "VC_VDI_API_URL")
	if apiURL == "" {
		apiURL = "http://127.0.0.1:8080"
	}
	internalToken := workspaceEnv("VC_WORKSPACE_INTERNAL_API_TOKEN", "VC_VDI_INTERNAL_API_TOKEN")
	transport := strings.ToLower(strings.TrimSpace(workspaceEnv("VC_WORKSPACE_MCP_TRANSPORT", "VC_VDI_MCP_TRANSPORT")))
	if transport == "" {
		transport = "stdio"
	}
	switch transport {
	case "stdio":
		identity, err := newStdioAgentIdentity(apiURL, internalToken, workspaceEnv("VC_WORKSPACE_MCP_ACCESS_TOKEN", "VC_VDI_MCP_ACCESS_TOKEN"))
		if err != nil {
			log.Fatal(err)
		}
		if _, err := identity(context.Background()); err != nil {
			log.Fatal(err)
		}
		server, err := newMCPServer(apiURL, internalToken, identity)
		if err != nil {
			log.Fatal(err)
		}
		if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			log.Fatal(err)
		}
	case "http":
		server, err := newMCPServer(apiURL, internalToken, contextAgentIdentity)
		if err != nil {
			log.Fatal(err)
		}
		address := workspaceEnv("VC_WORKSPACE_MCP_HTTP_ADDR", "VC_VDI_MCP_HTTP_ADDR")
		if address == "" {
			address = "127.0.0.1:8090"
		}
		if err := runHTTP(address, apiURL, internalToken, server); err != nil {
			log.Fatal(err)
		}
	default:
		log.Fatalf("unsupported VC_WORKSPACE_MCP_TRANSPORT %q", transport)
	}
}

func workspaceEnv(primary, legacy string) string {
	if value := os.Getenv(primary); value != "" {
		return value
	}
	return os.Getenv(legacy)
}

func newStdioAgentIdentity(apiURL, internalAPIToken, accessToken string) (agentIdentity, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil, errors.New("VC_WORKSPACE_MCP_ACCESS_TOKEN is required for stdio transport")
	}
	client, err := agentapi.New(apiURL, internalAPIToken)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context) (string, error) {
		agent, err := client.AuthenticateAgent(ctx, accessToken)
		if err != nil || !agent.Enabled {
			return "", errors.New("VC_WORKSPACE_MCP_ACCESS_TOKEN is invalid or disabled")
		}
		return agent.ID, nil
	}, nil
}

func newMCPServer(apiURL, internalAPIToken string, identity agentIdentity) (*mcp.Server, error) {
	client, err := agentapi.New(apiURL, internalAPIToken)
	if err != nil {
		return nil, err
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "vc-workspace", Version: "0.1.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name: "desktop_list", Description: "List VC Workspace-managed desktops that an AI agent may lease. This is read-only.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, agentapi.DesktopList, error) {
		agentID, err := identity(ctx)
		if err != nil {
			return nil, agentapi.DesktopList{}, err
		}
		output, err := client.ListDesktops(ctx, agentID)
		return nil, output, err
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "desktop_acquire", Description: "Acquire an exclusive, expiring lease for one managed desktop before controlling its lifecycle.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input acquireInput) (*mcp.CallToolResult, agentapi.Lease, error) {
		agentID, err := identity(ctx)
		if err != nil {
			return nil, agentapi.Lease{}, err
		}
		output, err := client.AcquireDesktop(ctx, agentID, input.DesktopVMID, input.TTLSeconds)
		return nil, output, err
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "desktop_lease_get", Description: "Read an existing desktop lease and verify that it remains active.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input leaseInput) (*mcp.CallToolResult, agentapi.Lease, error) {
		agentID, err := identity(ctx)
		if err != nil {
			return nil, agentapi.Lease{}, err
		}
		output, err := client.Lease(ctx, agentID, input.LeaseID)
		return nil, output, err
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "desktop_power", Description: "Start or stop a desktop covered by the caller's active lease. This mutates PVE state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input powerInput) (*mcp.CallToolResult, agentapi.Job, error) {
		agentID, err := identity(ctx)
		if err != nil {
			return nil, agentapi.Job{}, err
		}
		output, err := client.ChangePower(ctx, agentID, input.LeaseID, input.Action)
		return nil, output, err
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "desktop_release", Description: "Release an active desktop lease when the AI agent is done. This does not power off the desktop.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input leaseInput) (*mcp.CallToolResult, agentapi.Lease, error) {
		agentID, err := identity(ctx)
		if err != nil {
			return nil, agentapi.Lease{}, err
		}
		output, err := client.ReleaseDesktop(ctx, agentID, input.LeaseID)
		return nil, output, err
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "desktop_screenshot", Description: "Capture the primary monitor of the leased desktop as an MCP image. Metadata includes image dimensions and desktop_bounds in native input coordinates. Convert an image pixel (u,v) to mouse coordinates x=bounds.x+floor(u*bounds.width/image.width), y=bounds.y+floor(v*bounds.height/image.height). Use a fresh screenshot after any display change.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input screenshotInput) (*mcp.CallToolResult, screenshotOutput, error) {
		agentID, err := identity(ctx)
		if err != nil {
			return nil, screenshotOutput{}, err
		}
		output, err := client.ComputerAction(ctx, agentID, input.LeaseID, agentapi.ComputerAction{
			ControlEpoch: input.ControlEpoch, Operation: computer.OperationScreenshot, TimeoutMS: input.TimeoutMS,
			Screenshot: &computer.Screenshot{MaxWidth: input.MaxWidth},
		})
		if err != nil {
			return nil, screenshotOutput{}, err
		}
		return screenshotToolResult(output)
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "desktop_accessibility_snapshot", Description: "Read a bounded accessibility snapshot from the interactive desktop session. Use semantic information before coordinate input when available.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input accessibilityInput) (*mcp.CallToolResult, computer.Response, error) {
		agentID, err := identity(ctx)
		if err != nil {
			return nil, computer.Response{}, err
		}
		output, err := client.ComputerAction(ctx, agentID, input.LeaseID, agentapi.ComputerAction{
			ControlEpoch: input.ControlEpoch, Operation: computer.OperationAccessibility, TimeoutMS: input.TimeoutMS,
			Accessibility: &computer.Accessibility{MaxDepth: input.MaxDepth, MaxNodes: input.MaxNodes},
		})
		return nil, output, err
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "desktop_mouse", Description: "Move, click, or scroll in the leased desktop using native desktop coordinates, not resized image pixels. Use a recent accessibility node's coordinates directly, or convert screenshot pixels using its desktop_bounds and image dimensions. Observe again after a display change.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input mouseInput) (*mcp.CallToolResult, computer.Response, error) {
		agentID, err := identity(ctx)
		if err != nil {
			return nil, computer.Response{}, err
		}
		output, err := client.ComputerAction(ctx, agentID, input.LeaseID, agentapi.ComputerAction{
			ControlEpoch: input.ControlEpoch, Operation: computer.OperationMouse, TimeoutMS: input.TimeoutMS,
			Mouse: &computer.Mouse{Action: input.Action, X: input.X, Y: input.Y, Button: input.Button, DeltaX: input.DeltaX, DeltaY: input.DeltaY},
		})
		return nil, output, err
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "desktop_key", Description: "Send one allowlisted non-printable key chord to the leased interactive desktop.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input keyInput) (*mcp.CallToolResult, computer.Response, error) {
		agentID, err := identity(ctx)
		if err != nil {
			return nil, computer.Response{}, err
		}
		output, err := client.ComputerAction(ctx, agentID, input.LeaseID, agentapi.ComputerAction{
			ControlEpoch: input.ControlEpoch, Operation: computer.OperationKey, TimeoutMS: input.TimeoutMS,
			Key: &computer.Key{Key: input.Key, Modifiers: input.Modifiers},
		})
		return nil, output, err
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "desktop_type_text", Description: "Type bounded UTF-8 text into the leased interactive desktop. Text content is excluded from audit logs.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input typeTextInput) (*mcp.CallToolResult, computer.Response, error) {
		agentID, err := identity(ctx)
		if err != nil {
			return nil, computer.Response{}, err
		}
		output, err := client.ComputerAction(ctx, agentID, input.LeaseID, agentapi.ComputerAction{
			ControlEpoch: input.ControlEpoch, Operation: computer.OperationTypeText, TimeoutMS: input.TimeoutMS,
			Text: &computer.Text{Value: input.Text, Sensitive: input.Sensitive},
		})
		return nil, output, err
	})
	return server, nil
}

func contextAgentIdentity(ctx context.Context) (string, error) {
	agentID, ok := ctx.Value(agentContextKey{}).(string)
	if !ok || agentID == "" {
		return "", errors.New("authenticated agent identity is missing")
	}
	return agentID, nil
}

func runHTTP(address, apiURL, internalAPIToken string, mcpServer *mcp.Server) error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	handler, err := newHTTPHandler(apiURL, internalAPIToken, mcpServer)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      75 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		logger.Info("MCP HTTP server listening", "address", address)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("MCP HTTP server stopped unexpectedly", "error", err)
			stop()
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}

func newHTTPHandler(apiURL, internalAPIToken string, mcpServer *mcp.Server) (http.Handler, error) {
	agentClient, err := agentapi.New(apiURL, internalAPIToken)
	if err != nil {
		return nil, err
	}
	return newHTTPHandlerWithClient(apiURL, agentClient, mcpServer, &http.Client{Timeout: 3 * time.Second}), nil
}

func newHTTPHandlerWithClient(apiURL string, agentClient *agentapi.Client, mcpServer *mcp.Server, readyClient *http.Client) http.Handler {
	streamable := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return mcpServer
	}, &mcp.StreamableHTTPOptions{
		Stateless:                    true,
		JSONResponse:                 true,
		MaxRequestBodyBytes:          1 << 20,
		PropagateRequestCancellation: true,
	})
	readyURL := strings.TrimRight(apiURL, "/") + "/api/v1/ready"
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeStatus(w, http.StatusOK, "ok")
	})
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, readyURL, nil)
		if err != nil {
			writeStatus(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		response, err := readyClient.Do(request)
		if err != nil {
			writeStatus(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			writeStatus(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		writeStatus(w, http.StatusOK, "ready")
	})
	mux.Handle("/mcp", rejectBrowserOrigin(requireAgent(agentClient, streamable)))
	return mux
}

func requireAgent(client *agentapi.Client, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Control-plane headers aren't forwarded by the MCP SDK. Apply the same
		// no-storage boundary here for screenshots, semantic trees and leases.
		w = &privateMCPResponseWriter{ResponseWriter: w}
		w.Header().Set("Cache-Control", "no-store, no-transform")
		provided, ok := auth.ParseBearerToken(r.Header.Get("Authorization"))
		if !ok {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		agent, err := client.AuthenticateAgent(r.Context(), provided)
		if err != nil || !agent.Enabled {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), agentContextKey{}, agent.ID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func rejectBrowserOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" {
			http.Error(w, "browser origins are not allowed", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeStatus(w http.ResponseWriter, status int, value string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"status":"` + value + `"}`))
}
