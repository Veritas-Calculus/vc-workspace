package imagebuilder

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This validates the actual recipe and generated variables with the installed
// Packer plugin. It never calls build, authenticates to PVE or provisions a VM.
// The small artifact fixtures validate configuration, not package installation.
func TestPackerTemplateConfigValidation(t *testing.T) {
	if os.Getenv("VC_WORKSPACE_TEST_PACKER_VALIDATE") != "true" {
		t.Skip("run make images-packer-check with Packer and the Proxmox plugin installed")
	}
	packer, err := exec.LookPath("packer")
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs("../../deploy/images")
	if err != nil {
		t.Fatal(err)
	}
	interfaces, err := net.Interfaces()
	if err != nil || len(interfaces) == 0 {
		t.Fatal("cannot identify an interface for configuration validation", err)
	}

	for _, test := range []struct {
		name, bind, iface string
		request           Request
	}{
		{name: "debian-default", request: validDebianRequest()},
		{name: "debian-address", bind: "127.0.0.1", request: validDebianRequest()},
		{name: "debian-interface", iface: interfaces[0].Name, request: validDebianRequest()},
		{name: "windows-11", request: validWindowsRequest()},
		{name: "windows-10", request: func() Request {
			r := validWindowsRequest()
			r.ProfileID = "windows-10-22h2"
			r.WindowsImageName = "Windows 10 Enterprise Evaluation"
			return r
		}()},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := testBuilder(t).config
			config.PackerPath = packer
			config.RootDir = root
			config.PVEEndpoint = "https://pve.invalid"
			config.HTTPBindAddress = test.bind
			config.HTTPInterface = test.iface
			builder, err := New(config)
			if err != nil {
				t.Fatal(err)
			}
			spec, err := builder.commandSpec(test.request)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, packer, "validate", spec.args[len(spec.args)-1])
			cmd.Dir = spec.dir
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "PKR_VAR_") && !strings.HasPrefix(entry, "PACKER_LOG") {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			cmd.Env = append(cmd.Env, spec.env...)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("real Packer configuration validation: %v\n%s", err, output)
			}
		})
	}
}
