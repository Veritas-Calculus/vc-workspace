package pve

import (
	"net/http"
	"strings"
	"testing"
)

func TestGPUWritesRequireConfigurationDigest(t *testing.T) {
	client := testClientWithMutations(t, func(r *http.Request) (*http.Response, error) {
		t.Fatal("invalid digest reached PVE")
		return nil, nil
	})
	for _, digest := range []string{"", " ", strings.Repeat("a", 39), strings.Repeat("a", 41), strings.Repeat("z", 40), strings.Repeat("a", 40) + "\n"} {
		if err := client.ConfigureVMPCIResourceMapping(t.Context(), "node", 9204, "gpu", digest); err == nil {
			t.Fatal("accepted invalid PCI digest")
		}
		if err := client.ConfigureVMMDevResourceMapping(t.Context(), "node", 9204, "gpu", "i915-GVTg_V5_4", digest); err == nil {
			t.Fatal("accepted invalid mdev digest")
		}
	}
}

func TestGPUWriteDigestConflictIsNotRetried(t *testing.T) {
	for _, scenario := range []struct {
		mediated bool
		status   int
	}{
		{false, http.StatusConflict}, {true, http.StatusConflict},
		{false, http.StatusInternalServerError}, {true, http.StatusInternalServerError},
	} {
		calls := 0
		client := testClientWithMutations(t, func(r *http.Request) (*http.Response, error) {
			calls++
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Form.Get("digest") != strings.Repeat("b", 40) {
				t.Fatal("digest omitted or changed")
			}
			response := jsonResponse(`{"errors":{"digest":"configuration changed"}}`)
			if scenario.status == http.StatusInternalServerError {
				response = jsonResponse(`{"data":null,"message":"checksum mismatch (file change by other user?)"}`)
			}
			response.StatusCode = scenario.status
			return response, nil
		})
		var err error
		if scenario.mediated {
			err = client.ConfigureVMMDevResourceMapping(t.Context(), "node", 9204, "gpu", "i915-GVTg_V5_4", strings.Repeat("b", 40))
		} else {
			err = client.ConfigureVMPCIResourceMapping(t.Context(), "node", 9204, "gpu", strings.Repeat("b", 40))
		}
		if err == nil || calls != 1 {
			t.Fatalf("conflict bypassed or retried: %v, %d", err, calls)
		}
	}
}

func TestVMConfigurationPreservesDigest(t *testing.T) {
	client := testClient(t, func(r *http.Request) (*http.Response, error) {
		return jsonResponse(`{"data":{"digest":"0123456789012345678901234567890123456789"}}`), nil
	})
	config, err := client.VMConfiguration(t.Context(), "node", 9204)
	if err != nil || config.Digest != "0123456789012345678901234567890123456789" {
		t.Fatalf("digest lost: %v", err)
	}
}
