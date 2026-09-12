package pve

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Candidate listings do not assert task outcome. Each selected handle needs
// InspectTask and independent target evidence before any recovery decision.
type CloneTaskCandidate struct {
	UPID      string `json:"upid"`
	Node      string `json:"node"`
	Type      string `json:"type"`
	ID        string `json:"id"`
	User      string `json:"user"`
	TokenID   string `json:"tokenid,omitempty"`
	StartTime int64  `json:"starttime"`
}

func (c *Client) CloneTaskCandidates(ctx context.Context, node string, sourceVMID int, since, until int64) ([]CloneTaskCandidate, error) {
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`).MatchString(node) || sourceVMID <= 0 || since <= 0 || until < since || until-since > 3600 {
		return nil, errors.New("clone task discovery requires a source and a window of at most one hour")
	}
	query := url.Values{"typefilter": {"qmclone"}, "vmid": {strconv.Itoa(sourceVMID)}, "source": {"all"}, "since": {strconv.FormatInt(since, 10)}, "until": {strconv.FormatInt(until, 10)}, "limit": {"51"}}
	var response envelope[[]CloneTaskCandidate]
	if err := c.get(ctx, "/nodes/"+url.PathEscape(node)+"/tasks?"+query.Encode(), &response); err != nil {
		return nil, err
	}
	if len(response.Data) > 50 {
		return nil, errors.New("clone task discovery exceeded its candidate limit; narrow the time window")
	}
	seen := map[string]bool{}
	for _, candidate := range response.Data {
		if candidate.Node != node || candidate.Type != "qmclone" || candidate.ID != strconv.Itoa(sourceVMID) || candidate.User == "" || candidate.StartTime < since || candidate.StartTime > until || !strings.HasPrefix(candidate.UPID, "UPID:") || len(candidate.UPID) > 1024 || strings.ContainsAny(candidate.UPID, "/\\?#\r\n\x00") || seen[candidate.UPID] {
			return nil, errors.New("clone task discovery returned incomplete, duplicate or mismatched evidence")
		}
		seen[candidate.UPID] = true
	}
	if response.Data == nil {
		return []CloneTaskCandidate{}, nil
	}
	return response.Data, nil
}
