package httpapi

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

//go:embed login_tree_fixture.py
var loginTreeFixture string

// Kernel/process-domain evidence only, not default MCP or xrdp integration.
func TestLiveLinuxLoginProcessTree(t *testing.T) {
	if os.Getenv("VC_WORKSPACE_LIVE_LOGIN_TREE_AUDIT") != "true" {
		t.Skip("explicit isolated process tree acceptance required")
	}
	vmid, err := strconv.Atoi(os.Getenv("VC_WORKSPACE_LIVE_GUEST_SERVICE_VMID"))
	if err != nil || vmid <= 0 || vmid == 158 {
		t.Fatal("explicit non-business acceptance VMID required")
	}
	client := livePrivilegeClient(t)
	machine := liveMachine(t, client, vmid)
	if machine.Status != "running" || machine.Template || !isManagedDesktop(machine) || !strings.HasSuffix(machine.Name, "-check") {
		t.Fatal("running isolated acceptance desktop required")
	}
	config, err := client.VMConfiguration(t.Context(), machine.Node, vmid)
	if err != nil || config.OSType != "l26" {
		t.Fatal("Linux required", err)
	}
	preflight, err := client.ExecGuest(t.Context(), machine.Node, vmid, []string{"/bin/sh", "-c", "set -eu\ntest \"$(. /etc/os-release; echo $VERSION_ID)\" = 13\ntest -z \"$(ps -C Xorg -o pid=)\"\ntest -f /sys/fs/cgroup/cgroup.controllers\n/usr/bin/python3 -c 'from gi.repository import Gio, GLib; import os, signal; assert hasattr(os, \"pidfd_open\") and hasattr(signal, \"pidfd_send_signal\")'\nsha256sum /etc/pam.d/xrdp-sesman /etc/xrdp/sesman.ini /etc/xrdp/xrdp.ini"})
	if err != nil || preflight.ExitCode != 0 {
		t.Fatal("idle Debian 13, cgroup v2 and Gio UnixFD support required", err, preflight.ExitCode)
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	marker := "tree" + hex.EncodeToString(nonce[:])
	t.Logf("isolated process tree fixture %s on VM %d", marker, vmid)
	fixture := func(ctx context.Context, operation string) (map[string]any, error) {
		result, err := client.ExecGuestWithInput(ctx, machine.Node, vmid, []string{"/usr/bin/python3", "-c", loginTreeFixture, operation, marker}, []byte(loginTreeFixture))
		if err != nil || result.ExitCode != 0 {
			return nil, fmt.Errorf("tree fixture %s: exit=%d stderr=%s transport=%v", operation, result.ExitCode, result.Stderr, err)
		}
		var value map[string]any
		if err := json.Unmarshal([]byte(result.Stdout), &value); err != nil {
			return nil, fmt.Errorf("tree fixture %s: incomplete receipt", operation)
		}
		return value, nil
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		if _, err := fixture(ctx, "cleanup"); err != nil {
			t.Error("retaining process tree recovery manifest", err)
			return
		}
		result, err := client.ExecGuest(ctx, machine.Node, vmid, []string{"sha256sum", "/etc/pam.d/xrdp-sesman", "/etc/xrdp/sesman.ini", "/etc/xrdp/xrdp.ini"})
		if err != nil || result.ExitCode != 0 || result.Stdout != preflight.Stdout {
			t.Error("PAM/xrdp configuration changed", err)
		}
	})
	if _, err := fixture(t.Context(), "setup"); err != nil {
		t.Fatal(err)
	}
	value, err := fixture(t.Context(), "test")
	if err != nil || value["stage"] != "verified" {
		t.Fatal("process tree proof failed", err)
	}
	for _, key := range []string{"orphan_root_reproduced", "late_uid_reproduced", "pidfd_attachment", "kernel_subtree_kill", "control_survived", "fresh_scope_survived_old_kill"} {
		if value[key] != true {
			t.Fatal("missing process tree assertion", key)
		}
	}
	t.Log("actual orphan root child escaped parent-only cleanup and entered UID late; PIDFD-attached scope retained it and kernel subtree kill closed it without touching control or fresh-login scopes")
}
