package pve

import (
	"os/exec"
	"strings"
	"testing"
)

func TestRDPNetworkLabProfilesAndSafetyGate(t *testing.T) {
	script := "../../deploy/pve/rdp-network-lab.sh"
	if output, err := exec.Command("bash", "-n", script).CombinedOutput(); err != nil {
		t.Fatalf("syntax: %v %s", err, output)
	}
	for profile, expected := range map[string]string{
		"latency": "delay 120ms 20ms distribution normal",
		"limited": "delay 120ms 20ms distribution normal rate 6mbit",
		"loss":    "delay 120ms 20ms distribution normal loss random 1% rate 6mbit",
		"outage":  "loss 100%",
	} {
		output, err := exec.Command("bash", script, "--describe", profile).CombinedOutput()
		if err != nil || strings.TrimSpace(string(output)) != expected {
			t.Fatalf("%s: %v %s", profile, err, output)
		}
	}
	if err := exec.Command("bash", script, "--describe", "unknown").Run(); err == nil {
		t.Fatal("unknown profile accepted")
	}
	// No acknowledgement: must stop before executing any networking command,
	// even on a Linux CI runner that happens to be root.
	cmd := exec.Command("bash", script, "not-a-real-test-host", "ens18", "10.0.0.1", "outage", "10")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "VC_WORKSPACE_NETWORK_LAB_ACK="}
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "explicit isolated-guest acknowledgement") {
		t.Fatalf("missing safety gate: %v %s", err, output)
	}
}
