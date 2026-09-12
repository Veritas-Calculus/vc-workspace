package pve

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCloneLogTarget(t *testing.T) {
	for _, tc := range []struct {
		name, body  string
		known, fail bool
	}{
		{"match", `[{"n":1,"t":"creating a clone of VM 9202 with ID 9203"}]`, true, false},
		{"prefix", `[{"n":1,"t":"preflight"},{"n":2,"t":"creating a clone of VM 9202 with ID 9203"}]`, true, false},
		{"legacy", `[{"n":1,"t":"create full clone of drive scsi0 (ceph:vm-9202-disk-0)"}]`, false, false},
		{"empty", `[]`, false, false},
		{"embedded", `[{"n":1,"t":"warning: creating a clone of VM 9202 with ID 9203"}]`, false, false},
		{"gap", `[{"n":2,"t":"creating a clone of VM 9202 with ID 9203"}]`, false, true},
		{"duplicate", `[{"n":1,"t":"creating a clone of VM 9202 with ID 9203"},{"n":2,"t":"creating a clone of VM 9202 with ID 9203"}]`, false, true},
		{"overflow", `[{"n":1,"t":"creating a clone of VM 9999999999999999999999 with ID 9203"}]`, false, true},
		{"same", `[{"n":1,"t":"creating a clone of VM 9202 with ID 9202"}]`, false, true},
		{"too_many", `[` + strings.Repeat(`{"n":1,"t":"x"},`, 32) + `{"n":33,"t":"x"}]`, false, true},
		{"oversized_line", `[{"n":1,"t":"` + strings.Repeat("x", 16385) + `"}]`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Query().Get("start") != "0" || r.URL.Query().Get("limit") != "32" {
					t.Errorf("unbounded/mutating request: %s %s", r.Method, r.URL)
				}
				_, _ = w.Write([]byte(`{"data":` + tc.body + `}`))
			}))
			defer server.Close()
			client, err := New(Config{Endpoint: server.URL, TokenID: "test@pve!read", TokenSecret: "fixture", HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			source, target, known, err := client.CloneLogTarget(t.Context(), "test", "UPID:test:1:1:1:qmclone:9202:test@pve:")
			if (err != nil) != tc.fail || known != tc.known {
				t.Fatalf("known=%v error=%v", known, err)
			}
			if known && (source != 9202 || target != 9203) {
				t.Fatal("wrong target")
			}
		})
	}
}
