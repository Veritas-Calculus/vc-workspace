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

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/Veritas-Calculus/vc-workspace/internal/agentapi"
	"github.com/Veritas-Calculus/vc-workspace/internal/auth"
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

type agentIdentity func(context.Context) (string, error)

type agentContextKey struct{}

func main() {
	apiURL := os.Getenv("VC_VDI_API_URL")
	if apiURL == "" {
		apiURL = "http://127.0.0.1:8080"
	}
	internalToken := os.Getenv("VC_VDI_INTERNAL_API_TOKEN")
	transport := strings.ToLower(strings.TrimSpace(os.Getenv("VC_VDI_MCP_TRANSPORT")))
	if transport == "" {
		transport = "stdio"
	}
	switch transport {
	case "stdio":
		identity, err := newStdioAgentIdentity(apiURL, internalToken, os.Getenv("VC_VDI_MCP_ACCESS_TOKEN"))
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
		address := os.Getenv("VC_VDI_MCP_HTTP_ADDR")
		if address == "" {
			address = "127.0.0.1:8090"
		}
		if err := runHTTP(address, apiURL, internalToken, server); err != nil {
			log.Fatal(err)
		}
	default:
		log.Fatalf("unsupported VC_VDI_MCP_TRANSPORT %q", transport)
	}
}

func newStdioAgentIdentity(apiURL, internalAPIToken, accessToken string) (agentIdentity, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil, errors.New("VC_VDI_MCP_ACCESS_TOKEN is required for stdio transport")
	}
	client, err := agentapi.New(apiURL, internalAPIToken)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context) (string, error) {
		agent, err := client.AuthenticateAgent(ctx, accessToken)
		if err != nil || !agent.Enabled {
			return "", errors.New("VC_VDI_MCP_ACCESS_TOKEN is invalid or disabled")
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
