package agentapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/auth"
	"github.com/Veritas-Calculus/vc-workspace/internal/computer"
)

type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

type Desktop struct {
	VMID        int    `json:"vmid"`
	Name        string `json:"name"`
	Node        string `json:"node"`
	Status      string `json:"status"`
	CPUCount    int    `json:"cpu_count"`
	MemoryTotal int64  `json:"memory_total"`
}

type DesktopList struct {
	Desktops []Desktop `json:"desktops"`
}

type AgentPrincipal struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Enabled     bool   `json:"enabled"`
}

type Lease struct {
	ID           string    `json:"id"`
	AgentID      string    `json:"agent_id"`
	DesktopID    string    `json:"desktop_id"`
	State        string    `json:"state"`
	ControlEpoch int64     `json:"control_epoch"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type Job struct {
	ID         string `json:"id"`
	Operation  string `json:"operation"`
	State      string `json:"state"`
	TargetVMID int    `json:"target_vmid"`
	TargetNode string `json:"target_node"`
	Error      string `json:"error,omitempty"`
}

type ComputerAction struct {
	ControlEpoch  int64                   `json:"control_epoch"`
	Operation     computer.Operation      `json:"operation"`
	TimeoutMS     int                     `json:"timeout_ms,omitempty"`
	Screenshot    *computer.Screenshot    `json:"screenshot,omitempty"`
	Accessibility *computer.Accessibility `json:"accessibility,omitempty"`
	Mouse         *computer.Mouse         `json:"mouse,omitempty"`
	Key           *computer.Key           `json:"key,omitempty"`
	Text          *computer.Text          `json:"text,omitempty"`
}

func New(baseURL, token string) (*Client, error) {
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("VC_WORKSPACE_API_URL must be an absolute HTTP(S) URL")
	}
	if token == "" {
		return nil, fmt.Errorf("VC_WORKSPACE_INTERNAL_API_TOKEN is required")
	}
	return &Client{baseURL: parsed.String(), token: token, httpClient: &http.Client{Timeout: 45 * time.Second}}, nil
}

func (c *Client) AuthenticateAgent(ctx context.Context, accessToken string) (AgentPrincipal, error) {
	var output AgentPrincipal
	err := c.doWithHeaders(ctx, http.MethodPost, "/api/v1/agent/authenticate", nil, &output, "", map[string]string{"X-VC-Workspace-Agent-Token": accessToken})
	return output, err
}

func (c *Client) ListDesktops(ctx context.Context, agentID string) (DesktopList, error) {
	var output DesktopList
	err := c.do(ctx, http.MethodGet, "/api/v1/agent/desktops?agent_id="+url.QueryEscape(agentID), nil, &output, "")
	return output, err
}

func (c *Client) AcquireDesktop(ctx context.Context, agentID string, desktopVMID, ttlSeconds int) (Lease, error) {
	var output Lease
	input := map[string]any{"agent_id": agentID, "desktop_vmid": desktopVMID, "ttl_seconds": ttlSeconds}
	err := c.do(ctx, http.MethodPost, "/api/v1/agent/desktop-leases", input, &output, "")
	return output, err
}

func (c *Client) Lease(ctx context.Context, agentID, leaseID string) (Lease, error) {
	var output Lease
	path := "/api/v1/agent/desktop-leases/" + url.PathEscape(leaseID) + "?agent_id=" + url.QueryEscape(agentID)
	err := c.do(ctx, http.MethodGet, path, nil, &output, "")
	return output, err
}

func (c *Client) ReleaseDesktop(ctx context.Context, agentID, leaseID string) (Lease, error) {
	var output Lease
	path := "/api/v1/agent/desktop-leases/" + url.PathEscape(leaseID) + "?agent_id=" + url.QueryEscape(agentID)
	err := c.do(ctx, http.MethodDelete, path, nil, &output, "")
	return output, err
}

func (c *Client) ChangePower(ctx context.Context, agentID, leaseID, action string) (Job, error) {
	var output Job
	key, err := auth.OpaqueToken(18)
	if err != nil {
		return Job{}, err
	}
	path := "/api/v1/agent/desktop-leases/" + url.PathEscape(leaseID) + "/actions/" + url.PathEscape(action) + "?agent_id=" + url.QueryEscape(agentID)
	err = c.do(ctx, http.MethodPost, path, nil, &output, "mcp-"+key)
	return output, err
}

func (c *Client) ComputerAction(ctx context.Context, agentID, leaseID string, input ComputerAction) (computer.Response, error) {
	var output computer.Response
	path := "/api/v1/agent/desktop-leases/" + url.PathEscape(leaseID) + "/computer-actions?agent_id=" + url.QueryEscape(agentID)
	// Bootstrap is longer than an ordinary control API call. Clone only the
	// client settings, preserving connection pooling and concurrent callers.
	requestClient, transportClient := *c, *c.httpClient
	transportClient.Timeout = computer.APIActionTimeout + 5*time.Second
	requestClient.httpClient = &transportClient
	err := requestClient.doWithResponseLimit(ctx, http.MethodPost, path, input, &output, "", nil, 16<<20)
	return output, err
}

func (c *Client) do(ctx context.Context, method, path string, input, output any, idempotencyKey string) error {
	return c.doWithHeaders(ctx, method, path, input, output, idempotencyKey, nil)
}

func (c *Client) doWithHeaders(ctx context.Context, method, path string, input, output any, idempotencyKey string, headers map[string]string) error {
	return c.doWithResponseLimit(ctx, method, path, input, output, idempotencyKey, headers, 1<<20)
}

func (c *Client) doWithResponseLimit(ctx context.Context, method, path string, input, output any, idempotencyKey string, headers map[string]string, maximum int64) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("control plane request: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if err != nil {
		return fmt.Errorf("read control plane response: %w", err)
	}
	if int64(len(data)) > maximum {
		return fmt.Errorf("control plane response exceeds the transport limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var problem struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(data, &problem)
		if problem.Error.Message != "" {
			return fmt.Errorf("control plane %s (%s)", problem.Error.Message, problem.Error.Code)
		}
		return fmt.Errorf("control plane returned HTTP %s", strconv.Itoa(response.StatusCode))
	}
	if output == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, output); err != nil {
		return fmt.Errorf("decode control plane response: %w", err)
	}
	return nil
}
