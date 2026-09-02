package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
)

func TestHealth(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	response := httptest.NewRecorder()
	New(Dependencies{}).Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" {
		t.Fatalf("unexpected body: %#v", body)
	}
}

func TestReadyRequiresDatabase(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/ready", nil)
	response := httptest.NewRecorder()
	New(Dependencies{}).Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", response.Code)
	}
}

func TestRequestSourceIPUsesPeerAddressOnly(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "[::ffff:192.0.2.42]:43210"
	request.Header.Set("X-Forwarded-For", "203.0.113.9")
	if actual := requestSourceIP(request); actual != "192.0.2.42" {
		t.Fatalf("requestSourceIP()=%q, want trusted peer address", actual)
	}
}

func TestDesktopIPv4PrefersPrivateReachableAddress(t *testing.T) {
	interfaces := []pve.GuestNetworkInterface{
		{Name: "lo", IPAddresses: []pve.GuestIPAddress{{Address: "127.0.0.1", Kind: "ipv4", Prefix: 8}}},
		{Name: "ens18", IPAddresses: []pve.GuestIPAddress{
			{Address: "2001:db8::10", Kind: "ipv6", Prefix: 64},
			{Address: "10.20.30.40", Kind: "ipv4", Prefix: 24},
		}},
	}
	if actual := desktopIPv4(interfaces); actual != "10.20.30.40" {
		t.Fatalf("desktopIPv4()=%q, want 10.20.30.40", actual)
	}
}

func TestDesktopIPv4RejectsLoopbackAndLinkLocal(t *testing.T) {
	interfaces := []pve.GuestNetworkInterface{{IPAddresses: []pve.GuestIPAddress{
		{Address: "127.0.0.1", Kind: "ipv4", Prefix: 8},
		{Address: "169.254.1.2", Kind: "ipv4", Prefix: 16},
	}}}
	if actual := desktopIPv4(interfaces); actual != "" {
		t.Fatalf("desktopIPv4()=%q, want empty", actual)
	}
}

func TestDesktopOSFamilyUsesPVEOSType(t *testing.T) {
	tests := map[string]string{"l26": "linux", "win10": "windows", "win11": "windows", "other": "unknown"}
	for osType, expected := range tests {
		if actual := desktopOSFamily(osType); actual != expected {
			t.Errorf("desktopOSFamily(%q)=%q, want %q", osType, actual, expected)
		}
	}
}

func TestFilterDesktopsByVMIDRequiresAssignmentAndManagedTag(t *testing.T) {
	machines := []pve.VM{
		{VMID: 201, Name: "assigned", Kind: "qemu", Tags: []string{"vc-vdi"}},
		{VMID: 202, Name: "not assigned", Kind: "qemu", Tags: []string{"vc-vdi"}},
		{VMID: 203, Name: "not managed", Kind: "qemu"},
		{VMID: 204, Name: "template", Kind: "qemu", Template: true, Tags: []string{"vc-vdi"}},
	}
	filtered := filterDesktopsByVMID(machines, []int{201, 203, 204})
	if len(filtered) != 1 || filtered[0].VMID != 201 {
		t.Fatalf("unexpected authorized desktop list: %#v", filtered)
	}
}

func TestAgentAccessTokenHasDedicatedPrefix(t *testing.T) {
	token, err := newAgentAccessToken()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, "vcwa_") || len(token) < 40 {
		t.Fatalf("unexpected Agent credential format: prefix=%v length=%d", strings.HasPrefix(token, "vcwa_"), len(token))
	}
}

func TestDesktopAccessPolicyCommandsEnforceSystemGroups(t *testing.T) {
	linuxAdmin, err := desktopAccessPolicyCommand("linux", "local_admin")
	if err != nil {
		t.Fatal(err)
	}
	if len(linuxAdmin) != 3 || !strings.Contains(linuxAdmin[2], "usermod -a -G sudo vdi") ||
		!strings.Contains(linuxAdmin[2], "/etc/sudoers.d/vc-workspace-vdi") ||
		!strings.Contains(linuxAdmin[2], "runuser -u vdi -- sudo -n") {
		t.Fatalf("unexpected Linux administrator command: %#v", linuxAdmin)
	}
	linuxStandard, err := desktopAccessPolicyCommand("linux", "standard")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(linuxStandard[2], "gpasswd -d vdi sudo") ||
		!strings.Contains(linuxStandard[2], "rm -f /etc/sudoers.d/vc-workspace-vdi") ||
		!strings.Contains(linuxStandard[2], "runuser -u vdi -- sudo -n") ||
		!strings.Contains(linuxStandard[2], "terminate-user vdi") {
		t.Fatalf("unexpected Linux standard-user command: %#v", linuxStandard)
	}
	windowsStandard, err := desktopAccessPolicyCommand("windows", "standard")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(windowsStandard, " ")
	if !strings.Contains(joined, "S-1-5-32-544") || !strings.Contains(joined, "Remove-LocalGroupMember") {
		t.Fatalf("unexpected Windows standard-user command: %#v", windowsStandard)
	}
	if _, err := desktopAccessPolicyCommand("linux", "root"); err == nil {
		t.Fatal("expected unknown privilege mode to be rejected")
	}
}

type readinessFileReader struct {
	paths     []string
	readyPath string
}

func (r *readinessFileReader) ReadGuestFile(_ context.Context, _ string, _ int, path string, _ int) (string, error) {
	r.paths = append(r.paths, path)
	if path == r.readyPath {
		return "ready\r\n", nil
	}
	return "", errors.New("file not found")
}

func TestReadDesktopReadyFallsBackToWindowsMarker(t *testing.T) {
	reader := &readinessFileReader{readyPath: `C:\ProgramData\VC Workspace\Agent\desktop-ready`}
	if err := readDesktopReady(context.Background(), reader, "node-1", 9112); err != nil {
		t.Fatal(err)
	}
	if len(reader.paths) != 2 || reader.paths[0] != "/var/lib/vc-vdi/desktop-ready" || reader.paths[1] != `C:\ProgramData\VC Workspace\Agent\desktop-ready` {
		t.Fatalf("unexpected readiness paths: %#v", reader.paths)
	}
}

func TestReadDesktopReadySupportsLegacyWindowsMarker(t *testing.T) {
	reader := &readinessFileReader{readyPath: `C:\ProgramData\VC VDI\Agent\desktop-ready`}
	if err := readDesktopReady(context.Background(), reader, "node-1", 9112); err != nil {
		t.Fatal(err)
	}
	if len(reader.paths) != 3 || reader.paths[2] != `C:\ProgramData\VC VDI\Agent\desktop-ready` {
		t.Fatalf("unexpected readiness paths: %#v", reader.paths)
	}
}

func TestValidateImageProfileRequiresTemplateForReadyState(t *testing.T) {
	profile := store.ImageProfile{
		DisplayName: "Debian 13 · XFCE", BuildStatus: "ready", MirrorURL: "http://mirror.example.com/debian",
		DefaultGPUProfileID: "none", DefaultCores: 4, DefaultMemoryMB: 4096, DefaultDiskGB: 32,
		Firmware: "seabios", TPMVersion: "none",
	}
	if err := validateImageProfile(profile); err == nil {
		t.Fatal("expected ready profile without a template VMID to be rejected")
	}
	profile.TemplateVMID = 158
	if err := validateImageProfile(profile); err != nil {
		t.Fatalf("expected valid image profile, got %v", err)
	}
}

func TestValidateImageProfileRejectsCredentialsInMirrorURL(t *testing.T) {
	profile := store.ImageProfile{
		DisplayName: "Debian 13 · XFCE", BuildStatus: "testing", MirrorURL: "http://user:pass@mirror.example.com/debian",
		DefaultGPUProfileID: "none", DefaultCores: 4, DefaultMemoryMB: 4096, DefaultDiskGB: 32,
		Firmware: "seabios", TPMVersion: "none",
	}
	if err := validateImageProfile(profile); err == nil {
		t.Fatal("expected mirror URL credentials to be rejected")
	}
}

func TestValidateImageProfileRequiresUEFIForTPM(t *testing.T) {
	profile := store.ImageProfile{
		DisplayName: "Windows 11", BuildStatus: "blocked", DefaultGPUProfileID: "none",
		DefaultCores: 4, DefaultMemoryMB: 8192, DefaultDiskGB: 64, Firmware: "seabios", TPMVersion: "2.0",
	}
	if err := validateImageProfile(profile); err == nil {
		t.Fatal("expected TPM 2.0 without UEFI to be rejected")
	}
	profile.Firmware = "uefi"
	if err := validateImageProfile(profile); err != nil {
		t.Fatalf("expected valid Windows hardware defaults, got %v", err)
	}
}

func TestGPUPlacementRequiresMappedAssignableDeviceOnTargetNode(t *testing.T) {
	profile := store.GPUProfile{
		Mode: "pci_passthrough", VendorID: "0x8086", DeviceClass: "0x030000", ResourceMapping: "vc-vdi-intel-igpu",
	}
	mappings := []pve.PCIResourceMapping{{
		ID:      "vc-vdi-intel-igpu",
		Entries: []pve.PCIResourceMappingEntry{{Node: "infra-node4", DeviceID: "0000:00:02.0", IOMMUGroup: "7"}},
	}}
	devices := []pve.GPUDevice{{
		Node: "infra-node4", ID: "0000:00:02.0", VendorID: "0x8086", Class: "0x030000", Assignable: true,
	}}
	if !gpuPlacementAvailable(profile, "infra-node4", mappings, devices) {
		t.Fatal("expected mapped assignable device to be available")
	}
	if gpuPlacementAvailable(profile, "infra-node3", mappings, devices) {
		t.Fatal("expected mapping to pin placement to infra-node4")
	}
	devices[0].Assignable = false
	if gpuPlacementAvailable(profile, "infra-node4", mappings, devices) {
		t.Fatal("expected an unassignable IOMMU device to be rejected")
	}
}

func TestGPUPlacementRejectsMappingThatPointsAtAnotherDevice(t *testing.T) {
	profile := store.GPUProfile{
		Mode: "pci_passthrough", VendorID: "0x8086", DeviceClass: "0x030000", ResourceMapping: "vc-vdi-intel-igpu",
	}
	mappings := []pve.PCIResourceMapping{{
		ID:      "vc-vdi-intel-igpu",
		Entries: []pve.PCIResourceMappingEntry{{Node: "infra-node4", DeviceID: "0000:01:00.0"}},
	}}
	devices := []pve.GPUDevice{{
		Node: "infra-node4", ID: "0000:00:02.0", VendorID: "0x8086", Class: "0x030000", Assignable: true,
	}}
	if gpuPlacementAvailable(profile, "infra-node4", mappings, devices) {
		t.Fatal("expected a mapping for another PCI device to be rejected")
	}
}

func TestGPUPlacementAcceptsAvailableMDevWithoutIOMMU(t *testing.T) {
	profile := store.GPUProfile{
		Mode: "mdev", VendorID: "0x8086", DeviceClass: "0x030000", ResourceMapping: "vc-vdi-intel-gvtg", MDevType: "i915-GVTg_V5_4",
	}
	mappings := []pve.PCIResourceMapping{{
		ID: "vc-vdi-intel-gvtg", MDev: true,
		Entries: []pve.PCIResourceMappingEntry{{Node: "infra-node1", DeviceID: "0000:00:02.0"}},
	}}
	devices := []pve.GPUDevice{{
		Node: "infra-node1", ID: "0000:00:02.0", VendorID: "0x8086", Class: "0x030000", MDevCapable: true,
		MDevTypes: []pve.MDevType{{Type: "i915-GVTg_V5_4", Available: 1}},
	}}
	if !gpuPlacementAvailable(profile, "infra-node1", mappings, devices) {
		t.Fatal("expected an available mediated device to be schedulable")
	}
	devices[0].MDevTypes[0].Available = 0
	if gpuPlacementAvailable(profile, "infra-node1", mappings, devices) {
		t.Fatal("expected exhausted mediated-device capacity to be rejected")
	}
	mappings[0].MDev = false
	devices[0].MDevTypes[0].Available = 1
	if gpuPlacementAvailable(profile, "infra-node1", mappings, devices) {
		t.Fatal("expected a non-mediated mapping to be rejected")
	}
}

func TestMappingEntryMatchesWholeDevicePath(t *testing.T) {
	entry := pve.PCIResourceMappingEntry{DeviceID: "0000:01:00", DevicePaths: []string{"0000:01:00"}}
	if !mappingEntryContainsDevice(entry, "0000:01:00.0") || !mappingEntryContainsDevice(entry, "0000:01:00.1") {
		t.Fatal("expected a whole-device path to match each PCI function")
	}
	if mappingEntryContainsDevice(entry, "0000:01:01.0") {
		t.Fatal("whole-device path matched a different slot")
	}
}

func TestBuildPCIResourceMappingUsesAuthoritativeGPUIdentity(t *testing.T) {
	group := 7
	request := pciResourceMappingRequest{
		ID: "vc-vdi-intel-igpu", Description: "Intel iGPU",
		Entries: []pciResourceMappingEntryRequest{{Node: "infra-node4", DeviceID: "0000:00:02.0"}},
	}
	devices := []pve.GPUDevice{{
		Node: "infra-node4", ID: "0000:00:02.0", VendorID: "0x8086", DeviceID: "0x1912", SubsystemVendorID: "0x103c", SubsystemDeviceID: "0x8054", IOMMUGroup: &group, Assignable: true,
	}}
	mapping, err := buildPCIResourceMapping(request, map[string]bool{"infra-node4": true}, devices)
	if err != nil {
		t.Fatal(err)
	}
	if len(mapping.Entries) != 1 || mapping.Entries[0].HardwareID != "8086:1912" || mapping.Entries[0].SubsystemID != "103c:8054" || mapping.Entries[0].IOMMUGroup != "7" || len(mapping.Entries[0].DevicePaths) != 1 {
		t.Fatalf("unexpected mapping: %#v", mapping)
	}
}

func TestBuildPCIResourceMappingRejectsUnavailableOrDuplicateGPU(t *testing.T) {
	group := 7
	device := pve.GPUDevice{Node: "infra-node4", ID: "0000:00:02.0", VendorID: "0x8086", DeviceID: "0x1912", IOMMUGroup: &group}
	request := pciResourceMappingRequest{ID: "vc-vdi-intel-igpu", Entries: []pciResourceMappingEntryRequest{{Node: "infra-node4", DeviceID: device.ID}}}
	if _, err := buildPCIResourceMapping(request, map[string]bool{"infra-node4": true}, []pve.GPUDevice{device}); err == nil {
		t.Fatal("expected an unassignable GPU to be rejected")
	}
	device.Assignable = true
	request.Entries = append(request.Entries, request.Entries[0])
	if _, err := buildPCIResourceMapping(request, map[string]bool{"infra-node4": true}, []pve.GPUDevice{device}); err == nil {
		t.Fatal("expected a duplicate GPU mapping to be rejected")
	}
}

func TestBuildMDevResourceMappingAcceptsGVTgWithoutAssignableIOMMU(t *testing.T) {
	group := 0
	request := pciResourceMappingRequest{
		ID: "vc-vdi-intel-gvtg", Description: "Intel GVT-g", MDev: true,
		Entries: []pciResourceMappingEntryRequest{{Node: "infra-node1", DeviceID: "0000:00:02.0"}},
	}
	devices := []pve.GPUDevice{{
		Node: "infra-node1", ID: "0000:00:02.0", VendorID: "0x8086", DeviceID: "0x1912", Class: "0x030000", IOMMUGroup: &group, MDevCapable: true,
	}}
	mapping, err := buildPCIResourceMapping(request, map[string]bool{"infra-node1": true}, devices)
	if err != nil {
		t.Fatal(err)
	}
	if !mapping.MDev || len(mapping.Entries) != 1 || mapping.Entries[0].IOMMUGroup != "0" {
		t.Fatalf("unexpected mediated mapping: %#v", mapping)
	}
}

func TestValidateImageTemplateInventoryAcceptsConfiguredQEMUTemplate(t *testing.T) {
	profile := store.ImageProfile{TemplateVMID: 9100, SourceNode: "infra-node6"}
	summary := pve.Summary{VMs: []pve.VM{{VMID: 9100, Kind: "qemu", Template: true, Node: "infra-node6"}}}
	if err := validateImageTemplateInventory(profile, summary); err != nil {
		t.Fatal(err)
	}
}

func TestValidateImageTemplateInventoryRejectsDrift(t *testing.T) {
	tests := []struct {
		name    string
		profile store.ImageProfile
		summary pve.Summary
	}{
		{name: "missing", profile: store.ImageProfile{TemplateVMID: 9100}},
		{name: "regular vm", profile: store.ImageProfile{TemplateVMID: 9100}, summary: pve.Summary{VMs: []pve.VM{{VMID: 9100, Kind: "qemu", Template: false}}}},
		{name: "container", profile: store.ImageProfile{TemplateVMID: 9100}, summary: pve.Summary{VMs: []pve.VM{{VMID: 9100, Kind: "lxc", Template: true}}}},
		{name: "wrong node", profile: store.ImageProfile{TemplateVMID: 9100, SourceNode: "infra-node6"}, summary: pve.Summary{VMs: []pve.VM{{VMID: 9100, Kind: "qemu", Template: true, Node: "infra-node4"}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateImageTemplateInventory(test.profile, test.summary); err == nil {
				t.Fatal("expected drift to be rejected")
			}
		})
	}
}

func TestValidateImageBuildInventoryRequiresWritableOnlineTargetAndUnusedVMID(t *testing.T) {
	profile := store.ImageProfile{SourceNode: "infra-node6", StoragePool: "ceph-pve", TemplateVMID: 9200}
	summary := pve.Summary{
		Writable: true,
		Nodes:    []pve.Node{{Name: "infra-node6", Status: "online"}},
		Storage:  []pve.Storage{{Name: "ceph-pve", Content: "images,rootdir"}},
	}
	if err := validateImageBuildInventory(profile, summary); err != nil {
		t.Fatalf("expected valid image build target, got %v", err)
	}
	summary.VMs = []pve.VM{{VMID: 9200}}
	if err := validateImageBuildInventory(profile, summary); err == nil {
		t.Fatal("expected an existing VMID to block image build")
	}
	summary.VMs = nil
	summary.Writable = false
	if err := validateImageBuildInventory(profile, summary); err == nil {
		t.Fatal("expected read-only PVE configuration to block image build")
	}
}

func TestImageBuildRequestCarriesParameterizedProfile(t *testing.T) {
	profile := store.ImageProfile{
		ID: "debian-13-xfce", SourceNode: "infra-node6", TemplateVMID: 9200,
		SourceISO: "local:iso/debian.iso", SourceISOChecksum: "sha256:abc",
		MirrorURL: "http://mirror.example.com/debian", SecurityMirrorURL: "http://mirror.example.com/debian-security",
		StoragePool: "ceph-pve", Bridge: "vmbr0", DefaultCores: 6, DefaultMemoryMB: 6144, DefaultDiskGB: 48,
		Firmware: "seabios", TPMVersion: "none",
	}
	request := imageBuildRequest(profile, "BuildPass-123!", "")
	if request.Node != profile.SourceNode || request.StoragePool != profile.StoragePool || request.Cores != 6 || request.MemoryMB != 6144 || request.DiskGB != 48 {
		t.Fatalf("image build request lost profile parameters: %#v", request)
	}
}

func TestPrepareImageBuildProfileDoesNotDemoteCurrentProfileBeforeValidation(t *testing.T) {
	original := store.ImageProfile{
		ID: "debian-13-xfce", DisplayName: "Debian 13 · XFCE", Enabled: true, BuildStatus: "ready", StatusDetail: "validated",
		SourceNode: "infra-node6", SourceISO: "local:iso/debian.iso", SourceISOChecksum: "sha256:65273beed27b2df543b68b65630ba525cfbad8df2b12035732b2dff87d6664e7",
		TemplateVMID: 9100, MirrorURL: "http://mirror.example.com/debian", SecurityMirrorURL: "http://mirror.example.com/debian-security",
		StoragePool: "ceph-pve", Bridge: "vmbr0", DefaultCores: 4, DefaultMemoryMB: 4096, DefaultDiskGB: 32,
		Firmware: "seabios", TPMVersion: "none", DefaultGPUProfileID: "none",
	}
	vmid := 9200
	candidate, err := prepareImageBuildProfile(original, &imageProfileUpdateRequest{TemplateVMID: &vmid})
	if err != nil {
		t.Fatal(err)
	}
	if candidate.TemplateVMID != 9200 || candidate.BuildStatus != "draft" || candidate.StatusDetail != "" {
		t.Fatalf("unexpected build candidate: %#v", candidate)
	}
	if original.TemplateVMID != 9100 || original.BuildStatus != "ready" || original.StatusDetail != "validated" {
		t.Fatalf("preflight mutated the current profile: %#v", original)
	}
	invalidVMID := -1
	if _, err := prepareImageBuildProfile(original, &imageProfileUpdateRequest{TemplateVMID: &invalidVMID}); err == nil {
		t.Fatal("expected invalid build candidate to be rejected")
	}
	if original.TemplateVMID != 9100 || original.BuildStatus != "ready" {
		t.Fatalf("failed preflight mutated the current profile: %#v", original)
	}
}

func TestInfrastructureNeedsPVE(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/infrastructure", nil)
	response := httptest.NewRecorder()
	New(Dependencies{}).Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", response.Code)
	}
}

func TestManagedDesktopCapability(t *testing.T) {
	if !isManagedDesktop(pve.VM{Name: "desktop", Kind: "qemu", Tags: []string{"vc-vdi"}}) {
		t.Fatal("expected a VC Workspace QEMU guest to be managed")
	}
	for _, machine := range []pve.VM{
		{Name: "vc-vdi-name-only", Kind: "qemu"},
		{Name: "template", Kind: "qemu", Template: true, Tags: []string{"vc-vdi"}},
		{Name: "container", Kind: "lxc", Tags: []string{"vc-vdi"}},
	} {
		if isManagedDesktop(machine) {
			t.Fatalf("unexpected managed capability for %#v", machine)
		}
	}
}

func TestOIDCUsername(t *testing.T) {
	tests := map[string]string{
		"Chris.Example@example.com": "chris.example",
		"x@example.com":             "sso-user-subject1",
		"weird+name@example.com":    "weird-name",
	}
	for email, expected := range tests {
		if actual := oidcUsername(email, "subject1234"); actual != expected {
			t.Errorf("oidcUsername(%q)=%q, want %q", email, actual, expected)
		}
	}
}

func TestAgentAPIRequiresServiceToken(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent/desktops", nil)
	response := httptest.NewRecorder()
	New(Dependencies{InternalAPIToken: "expected-token"}).Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}
}
