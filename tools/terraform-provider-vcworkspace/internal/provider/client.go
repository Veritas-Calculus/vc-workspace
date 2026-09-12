package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type client struct {
	endpoint string
	token    string
	http     *http.Client
}

type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type assignment struct {
	SubjectType string `json:"subject_type"`
	SubjectID   string `json:"subject_id"`
	DesktopVMID int64  `json:"desktop_vmid"`
}

type desktop struct {
	VMID        int64  `json:"vmid"`
	DisplayName string `json:"display_name"`
	Node        string `json:"node"`
	OSFamily    string `json:"os_family"`
	Present     bool   `json:"present"`
	Enabled     bool   `json:"enabled"`
	AccessMode  string `json:"access_mode"`
}

type accessControl struct {
	Assignments []assignment `json:"assignments"`
	Desktops    []desktop    `json:"desktops"`
}

func newClient(endpoint, token string) (*client, error) {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	token = strings.TrimSpace(token)
	if endpoint == "" || token == "" {
		return nil, errors.New("endpoint and api_token are required")
	}
	if !strings.HasPrefix(token, "vcwi_") {
		return nil, errors.New("api_token must be a VC Workspace IaC credential with the vcwi_ prefix")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, errors.New("endpoint must be an absolute HTTP(S) URL")
	}
	return &client{endpoint: endpoint, token: token, http: &http.Client{Timeout: 30 * time.Second}}, nil
}

func (c *client) request(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.endpoint+"/api/v1"+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "terraform-provider-vcworkspace")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var decoded apiError
		_ = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&decoded)
		message := decoded.Error.Message
		if message == "" {
			message = response.Status
		}
		return fmt.Errorf("VC Workspace API %s %s: %s", method, path, message)
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(output); err != nil {
		return fmt.Errorf("decode VC Workspace response: %w", err)
	}
	return nil
}

func (c *client) accessControl(ctx context.Context) (accessControl, error) {
	var state accessControl
	err := c.request(ctx, http.MethodGet, "/access-control", nil, &state)
	return state, err
}

func (c *client) putAssignment(ctx context.Context, value assignment) error {
	path := "/desktop-assignments/" + url.PathEscape(value.SubjectType) + "/" + url.PathEscape(value.SubjectID) + "/" + strconv.FormatInt(value.DesktopVMID, 10)
	return c.request(ctx, http.MethodPut, path, nil, &value)
}

func (c *client) deleteAssignment(ctx context.Context, value assignment) error {
	path := "/desktop-assignments/" + url.PathEscape(value.SubjectType) + "/" + url.PathEscape(value.SubjectID) + "/" + strconv.FormatInt(value.DesktopVMID, 10)
	return c.request(ctx, http.MethodDelete, path, nil, nil)
}
