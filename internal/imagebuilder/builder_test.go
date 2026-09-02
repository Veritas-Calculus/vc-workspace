package imagebuilder

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandSpecKeepsSecretsOutOfArgumentsAndUsesConfiguredMirror(t *testing.T) {
	builder := testBuilder(t)
	request := validDebianRequest()
	spec, err := builder.commandSpec(request)
	if err != nil {
		t.Fatal(err)
	}
	joinedArgs := strings.Join(spec.args, " ")
	if strings.Contains(joinedArgs, request.BuilderPassword) || strings.Contains(joinedArgs, "token-secret") {
		t.Fatal("secret was placed in process arguments")
	}
	environment := strings.Join(spec.env, "\n")
	for _, expected := range []string{
		"PKR_VAR_mirror_host=mirror.example.com",
		"PKR_VAR_mirror_url=http://mirror.example.com/debian",
		"PKR_VAR_security_mirror_url=http://mirror.example.com/debian-security",
		"PKR_VAR_cores=6",
		"PKR_VAR_memory_mb=6144",
		"PKR_VAR_disk_size=48G",
	} {
		if !strings.Contains(environment, expected) {
			t.Fatalf("environment does not contain %q", expected)
		}
	}
}

func TestValidateRequestRequiresWindowsDriverMediaAndUEFI(t *testing.T) {
	request := validWindowsRequest()
	request.DriverISO = ""
	if err := ValidateRequest(request); err == nil {
		t.Fatal("expected missing Windows driver ISO to fail")
	}
	request = validWindowsRequest()
	request.Firmware = "seabios"
	if err := ValidateRequest(request); err == nil {
		t.Fatal("expected Windows without UEFI to fail")
	}
}

func TestValidateRequestRejectsCredentialsInMirrorAndUnsafePassword(t *testing.T) {
	request := validDebianRequest()
	request.MirrorURL = "http://user:pass@mirror.example.com/debian"
	if err := ValidateRequest(request); err == nil {
		t.Fatal("expected mirror credentials to fail")
	}
	request = validDebianRequest()
	request.BuilderPassword = "unsafe&password"
	if err := ValidateRequest(request); err == nil {
		t.Fatal("expected XML-sensitive password to fail")
	}
}

func TestProgressWriterRedactsSecretsAndNeverMovesBackward(t *testing.T) {
	var updates []Progress
	writer := &progressWriter{report: func(progress Progress) { updates = append(updates, progress) }, secrets: []string{"build-secret"}}
	_, _ = writer.Write([]byte("Waiting for SSH with build-secret\ncreating disk\n"))
	if len(updates) != 2 {
		t.Fatalf("got %d updates", len(updates))
	}
	if updates[0].Percent != 45 || updates[1].Percent != 45 {
		t.Fatalf("progress moved unexpectedly: %#v", updates)
	}
	if strings.Contains(writer.Last(), "build-secret") || !strings.Contains(updates[0].Detail, "[redacted]") {
		t.Fatal("progress output did not redact secret")
	}
}

func testBuilder(t *testing.T) *Builder {
	t.Helper()
	root := t.TempDir()
	for _, relative := range []string{definitions["debian-13-xfce"].templatePath, definitions["windows-11"].templatePath} {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("packer {}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	artifacts := make([]string, 3)
	for index, name := range []string{"linux-agent", "windows-agent.exe", "cloudbase.msi"} {
		artifacts[index] = filepath.Join(root, name)
		if err := os.WriteFile(artifacts[index], []byte("fixture"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	builder, err := New(Config{
		Enabled: true, PackerPath: "/usr/bin/true", RootDir: root,
		PVEEndpoint: "https://pve.example.com", PVETokenID: "builder@pve!packer", PVETokenSecret: "token-secret",
		LinuxAgentBinary: artifacts[0], WindowsAgentBinary: artifacts[1], CloudbaseInitMSI: artifacts[2],
	})
	if err != nil {
		t.Fatal(err)
	}
	return builder
}

func validDebianRequest() Request {
	return Request{
		ProfileID: "debian-13-xfce", Node: "infra-node6", VMID: 9200,
		SourceISO:         "local:iso/debian-13.6.0-amd64-netinst.iso",
		SourceISOChecksum: "sha256:65273beed27b2df543b68b65630ba525cfbad8df2b12035732b2dff87d6664e7",
		MirrorURL:         "http://mirror.example.com/debian", SecurityMirrorURL: "http://mirror.example.com/debian-security",
		StoragePool: "ceph-pve", Bridge: "vmbr0", Cores: 6, MemoryMB: 6144, DiskGB: 48,
		Firmware: "seabios", TPMVersion: "none", BuilderPassword: "BuildPass-123!",
	}
}

func validWindowsRequest() Request {
	return Request{
		ProfileID: "windows-11", Node: "infra-node4", VMID: 9201,
		SourceISO:         "local:iso/Win11_25H2_Enterprise_Eval_zh-cn_x64.iso",
		SourceISOChecksum: "sha256:7b4ac87391b659f7724229682b642256289a1c00504056249f0f12029157d3d2",
		DriverISO:         "local:iso/virtio-win-0.1.271.iso",
		DriverISOChecksum: "sha256:0040e268e1095b080abfec74214d094bc7fe565568533505b99d70622061c187",
		StoragePool:       "ceph-pve", Bridge: "vmbr0", WindowsImageName: "Windows 11 Enterprise Evaluation",
		Cores: 4, MemoryMB: 8192, DiskGB: 64, Firmware: "uefi", TPMVersion: "2.0",
		BuilderPassword: "BuildPass-123!",
	}
}
