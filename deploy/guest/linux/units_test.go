package linuxguest

import (
	"strings"
	"testing"
)

func TestReadinessAndBoundedOfflineReaperHaveSeparatePermissions(t *testing.T) {
	files, err := Units.ReadDir(".")
	if err != nil || len(files) != 3 {
		t.Fatal("exact canonical unit set required", err)
	}
	checks := map[string][]string{
		"vc-workspace-agent.service":    {"Requires=vc-workspace-accounts.timer", "ExecStart=/usr/local/sbin/vc-workspace-guest-agent --readiness-only --state-dir /var/lib/vc-workspace", "ReadWritePaths=/var/lib/vc-workspace", "PrivateTmp=true", "Restart=always"},
		"vc-workspace-accounts.service": {"Type=oneshot", "ExecStart=/usr/local/sbin/vc-workspace-guest-agent computer-v2-accounts-reconcile", "TimeoutStartSec=45", "TimeoutStopSec=5", "KillMode=control-group", "ProtectSystem=strict", "ProtectHome=true", "ProtectControlGroups=true", "RestrictAddressFamilies=AF_UNIX", "ReadWritePaths=/var/lib/vc-workspace/computer-v2 /etc /sys/fs/cgroup/user.slice /tmp", "PrivateTmp=false", "CapabilityBoundingSet=CAP_CHOWN CAP_DAC_OVERRIDE CAP_FOWNER CAP_KILL"},
		"vc-workspace-accounts.timer":   {"OnBootSec=5s", "OnUnitInactiveSec=10s", "AccuracySec=1s", "Unit=vc-workspace-accounts.service", "WantedBy=timers.target"},
	}
	for name, lines := range checks {
		raw, err := Units.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range lines {
			if strings.Count("\n"+string(raw), "\n"+line+"\n") != 1 {
				t.Fatalf("%s missing or duplicates %s", name, line)
			}
		}
	}
}
