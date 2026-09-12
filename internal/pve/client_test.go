package pve

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestNewRequiresHTTPS(t *testing.T) {
	_, err := New(Config{Endpoint: "http://pve.example", TokenID: "a", TokenSecret: "b"})
	if err == nil {
		t.Fatal("expected non-HTTPS endpoint to be rejected")
	}
}

func TestCloneDescriptionPreservesJobMarker(t *testing.T) {
	for _, marker := range []string{"", "VC Workspace clone job: job_test123"} {
		t.Run(marker, func(t *testing.T) {
			client := testClientWithMutations(t, func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodPost || r.URL.Path != "/api2/json/nodes/source/qemu/9202/clone" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				if err := r.ParseForm(); err != nil {
					t.Fatal(err)
				}
				if r.Form.Get("description") != marker || r.Form.Get("newid") != "9204" {
					t.Fatalf("lost marker or target: %v", r.Form)
				}
				if marker == "" && r.Form.Has("description") {
					t.Fatal("legacy clone unexpectedly rewrites description")
				}
				return jsonResponse(`{"data":"task-fixture"}`), nil
			})
			upid, err := client.CloneTemplate(t.Context(), CloneRequest{SourceNode: "source", SourceVMID: 9202, TargetVMID: 9204, Name: "vc-workspace-test", Description: marker})
			if err != nil || upid != "task-fixture" {
				t.Fatalf("clone: %s %v", upid, err)
			}
		})
	}
}

func TestVMPowerStateRequiresCompleteDirectNodeObservation(t *testing.T) {
	for _, state := range []string{"running", "stopped", "", "paused", "unknown"} {
		t.Run(state, func(t *testing.T) {
			client := testClient(t, func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodGet || r.URL.Path != "/api2/json/nodes/node-1/qemu/160/status/current" {
					t.Fatalf("unexpected live observation: %s %s", r.Method, r.URL.Path)
				}
				return jsonResponse(fmt.Sprintf(`{"data":{"vmid":160,"status":%q}}`, state)), nil
			})
			actual, err := client.VMPowerState(t.Context(), "node-1", 160)
			if state == "running" || state == "stopped" {
				if err != nil || actual != state {
					t.Fatal("valid live observation rejected", actual, err)
				}
			} else if err == nil || actual != "" {
				t.Fatal("unknown power state accepted", actual, err)
			}
		})
	}
	for _, raw := range []string{`{}`, `{"data":null}`, `{"data":{"status":"stopped"}}`, `{"data":{"vmid":161,"status":"stopped"}}`, `{"data":{"vmid":160,"status":null}}`, `{"data":{"vmid":160,"status":false}}`} {
		client := testClient(t, func(*http.Request) (*http.Response, error) { return jsonResponse(raw), nil })
		if actual, err := client.VMPowerState(t.Context(), "node-1", 160); err == nil || actual != "" {
			t.Fatal("incomplete/wrong VM observation accepted", raw, actual, err)
		}
	}
	client := testClient(t, func(*http.Request) (*http.Response, error) { t.Fatal("invalid target was dispatched"); return nil, nil })
	if _, err := client.VMPowerState(t.Context(), "", 160); err == nil {
		t.Fatal("empty node accepted")
	}
	if _, err := client.VMPowerState(t.Context(), "node-1", 0); err == nil {
		t.Fatal("invalid VMID accepted")
	}
}

func TestSummaryNormalizesPVEData(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := ""
		switch r.URL.Path {
		case "/api2/json/version":
			body = `{"data":{"version":"9.2.10","release":"9.2"}}`
		case "/api2/json/cluster/status":
			body = `{"data":[{"type":"cluster","name":"infra","quorate":1,"nodes":1}]}`
		case "/api2/json/nodes":
			body = `{"data":[{"node":"node1","status":"online","cpu":0.25,"maxcpu":4,"mem":1024,"maxmem":4096}]}`
		case "/api2/json/cluster/resources":
			body = `{"data":[{"vmid":901,"name":"debian","type":"qemu","node":"node1","status":"stopped","template":1,"tags":"linux;vc-workspace","maxcpu":2,"maxmem":2048}]}`
		case "/api2/json/storage":
			body = `{"data":[{"storage":"ceph","type":"rbd","content":"images","shared":1}]}`
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found", Body: io.NopCloser(strings.NewReader("not found")), Header: make(http.Header)}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": {"application/json"}}}, nil
	})}

	client, err := New(Config{Endpoint: "https://pve.example", TokenID: "user@pve!vdi", TokenSecret: "secret", HTTPClient: httpClient})
	if err != nil {
		t.Fatal(err)
	}
	summary, err := client.Summary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if summary.Cluster.Name != "infra" || len(summary.Nodes) != 1 || len(summary.VMs) != 1 || !summary.VMs[0].Template || !containsTag(summary.VMs[0].Tags, "vc-workspace") {
		t.Fatalf("unexpected summary: %#v", summary)
	}
}

func TestEmptySummarySerializesCollectionsAsArrays(t *testing.T) {
	client := testClient(t, func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/api2/json/version":
			return jsonResponse(`{"data":{"version":"9.2.10","release":"9.2"}}`), nil
		case "/api2/json/cluster/status", "/api2/json/nodes", "/api2/json/cluster/resources", "/api2/json/storage":
			return jsonResponse(`{"data":[]}`), nil
		default:
			t.Fatalf("unexpected request %s", r.URL)
			return nil, nil
		}
	})
	summary, err := client.Summary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &response); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"nodes", "virtual_machines", "storage"} {
		if string(response[key]) != "[]" {
			t.Errorf("empty %s must be an array, got %s", key, response[key])
		}
	}
}

func containsTag(tags []string, expected string) bool {
	for _, tag := range tags {
		if tag == expected {
			return true
		}
	}
	return false
}

func TestGuestNetworkInterfacesNormalizesAgentData(t *testing.T) {
	client := testClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/api2/json/nodes/node-1/qemu/148/agent/network-get-interfaces" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		body := `{"data":{"result":[{"name":"lo","hardware-address":"00:00:00:00:00:00","ip-addresses":[{"ip-address":"127.0.0.1","ip-address-type":"ipv4","prefix":8}]},{"name":"ens18","hardware-address":"BC:24:11:00:00:01","ip-addresses":[{"ip-address":"10.20.30.40","ip-address-type":"ipv4","prefix":24}]}]}}`
		return jsonResponse(body), nil
	})

	interfaces, err := client.GuestNetworkInterfaces(context.Background(), "node-1", 148)
	if err != nil {
		t.Fatal(err)
	}
	if len(interfaces) != 2 || interfaces[1].Name != "ens18" || interfaces[1].IPAddresses[0].Address != "10.20.30.40" {
		t.Fatalf("unexpected interfaces: %#v", interfaces)
	}
}

func TestVMConfigurationNormalizesTemplateHardware(t *testing.T) {
	client := testClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/api2/json/nodes/infra-node4/qemu/9111/config" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		return jsonResponse(`{"data":{"name":"vc-workspace-windows-11","ostype":"win11","bios":"ovmf","machine":"pc-q35-9.2","scsihw":"virtio-scsi-single","agent":"enabled=1,fstrim_cloned_disks=1","cores":4,"memory":"8192","tags":"windows;vc-workspace;template","efidisk0":"ceph:vm-9111-disk-0,efitype=4m","tpmstate0":"ceph:vm-9111-disk-1,version=v2.0","sata0":"ceph:vm-9111-disk-2,size=64G","ide2":"ceph:cloudinit","net0":"e1000=00:11:22:33:44:55,bridge=vmbr0","hostpci0":"mapping=vc-workspace-intel-igpu,pcie=1"}}`), nil
	})

	configuration, err := client.VMConfiguration(context.Background(), "infra-node4", 9111)
	if err != nil {
		t.Fatal(err)
	}
	if configuration.OSType != "win11" || configuration.BIOS != "ovmf" || !configuration.AgentEnabled || configuration.Cores != 4 || configuration.MemoryMB != 8192 {
		t.Fatalf("unexpected configuration: %#v", configuration)
	}
	if len(configuration.Tags) != 3 || configuration.Tags[0] != "template" || configuration.Disks["sata0"] == "" || configuration.NetworkAdapters["net0"] == "" || configuration.PCIHostDevices["hostpci0"] == "" {
		t.Fatalf("hardware collections were not normalized: %#v", configuration)
	}
}

func TestSetGuestUserPasswordUsesProtectedAgentEndpoint(t *testing.T) {
	client := testClientWithMutations(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Path != "/api2/json/nodes/node-1/qemu/148/agent/set-user-password" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		values, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatal(err)
		}
		if values.Get("username") != "vdi" || values.Get("password") != "Vd1!temporary" {
			t.Fatalf("unexpected form fields: %#v", values)
		}
		return jsonResponse(`{"data":{"result":{}}}`), nil
	})

	if err := client.SetGuestUserPassword(context.Background(), "node-1", 148, "vdi", "Vd1!temporary"); err != nil {
		t.Fatal(err)
	}
}

func TestReadGuestFileUsesBoundedAgentRead(t *testing.T) {
	client := testClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/api2/json/nodes/node-1/qemu/148/agent/file-read" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("file") != "/var/lib/vc-workspace/desktop-ready" || r.URL.Query().Get("count") != "64" {
			t.Fatalf("unexpected query: %s", r.URL.RawQuery)
		}
		return jsonResponse(`{"data":{"content":"ready\n","bytes-read":6}}`), nil
	})

	content, err := client.ReadGuestFile(context.Background(), "node-1", 148, "/var/lib/vc-workspace/desktop-ready", 64)
	if err != nil {
		t.Fatal(err)
	}
	if content != "ready\n" {
		t.Fatalf("unexpected content: %q", content)
	}
}

func TestWriteGuestFileUsesProtectedAgentEndpoint(t *testing.T) {
	client := testClientWithMutations(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Path != "/api2/json/nodes/node-1/qemu/148/agent/file-write" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		values, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatal(err)
		}
		if values.Get("file") != "/var/lib/vc-workspace/computer/requests/action_test.json" || values.Get("content") != `{"operation":"screenshot"}` || values.Get("encode") != "1" {
			t.Fatalf("unexpected form fields: %#v", values)
		}
		return jsonResponse(`{"data":null}`), nil
	})

	if err := client.WriteGuestFile(context.Background(), "node-1", 148, "/var/lib/vc-workspace/computer/requests/action_test.json", `{"operation":"screenshot"}`); err != nil {
		t.Fatal(err)
	}
}

func TestWriteGuestBinaryFileUsesPreencodedContent(t *testing.T) {
	payload := []byte{0x00, 0xff, 0x80, 0x41}
	client := testClientWithMutations(t, func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		values, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := base64.StdEncoding.DecodeString(values.Get("content"))
		if err != nil {
			t.Fatal(err)
		}
		if r.URL.Path != "/api2/json/nodes/node-1/qemu/148/agent/file-write" || values.Get("encode") != "0" || string(decoded) != string(payload) {
			t.Fatalf("unexpected binary file-write request: path=%s fields=%#v", r.URL.Path, values)
		}
		return jsonResponse(`{"data":null}`), nil
	})
	if err := client.WriteGuestBinaryFile(context.Background(), "node-1", 148, "/tmp/chunk", payload); err != nil {
		t.Fatal(err)
	}
}

func TestExecGuestStartsAndWaitsForCommand(t *testing.T) {
	statusReads := 0
	client := testClientWithMutations(t, func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/api2/json/nodes/node-1/qemu/148/agent/exec":
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected method: %s", r.Method)
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			values, err := url.ParseQuery(string(body))
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"/bin/sh", "-c", "id -nG vdi"}
			if got := values["command"]; len(got) != len(want) || strings.Join(got, "|") != strings.Join(want, "|") {
				t.Fatalf("unexpected command: %#v", got)
			}
			return jsonResponse(`{"data":{"pid":723}}`), nil
		case "/api2/json/nodes/node-1/qemu/148/agent/exec-status":
			if r.Method != http.MethodGet || r.URL.Query().Get("pid") != "723" {
				t.Fatalf("unexpected status request: %s %s", r.Method, r.URL.String())
			}
			statusReads++
			if statusReads == 1 {
				return jsonResponse(`{"data":{"exited":0}}`), nil
			}
			return jsonResponse(`{"data":{"exited":1,"exitcode":0,"out-data":"vdi sudo\n","err-data":""}}`), nil
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
			return nil, nil
		}
	})

	result, err := client.ExecGuest(context.Background(), "node-1", 148, []string{"/bin/sh", "-c", "id -nG vdi"})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || result.Stdout != "vdi sudo\n" || statusReads != 2 {
		t.Fatalf("unexpected guest result: %#v (reads %d)", result, statusReads)
	}
}

func TestExecGuestWithInputKeepsSecretOutOfCommand(t *testing.T) {
	const secret = "one-time-directory-password"
	client := testClientWithMutations(t, func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/api2/json/nodes/node-1/qemu/148/agent/exec":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			values, err := url.ParseQuery(string(body))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(strings.Join(values["command"], " "), secret) {
				t.Fatal("one-time secret leaked into guest command argv")
			}
			if actual := values.Get("input-data"); actual != secret {
				t.Fatalf("unexpected PVE stdin: %q", actual)
			}
			return jsonResponse(`{"data":{"pid":724}}`), nil
		case "/api2/json/nodes/node-1/qemu/148/agent/exec-status":
			return jsonResponse(`{"data":{"exited":1,"exitcode":0,"out-data":"","err-data":""}}`), nil
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
			return nil, nil
		}
	})
	if _, err := client.ExecGuestWithInput(context.Background(), "node-1", 148, []string{"/bin/sh", "-c", "cat >/dev/null"}, []byte(secret)); err != nil {
		t.Fatal(err)
	}
}

func TestExecGuestRequiresExplicitCompleteNormalExit(t *testing.T) {
	for _, test := range []struct {
		name, status string
		allowed      bool
		exit         int
	}{
		{"zero", `{"exited":1,"exitcode":0}`, true, 0},
		{"boolean", `{"exited":true,"exitcode":3,"out-truncated":false,"err-truncated":0}`, true, 3},
		{"signal", `{"exited":1,"signal":9}`, false, 0},
		{"windows_exception", `{"exited":1,"signal":3221225477}`, false, 0},
		{"windows_signed_exception", `{"exited":1,"signal":-1073741819}`, false, 0},
		{"contradictory_signal", `{"exited":1,"signal":0,"exitcode":0}`, false, 0},
		{"missing_code", `{"exited":1}`, false, 0},
		{"null_code", `{"exited":1,"exitcode":null}`, false, 0},
		{"negative_code", `{"exited":1,"exitcode":-1}`, false, 0},
		{"oversized_code", `{"exited":1,"exitcode":4294967296}`, false, 0},
		{"stdout_truncated", `{"exited":1,"exitcode":0,"out-truncated":true,"out-data":"partial"}`, false, 0},
		{"stderr_truncated", `{"exited":1,"exitcode":0,"err-truncated":1}`, false, 0},
		{"invalid_flag", `{"exited":1,"exitcode":0,"err-truncated":"false"}`, false, 0},
		{"missing_state", `{}`, false, 0},
		{"invalid_state", `{"exited":2,"exitcode":0}`, false, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			starts, reads := 0, 0
			client := testClientWithMutations(t, func(r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodPost {
					starts++
					return jsonResponse(`{"data":{"pid":725}}`), nil
				}
				reads++
				return jsonResponse(`{"data":` + test.status + `}`), nil
			})
			result, err := client.ExecGuest(t.Context(), "node-1", 148, []string{"/bin/true"})
			if (err == nil) != test.allowed || starts != 1 || reads != 1 {
				t.Fatalf("allowed=%v result=%+v err=%v starts=%d reads=%d", test.allowed, result, err, starts, reads)
			}
			if test.allowed && result.ExitCode != test.exit {
				t.Fatal("normal exit code changed")
			}
			if !test.allowed && (result.ExitCode == 0 || result.Stdout != "" || result.Stderr != "") {
				t.Fatal("abnormal or incomplete output looked successful")
			}
		})
	}
}

func TestExecGuestWithInputRejectsInputAbovePVELimit(t *testing.T) {
	client := testClientWithMutations(t, func(r *http.Request) (*http.Response, error) {
		t.Fatalf("oversized input should not reach PVE: %s", r.URL.Path)
		return nil, nil
	})
	input := []byte(strings.Repeat("x", 64*1024+1))
	if _, err := client.ExecGuestWithInput(context.Background(), "node-1", 148, []string{"/bin/sh", "-c", "cat >/dev/null"}, input); err == nil {
		t.Fatal("expected oversized PVE stdin to be rejected")
	}
}

func TestExecGuestRejectsEmptyArgument(t *testing.T) {
	client := testClientWithMutations(t, func(r *http.Request) (*http.Response, error) {
		t.Fatal("invalid command should not reach PVE")
		return nil, nil
	})
	if _, err := client.ExecGuest(context.Background(), "node-1", 148, []string{"/bin/sh", ""}); err == nil {
		t.Fatal("expected invalid guest command")
	}
}

func TestExecGuestRetriesTransientStatusTimeout(t *testing.T) {
	statusReads := 0
	client := testClientWithMutations(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api2/json/nodes/node-1/qemu/148/agent/exec" {
			return jsonResponse(`{"data":{"pid":724}}`), nil
		}
		statusReads++
		if statusReads == 1 {
			return &http.Response{StatusCode: http.StatusInternalServerError, Status: "500 Internal Server Error", Body: io.NopCloser(strings.NewReader(`{"message":"qga command 'guest-exec-status' failed - got timeout"}`)), Header: make(http.Header)}, nil
		}
		return jsonResponse(`{"data":{"exited":1,"exitcode":0}}`), nil
	})
	result, err := client.ExecGuest(context.Background(), "node-1", 148, []string{"/bin/true"})
	if err != nil || result.ExitCode != 0 || statusReads != 2 {
		t.Fatalf("unexpected retry result: %#v error=%v reads=%d", result, err, statusReads)
	}
}

func TestExecGuestOutageOnlyRetriesAcknowledgedPID(t *testing.T) {
	for _, failedStart := range []bool{false, true} {
		t.Run(fmt.Sprint(failedStart), func(t *testing.T) {
			starts, reads := 0, 0
			outage := func() *http.Response {
				return &http.Response{StatusCode: 500, Status: "500 Internal Server Error", Body: io.NopCloser(strings.NewReader(`{"message":"QEMU guest agent is not running"}`)), Header: make(http.Header)}
			}
			client := testClientWithMutations(t, func(r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodPost {
					starts++
					if failedStart {
						return outage(), nil
					}
					return jsonResponse(`{"data":{"pid":724}}`), nil
				}
				if r.URL.Query().Get("pid") != "724" {
					t.Fatal("read a different process")
				}
				reads++
				if reads == 1 {
					return outage(), nil
				}
				return jsonResponse(`{"data":{"exited":1,"exitcode":0}}`), nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err := client.ExecGuest(ctx, "node-1", 148, []string{"/bin/true"})
			if starts != 1 || (failedStart && (err == nil || reads != 0)) || (!failedStart && (err != nil || reads != 2)) {
				t.Fatalf("starts=%d reads=%d error=%v", starts, reads, err)
			}
		})
	}
}

func TestExecGuestOutageRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	starts, reads := 0, 0
	client := testClientWithMutations(t, func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost {
			starts++
			return jsonResponse(`{"data":{"pid":724}}`), nil
		}
		reads++
		cancel()
		return &http.Response{StatusCode: 500, Status: "500 Internal Server Error", Body: io.NopCloser(strings.NewReader(`{"message":"QEMU guest agent is not running"}`)), Header: make(http.Header)}, nil
	})
	_, err := client.ExecGuest(ctx, "node-1", 148, []string{"/bin/true"})
	if err != context.Canceled || starts != 1 || reads != 1 {
		t.Fatalf("outage ignored cancellation: starts=%d reads=%d error=%v", starts, reads, err)
	}
}

func TestGPUDevicesReportsOnlyDisplayControllersAndIOMMUReadiness(t *testing.T) {
	client := testClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/api2/json/nodes/node-1/hardware/pci" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		return jsonResponse(`{"data":[{"id":"0000:00:02.0","vendor":"0x8086","device":"0x1912","subsystem_vendor":"0x103c","subsystem_device":"0x8054","vendor_name":"Intel Corporation","device_name":"HD Graphics 530","class":"0x030000","iommu_group":7,"mdev":{"i915-GVTg_V5_4":{"available":1}}},{"id":"0000:00:1f.6","vendor":"0x8086","device":"0x15b8","class":"0x020000"}]}`), nil
	})

	devices, err := client.GPUDevices(context.Background(), []string{"node-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].DeviceName != "HD Graphics 530" || devices[0].SubsystemVendorID != "0x103c" || devices[0].SubsystemDeviceID != "0x8054" || !devices[0].Assignable || !devices[0].MDevCapable {
		t.Fatalf("unexpected GPU devices: %#v", devices)
	}
}

func TestGPUDevicesTreatsPVEZeroIOMMUGroupAsUnavailable(t *testing.T) {
	client := testClient(t, func(r *http.Request) (*http.Response, error) {
		return jsonResponse(`{"data":[{"id":"0000:00:02.0","vendor":"0x8086","device":"0x1912","class":"0x030000","iommugroup":0}]}`), nil
	})

	devices, err := client.GPUDevices(context.Background(), []string{"node-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].IOMMUGroup == nil || *devices[0].IOMMUGroup != 0 || devices[0].Assignable {
		t.Fatalf("unexpected GPU readiness: %#v", devices)
	}
}

func TestGPUDevicesLoadsMDevTypesFromPVEEndpoint(t *testing.T) {
	client := testClient(t, func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/api2/json/nodes/node-1/hardware/pci":
			return jsonResponse(`{"data":[{"id":"0000:00:02.0","vendor":"0x8086","device":"0x1912","device_name":"HD Graphics 530","class":"0x030000","iommugroup":0,"mdev":1}]}`), nil
		case "/api2/json/nodes/node-1/hardware/pci/0000:00:02.0/mdev":
			return jsonResponse(`{"data":[{"type":"i915-GVTg_V5_4","name":"GVTg_V5_4","available":1,"description":"resolution: 1920x1200"}]}`), nil
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
			return nil, nil
		}
	})

	devices, err := client.GPUDevices(context.Background(), []string{"node-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || !devices[0].MDevCapable || len(devices[0].MDevTypes) != 1 || devices[0].MDevTypes[0].Type != "i915-GVTg_V5_4" || devices[0].MDevTypes[0].Available != 1 {
		t.Fatalf("unexpected mdev inventory: %#v", devices)
	}
}

func TestPCIResourceMappingsNormalizesNodeEntries(t *testing.T) {
	client := testClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/api2/json/cluster/mapping/pci" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		return jsonResponse(`{"data":[{"id":"vc-workspace-intel-igpu","description":"Intel iGPU","mdev":1,"map":["node=infra-node4,path=0000:00:02.0,id=8086:1912,iommugroup=7"]}]}`), nil
	})

	mappings, err := client.PCIResourceMappings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(mappings) != 1 || mappings[0].ID != "vc-workspace-intel-igpu" || !mappings[0].MDev || len(mappings[0].Entries) != 1 {
		t.Fatalf("unexpected mappings: %#v", mappings)
	}
	entry := mappings[0].Entries[0]
	if entry.Node != "infra-node4" || entry.DeviceID != "0000:00:02.0" || len(entry.DevicePaths) != 1 || entry.HardwareID != "8086:1912" || entry.IOMMUGroup != "7" {
		t.Fatalf("unexpected mapping entry: %#v", entry)
	}
}

func TestPCIResourceMappingsPreservesMultiFunctionPaths(t *testing.T) {
	client := testClient(t, func(r *http.Request) (*http.Response, error) {
		return jsonResponse(`{"data":[{"id":"vc-workspace-discrete-gpu","map":["node=infra-node5,path=0000:01:00.0;0000:01:00.1,id=10de:2484,iommugroup=12"]}]}`), nil
	})
	mappings, err := client.PCIResourceMappings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(mappings) != 1 || len(mappings[0].Entries) != 1 || len(mappings[0].Entries[0].DevicePaths) != 2 || mappings[0].Entries[0].DevicePaths[1] != "0000:01:00.1" {
		t.Fatalf("unexpected multi-function mapping: %#v", mappings)
	}
}

func TestCreatePCIResourceMappingUsesPVEPropertyString(t *testing.T) {
	client := testClientWithMutations(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Path != "/api2/json/cluster/mapping/pci" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		values, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatal(err)
		}
		if values.Get("id") != "vc-workspace-intel-igpu" || values.Get("description") != "Intel iGPU" {
			t.Fatalf("unexpected mapping metadata: %#v", values)
		}
		if values.Get("map") != "node=infra-node4,path=0000:00:02.0,id=8086:1912,iommugroup=7" {
			t.Fatalf("unexpected map property string: %q", values.Get("map"))
		}
		return jsonResponse(`{"data":null}`), nil
	})
	mapping := PCIResourceMapping{
		ID: "vc-workspace-intel-igpu", Description: "Intel iGPU",
		Entries: []PCIResourceMappingEntry{{Node: "infra-node4", DeviceID: "0000:00:02.0", HardwareID: "0x8086:0x1912", IOMMUGroup: "7"}},
	}
	if err := client.CreatePCIResourceMapping(context.Background(), mapping); err != nil {
		t.Fatal(err)
	}
}

func TestCreateMDevPCIResourceMappingSetsPVEFlag(t *testing.T) {
	client := testClientWithMutations(t, func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		values, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatal(err)
		}
		if values.Get("mdev") != "1" || values.Get("map") != "node=infra-node1,path=0000:00:02.0,id=8086:1912,iommugroup=0" {
			t.Fatalf("unexpected mediated mapping form: %#v", values)
		}
		return jsonResponse(`{"data":null}`), nil
	})
	mapping := PCIResourceMapping{
		ID: "vc-workspace-intel-gvtg", MDev: true,
		Entries: []PCIResourceMappingEntry{{Node: "infra-node1", DeviceID: "0000:00:02.0", HardwareID: "8086:1912", IOMMUGroup: "0"}},
	}
	if err := client.CreatePCIResourceMapping(context.Background(), mapping); err != nil {
		t.Fatal(err)
	}
}

func TestUpdatePCIResourceMappingUsesLogicalIDPath(t *testing.T) {
	client := testClientWithMutations(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPut || r.URL.Path != "/api2/json/cluster/mapping/pci/vc-workspace-intel-igpu" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		return jsonResponse(`{"data":null}`), nil
	})
	mapping := PCIResourceMapping{ID: "vc-workspace-intel-igpu", Entries: []PCIResourceMappingEntry{{Node: "infra-node4", DeviceID: "00:02.0", HardwareID: "8086:1912", IOMMUGroup: "7"}}}
	if err := client.UpdatePCIResourceMapping(context.Background(), mapping); err != nil {
		t.Fatal(err)
	}
}

func TestConfigureVMPCIResourceMappingUsesLogicalMapping(t *testing.T) {
	client := testClientWithMutations(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPut || r.URL.Path != "/api2/json/nodes/infra-node4/qemu/9113/config" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		values, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatal(err)
		}
		if values.Get("digest") != strings.Repeat("a", 40) {
			t.Fatal("missing digest precondition")
		}
		if values.Get("hostpci0") != "mapping=vc-workspace-intel-igpu,pcie=1,x-vga=1" {
			t.Fatalf("unexpected hostpci0 value: %q", values.Get("hostpci0"))
		}
		return jsonResponse(`{"data":null}`), nil
	})

	if err := client.ConfigureVMPCIResourceMapping(context.Background(), "infra-node4", 9113, "vc-workspace-intel-igpu", strings.Repeat("a", 40)); err != nil {
		t.Fatal(err)
	}
}

func TestConfigureVMMDevResourceMappingUsesType(t *testing.T) {
	client := testClientWithMutations(t, func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		values, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatal(err)
		}
		if values.Get("digest") != strings.Repeat("a", 40) {
			t.Fatal("missing digest precondition")
		}
		if values.Get("hostpci0") != "mapping=vc-workspace-intel-gvtg,mdev=i915-GVTg_V5_4" {
			t.Fatalf("unexpected mediated hostpci0 value: %q", values.Get("hostpci0"))
		}
		return jsonResponse(`{"data":null}`), nil
	})
	if err := client.ConfigureVMMDevResourceMapping(context.Background(), "infra-node1", 9120, "vc-workspace-intel-gvtg", "i915-GVTg_V5_4", strings.Repeat("a", 40)); err != nil {
		t.Fatal(err)
	}
}

func testClient(t *testing.T, transport roundTripFunc) *Client {
	t.Helper()
	client, err := New(Config{
		Endpoint:    "https://pve.example",
		TokenID:     "user@pve!vdi",
		TokenSecret: "secret",
		HTTPClient:  &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func testClientWithMutations(t *testing.T, transport roundTripFunc) *Client {
	t.Helper()
	client, err := New(Config{
		Endpoint:         "https://pve.example",
		TokenID:          "user@pve!vdi",
		TokenSecret:      "secret",
		MutationsEnabled: true,
		HTTPClient:       &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": {"application/json"}},
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}
