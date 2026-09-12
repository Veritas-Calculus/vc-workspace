package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/computer"
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
		{VMID: 201, Name: "assigned", Kind: "qemu", Managed: true, Tags: []string{"vc-workspace", "template"}},
		{VMID: 202, Name: "not assigned", Kind: "qemu", Managed: true, Tags: []string{"vc-workspace"}},
		{VMID: 203, Name: "not managed", Kind: "qemu"},
		{VMID: 204, Name: "template", Kind: "qemu", Template: true, Tags: []string{"vc-workspace"}},
		{VMID: 205, Name: "image candidate", Kind: "qemu", Tags: []string{"vc-workspace", "template"}},
	}
	filtered := filterDesktopsByVMID(machines, []int{201, 203, 204, 205})
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

func TestValidateNewPasswordBounds(t *testing.T) {
	for _, value := range []string{"short", strings.Repeat("x", 129)} {
		if err := validateNewPassword(value); err == nil {
			t.Fatalf("expected password length %d to be rejected", len([]rune(value)))
		}
	}
	for _, value := range []string{"correct-horse-battery", strings.Repeat("密", 128)} {
		if err := validateNewPassword(value); err != nil {
			t.Fatalf("expected a %d-character password to be valid: %v", len([]rune(value)), err)
		}
	}
}

func TestPublicUserIncludesCredentialOwnerWithoutExposingHash(t *testing.T) {
	local := publicUser(store.User{ID: "local", PasswordHash: "secret-hash"})
	if local["identity_kind"] != "local" {
		t.Fatalf("unexpected local identity: %#v", local)
	}
	if _, exposed := local["password_hash"]; exposed {
		t.Fatalf("password hash was exposed: %#v", local)
	}
	oidc := publicUser(store.User{ID: "oidc"})
	if oidc["identity_kind"] != "oidc" {
		t.Fatalf("unexpected OIDC identity: %#v", oidc)
	}
}

func TestComputerErrorReasonDoesNotExposeGuestErrors(t *testing.T) {
	if got := computerErrorReason(computer.ErrTimeout); got != "timeout" {
		t.Fatalf("unexpected timeout reason %q", got)
	}
	if got := computerErrorReason(errors.New("password=must-not-leak")); got != "guest_rejected" {
		t.Fatalf("unexpected guest error reason %q", got)
	}
}

type failingAuthorityExecutor struct {
	err error
}

func (f failingAuthorityExecutor) Execute(context.Context, pve.VM, computer.Request, time.Duration) (computer.Response, error) {
	return computer.Response{}, nil
}

func (f failingAuthorityExecutor) ActivateAuthority(context.Context, pve.VM, computer.Authority) error {
	return nil
}

func (f failingAuthorityExecutor) RevokeAuthority(context.Context, pve.VM, computer.Authority) error {
	return f.err
}

func TestHumanTakeoverAuthorityFailurePropagates(t *testing.T) {
	db, _ := regressionDB(t)
	if err := db.UpsertManagedDesktop(t.Context(), store.ManagedDesktop{VMID: 158, Node: "test", DisplayName: "Epoch test"}); err != nil {
		t.Fatal(err)
	}
	wanted := errors.New("qga authority write failed")
	server := New(Dependencies{Store: db, Computer: failingAuthorityExecutor{err: wanted}})
	err := server.revokeAllComputerAuthority(t.Context(), pve.VM{VMID: 158})
	if !errors.Is(err, wanted) {
		t.Fatalf("expected takeover to fail closed, got %v", err)
	}
}

type recordingAuthorityExecutor struct {
	revocations []computer.Authority
}

func (*recordingAuthorityExecutor) Execute(context.Context, pve.VM, computer.Request, time.Duration) (computer.Response, error) {
	return computer.Response{}, nil
}

func (*recordingAuthorityExecutor) ActivateAuthority(context.Context, pve.VM, computer.Authority) error {
	return nil
}

func (r *recordingAuthorityExecutor) RevokeAuthority(_ context.Context, _ pve.VM, authority computer.Authority) error {
	r.revocations = append(r.revocations, authority)
	return nil
}

func TestHumanTakeoverAlwaysWritesGuestDenialTombstone(t *testing.T) {
	db, _ := regressionDB(t)
	if err := db.UpsertManagedDesktop(t.Context(), store.ManagedDesktop{VMID: 158, Node: "test", DisplayName: "Epoch test"}); err != nil {
		t.Fatal(err)
	}
	executor := &recordingAuthorityExecutor{}
	server := New(Dependencies{Store: db, Computer: executor})
	if err := server.revokeAllComputerAuthority(t.Context(), pve.VM{VMID: 158}); err != nil {
		t.Fatal(err)
	}
	if len(executor.revocations) != 1 {
		t.Fatalf("expected one unconditional Guest revocation, got %d", len(executor.revocations))
	}
	authority := executor.revocations[0]
	if authority.State != "revoked" || authority.LeaseID != "lease_human_takeover_tombstone" || authority.ControlEpoch != 1 {
		t.Fatalf("unexpected human takeover tombstone: %#v", authority)
	}
}

func TestHumanTakeoverWithoutEpochStoreFailsClosed(t *testing.T) {
	executor := &recordingAuthorityExecutor{}
	server := New(Dependencies{Computer: executor})
	if err := server.revokeAllComputerAuthority(t.Context(), pve.VM{VMID: 158}); !errors.Is(err, computer.ErrUnavailable) || len(executor.revocations) != 0 {
		t.Fatal("takeover guessed an epoch", err)
	}
}

func TestComputerDesktopGateSerializesTakeoverWithAction(t *testing.T) {
	db, _ := regressionDB(t)
	unlockFirst, err := db.AcquireDesktopControlLock(t.Context(), 158)
	if err != nil {
		t.Fatal(err)
	}
	defer unlockFirst()
	acquired := make(chan struct{})
	go func() {
		unlockSecond, err := db.AcquireDesktopControlLock(t.Context(), 158)
		if err != nil {
			t.Error(err)
			return
		}
		close(acquired)
		unlockSecond()
	}()
	select {
	case <-acquired:
		t.Fatal("a human takeover must wait until the in-flight desktop action leaves the gate")
	case <-time.After(50 * time.Millisecond):
	}
	unlockFirst()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("desktop gate was not released")
	}
}

func TestDesktopAccessPolicyCommandsEnforceSystemGroups(t *testing.T) {
	linuxAdmin, err := desktopAccessPolicyCommand("linux", "local_admin")
	if err != nil {
		t.Fatal(err)
	}
	if len(linuxAdmin) != 3 || !strings.Contains(linuxAdmin[2], `usermod -a -G sudo "$username"`) ||
		!strings.Contains(linuxAdmin[2], `/etc/sudoers.d/vc-workspace-$username`) ||
		!strings.Contains(linuxAdmin[2], `runuser -u "$username" -- sudo -n`) {
		t.Fatalf("unexpected Linux administrator command: %#v", linuxAdmin)
	}
	linuxStandard, err := desktopAccessPolicyCommand("linux", "standard")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(linuxStandard[2], `gpasswd -d "$username" sudo`) ||
		!strings.Contains(linuxStandard[2], `/etc/sudoers.d/vc-workspace-$username`) ||
		!strings.Contains(linuxStandard[2], `runuser -u "$username" -- sudo -n`) ||
		!strings.Contains(linuxStandard[2], `terminate-user "$username"`) {
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
	perUser, err := desktopAccessPolicyCommandForUser("linux", "standard", managedGuestUsername("user-123"))
	if err != nil || strings.Contains(strings.Join(perUser, " "), "user-123") {
		t.Fatalf("expected a safe opaque per-user guest account command: command=%#v error=%v", perUser, err)
	}
	if _, err := desktopAccessPolicyCommandForUser("linux", "standard", "bad user"); err == nil {
		t.Fatal("expected unsafe guest username to be rejected")
	}
}

func TestDesktopSessionPolicyCommandsEnforceRDPChannelsAndBackground(t *testing.T) {
	policy := store.DesktopAccessPolicy{
		ClipboardRedirection: false,
		DriveRedirection:     false,
		ManagedBackground:    true,
	}
	linux, err := desktopSessionPolicyCommand("linux", policy)
	if err != nil {
		t.Fatal(err)
	}
	joinedLinux := strings.Join(linux, " ")
	for _, required := range []string{
		"set_ini_key \"$xrdp_ini\" Channels cliprdr false",
		"set_ini_key \"$xrdp_ini\" Channels rdpdr false",
		"Security RestrictInboundClipboard all",
		"Security RestrictOutboundClipboard all",
		"Chansrv EnableFuseMount false",
		"background-policy",
		"systemctl restart xrdp-sesman.service xrdp.service",
	} {
		if !strings.Contains(joinedLinux, required) {
			t.Fatalf("Linux session policy is missing %q: %s", required, joinedLinux)
		}
	}
	windows, err := desktopSessionPolicyCommand("windows", policy)
	if err != nil {
		t.Fatal(err)
	}
	joinedWindows := strings.Join(windows, " ")
	for _, required := range []string{"fDisableClip", "fDisableCdm", "-Value 1", "background-policy", "gpupdate.exe"} {
		if !strings.Contains(joinedWindows, required) {
			t.Fatalf("Windows session policy is missing %q: %s", required, joinedWindows)
		}
	}
	if _, err := desktopSessionPolicyCommand("unknown", policy); err == nil {
		t.Fatal("expected an unknown desktop OS to reject the session policy")
	}
}

func TestNativeSessionPolicySnapshotIsStableAndSensitiveToControls(t *testing.T) {
	base := store.DesktopAccessPolicy{DesiredRevision: 3, AppliedRevision: 3, ClipboardRedirection: true, ManagedBackground: true}
	first := nativeSessionPolicySnapshot(base)
	second := nativeSessionPolicySnapshot(base)
	if first != second || !strings.HasPrefix(first.Hash, "sha256:") || len(first.Hash) != 71 {
		t.Fatalf("unexpected stable policy snapshot: first=%#v second=%#v", first, second)
	}
	base.ClipboardRedirection = false
	if changed := nativeSessionPolicySnapshot(base); changed.Hash == first.Hash {
		t.Fatal("policy hash must change when an enforced control changes")
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
	if len(reader.paths) != 3 || reader.paths[0] != "/var/lib/vc-workspace/desktop-ready" || reader.paths[1] != "/var/lib/vc-vdi/desktop-ready" || reader.paths[2] != `C:\ProgramData\VC Workspace\Agent\desktop-ready` {
		t.Fatalf("unexpected readiness paths: %#v", reader.paths)
	}
}

func TestReadDesktopReadySupportsLegacyWindowsMarker(t *testing.T) {
	reader := &readinessFileReader{readyPath: `C:\ProgramData\VC VDI\Agent\desktop-ready`}
	if err := readDesktopReady(context.Background(), reader, "node-1", 9112); err != nil {
		t.Fatal(err)
	}
	if len(reader.paths) != 4 || reader.paths[3] != `C:\ProgramData\VC VDI\Agent\desktop-ready` {
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
		Mode: "pci_passthrough", VendorID: "0x8086", DeviceClass: "0x030000", ResourceMapping: "vc-workspace-intel-igpu",
	}
	mappings := []pve.PCIResourceMapping{{
		ID:      "vc-workspace-intel-igpu",
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
		Mode: "pci_passthrough", VendorID: "0x8086", DeviceClass: "0x030000", ResourceMapping: "vc-workspace-intel-igpu",
	}
	mappings := []pve.PCIResourceMapping{{
		ID:      "vc-workspace-intel-igpu",
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
		Mode: "mdev", VendorID: "0x8086", DeviceClass: "0x030000", ResourceMapping: "vc-workspace-intel-gvtg", MDevType: "i915-GVTg_V5_4",
	}
	mappings := []pve.PCIResourceMapping{{
		ID: "vc-workspace-intel-gvtg", MDev: true,
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
		ID: "vc-workspace-intel-igpu", Description: "Intel iGPU",
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
	request := pciResourceMappingRequest{ID: "vc-workspace-intel-igpu", Entries: []pciResourceMappingEntryRequest{{Node: "infra-node4", DeviceID: device.ID}}}
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
		ID: "vc-workspace-intel-gvtg", Description: "Intel GVT-g", MDev: true,
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
	if !isManagedDesktop(pve.VM{Name: "desktop", Kind: "qemu", Tags: []string{"vc-workspace"}}) {
		t.Fatal("expected a VC Workspace QEMU guest to be managed")
	}
	if !isManagedDesktop(pve.VM{Name: "legacy-desktop", Kind: "qemu", Tags: []string{"vc-vdi"}}) {
		t.Fatal("expected the former PVE tag to remain readable during migration")
	}
	for _, machine := range []pve.VM{
		{Name: "vc-workspace-name-only", Kind: "qemu"},
		{Name: "template", Kind: "qemu", Template: true, Tags: []string{"vc-workspace"}},
		{Name: "container", Kind: "lxc", Tags: []string{"vc-workspace"}},
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

func TestManagedGuestUsernameIsStableOpaqueAndWindowsSafe(t *testing.T) {
	first := managedGuestUsername("user-123")
	if first != managedGuestUsername("user-123") || first == managedGuestUsername("user-456") {
		t.Fatalf("guest username mapping is not stable and unique: %q", first)
	}
	if !guestUsernamePattern.MatchString(first) || len(first) > 20 || strings.Contains(first, "user") {
		t.Fatalf("guest username is not opaque and cross-platform safe: %q", first)
	}
}

func TestOIDCGroupIDIsStableAndDoesNotExposeTheClaim(t *testing.T) {
	first := oidcGroupID("default", "Directory Administrators")
	if first != oidcGroupID("default", "Directory Administrators") || first == oidcGroupID("other", "Directory Administrators") {
		t.Fatalf("OIDC group mapping is not stable and provider scoped: %q", first)
	}
	if strings.Contains(first, "Directory") || !strings.HasPrefix(first, "oidcg_") {
		t.Fatalf("OIDC group ID exposes the external claim: %q", first)
	}
}

func TestIdentityProfileValidationRejectsInlineSecretsAndUnsafeMultilineValues(t *testing.T) {
	valid := map[string]any{"domain": "ad.example.com", "realm": "AD.EXAMPLE.COM", "allowed_groups": "VC Workspace Users"}
	if err := validateIdentityProfileConfig("linux", "linux_sssd_ad", valid); err != nil {
		t.Fatalf("valid AD profile was rejected: %v", err)
	}
	for _, invalid := range []map[string]any{
		{"domain": "ad.example.com", "realm": "AD.EXAMPLE.COM", "allowed_groups": "users", "bind_password": "secret"},
		{"domain": "ad.example.com\nservices = sudo", "realm": "AD.EXAMPLE.COM", "allowed_groups": "users"},
		{"domain": "ad.example.com", "realm": "AD.EXAMPLE.COM", "allowed_groups": "users\n[domain/injected]"},
	} {
		if err := validateIdentityProfileConfig("linux", "linux_sssd_ad", invalid); err == nil {
			t.Fatalf("unsafe profile was accepted: %#v", invalid)
		}
	}
	if err := validateIdentityProfileConfig("windows", "windows_ad", map[string]any{"domain": "ad.example.com", "join_method": "offline", "allowed_group": "AD\\VDI Users"}); err == nil {
		t.Fatal("unsupported offline Windows AD join was accepted")
	}
	if err := validateIdentityProfileConfig("windows", "windows_entra", map[string]any{"tenant_id": "not-a-tenant", "target_hostname": "desktop-01", "allowed_principal": "user@example.com"}); err == nil {
		t.Fatal("invalid Entra tenant ID was accepted")
	}
}

func TestDirectoryReconcilePlansKeepOneTimeSecretsOutOfArgv(t *testing.T) {
	const secret = "not-in-command-argv"
	profiles := []store.IdentityProfile{
		{Platform: "linux", Mode: "linux_sssd_ad", Config: json.RawMessage(`{"domain":"ad.example.com","realm":"AD.EXAMPLE.COM","allowed_groups":"VDI Users"}`)},
		{Platform: "windows", Mode: "windows_ad", Config: json.RawMessage(`{"domain":"ad.example.com","join_method":"online","allowed_group":"AD\\VDI Users"}`)},
		{Platform: "linux", Mode: "linux_sssd_oidc", Config: json.RawMessage(`{"domain":"entra","idp_type":"entra_id","client_id":"client","token_endpoint":"https://login.example/token","userinfo_endpoint":"https://graph.example/me","device_auth_endpoint":"https://login.example/device","id_scope":"scope"}`)},
	}
	requests := []desktopIdentityReconcileRequest{
		{JoinUsername: "joiner@ad.example.com", JoinPassword: secret},
		{JoinUsername: "joiner@ad.example.com", JoinPassword: secret},
		{ClientSecret: secret},
	}
	for index, profile := range profiles {
		plan, err := buildGuestIdentityReconcilePlan(profile, requests[index])
		if err != nil {
			t.Fatalf("build plan %d: %v", index, err)
		}
		if strings.Contains(strings.Join(plan.command, " "), secret) || !strings.Contains(string(plan.input), secret) {
			t.Fatalf("plan %d did not isolate its one-time secret", index)
		}
	}
}

func TestLDAPReconcilePlanUsesCurrentDebianSSSDConfigAndRealShellNewlines(t *testing.T) {
	profile := store.IdentityProfile{
		Platform: "linux",
		Mode:     "linux_sssd_ldap",
		Config:   json.RawMessage(`{"domain":"directory.example","uri":"ldap://ldap.example","search_base":"dc=example,dc=test","access_filter":"(employeeType=workspace)"}`),
	}
	plan, err := buildGuestIdentityReconcilePlan(profile, desktopIdentityReconcileRequest{})
	if err != nil {
		t.Fatalf("build LDAP plan: %v", err)
	}
	if len(plan.command) != 3 || plan.command[0] != "/bin/sh" || plan.command[1] != "-c" {
		t.Fatalf("unexpected LDAP plan command: %#v", plan.command)
	}
	script := plan.command[2]
	if strings.Contains(script, `set -eu\n`) || !strings.HasPrefix(script, "set -eu\n") {
		t.Fatalf("LDAP shell plan does not contain executable newlines: %q", script)
	}
	const prefix = "printf '%s' '"
	start := strings.Index(script, prefix)
	if start < 0 {
		t.Fatalf("LDAP shell plan does not install sssd.conf: %q", script)
	}
	start += len(prefix)
	end := strings.Index(script[start:], "' | base64 -d")
	if end < 0 {
		t.Fatalf("LDAP shell plan has no encoded sssd.conf boundary: %q", script)
	}
	contents, err := base64.StdEncoding.DecodeString(script[start : start+end])
	if err != nil {
		t.Fatalf("decode generated sssd.conf: %v", err)
	}
	config := string(contents)
	if strings.Contains(config, "config_file_version") {
		t.Fatalf("generated sssd.conf contains the option removed by SSSD 2.10: %s", config)
	}
	if !strings.Contains(config, "ldap_tls_cacert = /etc/ssl/certs/ca-certificates.crt") || !strings.Contains(config, "ldap_id_use_start_tls = true") {
		t.Fatalf("generated LDAP config does not use the system CA with StartTLS: %s", config)
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
