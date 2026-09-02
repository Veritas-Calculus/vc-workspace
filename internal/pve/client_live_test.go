package pve

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestLivePCIResourceMappings(t *testing.T) {
	client := liveClient(t)
	mappings, err := client.PCIResourceMappings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("read %d PCI resource mappings", len(mappings))
}

func TestLiveMaintainedDesktopTemplates(t *testing.T) {
	if os.Getenv("VC_VDI_LIVE_TEMPLATE_AUDIT") != "true" {
		t.Skip("set VC_VDI_LIVE_TEMPLATE_AUDIT=true to audit the maintained VC Workspace templates")
	}
	client := liveClient(t)
	summary, err := client.Summary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	expectations := []struct {
		vmid        int
		osType      string
		requireUEFI bool
		requireTPM  bool
	}{
		{vmid: 9100, osType: "l26"},
		{vmid: 9110, osType: "win10", requireUEFI: true, requireTPM: true},
		{vmid: 9111, osType: "win11", requireUEFI: true, requireTPM: true},
	}
	for _, expectation := range expectations {
		t.Run(fmt.Sprintf("vmid_%d", expectation.vmid), func(t *testing.T) {
			var template VM
			found := false
			for _, vm := range summary.VMs {
				if vm.VMID == expectation.vmid {
					template, found = vm, true
					break
				}
			}
			if !found {
				t.Fatalf("template VMID %d was not found", expectation.vmid)
			}
			if template.Kind != "qemu" || !template.Template || template.Status != "stopped" {
				t.Fatalf("VMID %d is not a stopped QEMU template: %#v", expectation.vmid, template)
			}
			configuration, err := client.VMConfiguration(t.Context(), template.Node, template.VMID)
			if err != nil {
				t.Fatal(err)
			}
			if configuration.OSType != expectation.osType || !configuration.AgentEnabled || configuration.Cores < 1 || configuration.MemoryMB < 512 {
				t.Fatalf("template VMID %d has an invalid base configuration: %#v", expectation.vmid, configuration)
			}
			for _, requiredTag := range []string{"vc-vdi", "template", "rdp"} {
				if !containsString(configuration.Tags, requiredTag) {
					t.Fatalf("template VMID %d is missing tag %q: %#v", expectation.vmid, requiredTag, configuration.Tags)
				}
			}
			if len(configuration.Disks) == 0 || len(configuration.NetworkAdapters) == 0 {
				t.Fatalf("template VMID %d is missing disk or network hardware", expectation.vmid)
			}
			if len(configuration.PCIHostDevices) != 0 {
				t.Fatalf("base template VMID %d must not own a host PCI device", expectation.vmid)
			}
			if expectation.requireUEFI && (configuration.BIOS != "ovmf" || configuration.EFIDisk == "") {
				t.Fatalf("template VMID %d is missing OVMF/EFI state", expectation.vmid)
			}
			if expectation.requireTPM && (!strings.Contains(configuration.TPMState, "version=v2.0") || configuration.TPMState == "") {
				t.Fatalf("template VMID %d is missing TPM 2.0 state", expectation.vmid)
			}
			t.Logf("verified VMID %d (%s) on %s", template.VMID, configuration.OSType, template.Node)
		})
	}
}

func TestLiveGPUInventory(t *testing.T) {
	if os.Getenv("VC_VDI_LIVE_GPU_AUDIT") != "true" {
		t.Skip("set VC_VDI_LIVE_GPU_AUDIT=true to audit the live PVE GPU inventory")
	}
	client := liveClient(t)
	summary, err := client.Summary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	nodes := make([]string, 0, len(summary.Nodes))
	for _, node := range summary.Nodes {
		if node.Status == "online" {
			nodes = append(nodes, node.Name)
		}
	}
	devices, err := client.GPUDevices(t.Context(), nodes)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) == 0 {
		t.Fatal("no display controller was found on an online PVE node")
	}
	for _, device := range devices {
		group := "none"
		if device.IOMMUGroup != nil {
			group = fmt.Sprint(*device.IOMMUGroup)
		}
		hardwareID := strings.TrimPrefix(device.VendorID, "0x") + ":" + strings.TrimPrefix(device.DeviceID, "0x")
		subsystemID := strings.TrimPrefix(device.SubsystemVendorID, "0x") + ":" + strings.TrimPrefix(device.SubsystemDeviceID, "0x")
		mdevTypes := make([]string, 0, len(device.MDevTypes))
		for _, kind := range device.MDevTypes {
			mdevTypes = append(mdevTypes, fmt.Sprintf("%s=%d", kind.Type, kind.Available))
		}
		if device.MDevCapable && len(mdevTypes) == 0 {
			t.Fatalf("%s %s reports mdev capability without any mediated-device types", device.Node, device.ID)
		}
		t.Logf("%s %s hardware=%s subsystem=%s iommu=%s assignable=%t mdev=%s", device.Node, device.ID, hardwareID, subsystemID, group, device.Assignable, strings.Join(mdevTypes, ","))
	}
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func liveClient(t *testing.T) *Client {
	t.Helper()
	endpoint := strings.TrimSpace(os.Getenv("VC_VDI_LIVE_PVE_ENDPOINT"))
	credentialFile := strings.TrimSpace(os.Getenv("VC_VDI_LIVE_PVE_CREDENTIAL_FILE"))
	if endpoint == "" || credentialFile == "" {
		t.Skip("set VC_VDI_LIVE_PVE_ENDPOINT and VC_VDI_LIVE_PVE_CREDENTIAL_FILE to run read-only PVE checks")
	}
	contents, err := os.ReadFile(credentialFile)
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(contents))
	if len(fields) != 3 || fields[1] != "/" {
		t.Fatal("PVE credential file must contain username@realm / password")
	}
	client, err := New(Config{Endpoint: endpoint, Username: fields[0], Password: fields[2]})
	if err != nil {
		t.Fatal(err)
	}
	return client
}
