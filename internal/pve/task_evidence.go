package pve

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// TaskEvidence is an observation, not authorization to attach a task to a Job.
// TokenID is distinct from User in PVE's response; dropping it loses principal
// identity when the same user owns multiple API tokens.
type TaskEvidence struct {
	UPID       string `json:"upid"`
	Node       string `json:"node"`
	Type       string `json:"type"`
	ID         string `json:"id"`
	User       string `json:"user"`
	TokenID    string `json:"tokenid,omitempty"`
	StartTime  int64  `json:"starttime"`
	Status     string `json:"status"`
	ExitStatus string `json:"exitstatus,omitempty"`
}

func (c *Client) InspectTask(ctx context.Context, node, upid string) (TaskEvidence, error) {
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`).MatchString(node) || !strings.HasPrefix(upid, "UPID:") || len(upid) > 1024 || strings.ContainsAny(upid, "/\\?#\r\n\x00") {
		return TaskEvidence{}, errors.New("invalid task inspection target")
	}
	var response envelope[TaskEvidence]
	path := fmt.Sprintf("/nodes/%s/tasks/%s/status", url.PathEscape(node), url.PathEscape(upid))
	if err := c.get(ctx, path, &response); err != nil {
		return TaskEvidence{}, err
	}
	evidence := response.Data
	if evidence.UPID != upid || evidence.Node != node || evidence.Type == "" || evidence.User == "" || evidence.StartTime <= 0 ||
		(evidence.Status != "running" && evidence.Status != "stopped") ||
		(evidence.Status == "stopped" && evidence.ExitStatus == "") ||
		(evidence.Status == "running" && evidence.ExitStatus != "") {
		return TaskEvidence{}, errors.New("task inspection returned incomplete or mismatched evidence")
	}
	return evidence, nil
}
