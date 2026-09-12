package pve

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Explicitly selected read-only acceptance. Credentials are environment-only;
// neither task principals nor token material are printed in test output.
func TestLiveTaskEvidenceReadOnly(t *testing.T) {
	if os.Getenv("VC_WORKSPACE_LIVE_TASK_EVIDENCE") != "true" {
		t.Skip("explicit task evidence read-only acceptance")
	}
	upid := os.Getenv("VC_WORKSPACE_LIVE_TASK_UPID")
	parts := strings.Split(upid, ":")
	if len(parts) != 9 || parts[0] != "UPID" || parts[8] != "" {
		t.Fatal("explicit canonical task handle required")
	}
	started, err := strconv.ParseInt(parts[4], 16, 64)
	if err != nil || started <= 0 {
		t.Fatal("invalid task start timestamp")
	}
	client, err := New(Config{Endpoint: os.Getenv("VC_WORKSPACE_LIVE_PVE_ENDPOINT"), TokenID: os.Getenv("VC_WORKSPACE_LIVE_TASK_TOKEN_ID"), TokenSecret: os.Getenv("VC_WORKSPACE_LIVE_TASK_TOKEN_SECRET"), MutationsEnabled: false})
	if err != nil {
		t.Fatal("invalid read-only task client configuration")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	evidence, err := client.InspectTask(ctx, parts[1], upid)
	if err != nil {
		t.Fatal(err)
	}
	principal := evidence.User
	if evidence.TokenID != "" {
		principal += "!" + evidence.TokenID
	}
	if evidence.Type != parts[5] || evidence.ID != parts[6] || evidence.StartTime != started || principal != parts[7] {
		t.Fatal("task identity or timestamp differs from selected handle")
	}
	if evidence.Status != "stopped" || evidence.ExitStatus != "OK" {
		t.Fatal("selected acceptance task is not successfully completed")
	}
	if evidence.Type == "qmclone" {
		source, err := strconv.Atoi(evidence.ID)
		if err != nil {
			t.Fatal("clone source ID is invalid")
		}
		candidates, err := client.CloneTaskCandidates(ctx, evidence.Node, source, evidence.StartTime-30, evidence.StartTime+30)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, candidate := range candidates {
			if candidate.UPID == upid {
				found = true
			}
		}
		if !found {
			t.Fatal("bounded discovery did not contain the selected historical clone")
		}
		t.Logf("bounded read-only discovery contained selected clone among %d candidates", len(candidates))
		logSource, logTarget, recognized, err := client.CloneLogTarget(ctx, evidence.Node, upid)
		if err != nil {
			t.Fatal("bounded clone log read failed:", err)
		}
		if recognized {
			expected, err := strconv.Atoi(os.Getenv("VC_WORKSPACE_LIVE_CLONE_TARGET"))
			if err != nil || expected <= 0 || logSource != source || logTarget != expected {
				t.Fatal("clone log declaration differs from explicit target")
			}
			t.Log("bounded log declaration matched explicit source and target")
		} else {
			t.Log("bounded log format has no recognized target declaration; target association remains unproven")
		}
	}
	t.Log("read-only task evidence matched selected handle, principal, start time and successful terminal state")
}
