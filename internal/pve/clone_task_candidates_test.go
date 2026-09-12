package pve

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestCloneTaskCandidatesAreBoundedAndFiltered(t *testing.T) {
	valid := CloneTaskCandidate{UPID: "UPID:test:1:1:1:qmclone:9202:user@pve!builder:", Node: "test", Type: "qmclone", ID: "9202", User: "user@pve", TokenID: "builder", StartTime: 100}
	for _, mode := range []string{"valid", "empty", "wrong_source", "wrong_type", "wrong_node", "old", "future", "missing_user", "duplicate", "overflow"} {
		t.Run(mode, func(t *testing.T) {
			candidate := valid
			switch mode {
			case "wrong_source":
				candidate.ID = "9203"
			case "wrong_type":
				candidate.Type = "qmstart"
			case "wrong_node":
				candidate.Node = "other"
			case "old":
				candidate.StartTime = 99
			case "future":
				candidate.StartTime = 201
			case "missing_user":
				candidate.User = ""
			}
			items := []CloneTaskCandidate{candidate}
			if mode == "empty" {
				items = nil
			}
			if mode == "duplicate" {
				items = append(items, candidate)
			}
			if mode == "overflow" {
				items = make([]CloneTaskCandidate, 51)
			}
			client := testClient(t, func(r *http.Request) (*http.Response, error) {
				q := r.URL.Query()
				if r.Method != "GET" || r.URL.Path != "/api2/json/nodes/test/tasks" || q.Get("vmid") != "9202" || q.Get("typefilter") != "qmclone" || q.Get("source") != "all" || q.Get("since") != "100" || q.Get("until") != "200" || q.Get("limit") != "51" {
					t.Fatalf("unbounded or wrong discovery: %s", r.URL)
				}
				body, _ := json.Marshal(map[string]any{"data": items})
				return jsonResponse(string(body)), nil
			})
			got, err := client.CloneTaskCandidates(t.Context(), "test", 9202, 100, 200)
			want := mode == "valid" || mode == "empty"
			if (err == nil) != want {
				t.Fatalf("candidates=%v error=%v", got, err)
			}
			if mode == "valid" && (len(got) != 1 || got[0] != valid) {
				t.Fatal("lost candidate identity")
			}
			if mode == "empty" && got == nil {
				t.Fatal("empty result is not a concrete empty list")
			}
		})
	}
	client := testClient(t, func(*http.Request) (*http.Response, error) { t.Fatal("invalid request sent upstream"); return nil, nil })
	if _, err := client.CloneTaskCandidates(t.Context(), "test", 9202, 100, 3701); err == nil {
		t.Fatal("unbounded window accepted")
	}
}
