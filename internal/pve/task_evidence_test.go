package pve

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestTaskEvidencePreservesPrincipalAndRejectsMismatch(t *testing.T) {
	const upid = "UPID:test:00000001:00000001:00000001:qmclone:9202:user@pve!builder:"
	valid := TaskEvidence{UPID: upid, Node: "test", Type: "qmclone", ID: "9202", User: "user@pve", TokenID: "builder", StartTime: 1, Status: "stopped", ExitStatus: "OK"}
	for _, tc := range []struct {
		name   string
		change func(*TaskEvidence)
		want   bool
	}{
		{"valid", func(*TaskEvidence) {}, true},
		{"node", func(e *TaskEvidence) { e.Node = "other" }, false},
		{"upid", func(e *TaskEvidence) { e.UPID += "other" }, false},
		{"missing_principal", func(e *TaskEvidence) { e.User = "" }, false},
		{"missing_time", func(e *TaskEvidence) { e.StartTime = 0 }, false},
		{"unknown_state", func(e *TaskEvidence) { e.Status = "unknown" }, false},
		{"missing_exit", func(e *TaskEvidence) { e.ExitStatus = "" }, false},
		{"running_with_exit", func(e *TaskEvidence) { e.Status = "running" }, false},
		{"running", func(e *TaskEvidence) { e.Status = "running"; e.ExitStatus = "" }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expected := valid
			tc.change(&expected)
			client := testClient(t, func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" || r.URL.Path != "/api2/json/nodes/test/tasks/"+upid+"/status" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				body, _ := json.Marshal(map[string]any{"data": expected})
				return jsonResponse(string(body)), nil
			})
			got, err := client.InspectTask(t.Context(), "test", upid)
			if (err == nil) != tc.want {
				t.Fatalf("evidence=%+v error=%v", got, err)
			}
			if tc.want && got != expected {
				t.Fatalf("lost task identity: %+v", got)
			}
		})
	}
	client := testClient(t, func(*http.Request) (*http.Response, error) { t.Fatal("invalid target sent upstream"); return nil, nil })
	for _, target := range []string{"", "UPID:bad/../target", "UPID:bad\nheader"} {
		if _, err := client.InspectTask(t.Context(), "test", target); err == nil {
			t.Fatal("invalid target accepted")
		}
	}
}
