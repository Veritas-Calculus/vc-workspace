package agentapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/computer"
)

func TestComputerResponseBudgetDoesNotTruncateLargeScreenshots(t *testing.T) {
	for _, size := range []int{2 << 20, 17 << 20} {
		t.Run(fmt.Sprintf("%dMiB", size/(1<<20)), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(computer.Response{SchemaVersion: 1, OK: true, Screenshot: &computer.ScreenshotResult{Data: strings.Repeat("a", size)}})
			}))
			defer server.Close()
			client, err := New(server.URL, "disposable")
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.ComputerAction(t.Context(), "agent-test", "lease-test", ComputerAction{Operation: computer.OperationScreenshot})
			if size < 16<<20 {
				if err != nil || result.Screenshot == nil || len(result.Screenshot.Data) != size {
					t.Fatal("large screenshot was truncated", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "transport limit") {
				t.Fatal("oversized response accepted", err)
			}
			if client.httpClient.Timeout != 45*time.Second {
				t.Fatal("computer call mutated the shared client")
			}
		})
	}
}
