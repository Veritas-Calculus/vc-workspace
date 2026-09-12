package computer

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
)

func windowsObservationFixture() map[string]any {
	return map[string]any{
		"schema_version": 1, "observation_version": 1,
		"username": "vca0123456789ab", "sid": "S-1-5-21-1-2-3-1001",
		"exists": true, "disabled": true, "expires_unix_seconds": uint32(1), "sessions": []string{},
	}
}

func TestWindowsAccountObservationRequiresExplicitBoundSAMAndWTSState(t *testing.T) {
	for _, test := range []struct {
		name    string
		change  func(map[string]any)
		stopped bool
	}{
		{"disabled-no-logon", func(map[string]any) {}, true},
		{"enabled-no-logon", func(v map[string]any) { v["disabled"] = false }, false},
		{"disabled-retained-logon", func(v map[string]any) { v["sessions"] = []string{"windows:2:0000000000000001"} }, false},
		{"deleted-no-logon", func(v map[string]any) { v["exists"], v["disabled"], v["expires_unix_seconds"] = false, nil, nil }, true},
		{"deleted-retained-logon", func(v map[string]any) {
			v["exists"], v["disabled"], v["expires_unix_seconds"] = false, nil, nil
			v["sessions"] = []string{"windows:2:0000000000000001"}
		}, false},
		{"unbounded-but-observed", func(v map[string]any) { v["disabled"], v["expires_unix_seconds"] = false, uint32(0xffffffff) }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := windowsObservationFixture()
			test.change(value)
			raw, _ := json.Marshal(value)
			state, err := parseWindowsAccountObservation(string(raw), value["username"].(string), value["sid"].(string))
			if err != nil || state.LoginStopped() != test.stopped || state.Exists != value["exists"] {
				t.Fatal("incorrect account/WTS evidence", state, err)
			}
		})
	}
	if (WindowsAccountObservation{}).LoginStopped() {
		t.Fatal("zero or failed observation must not acknowledge logoff")
	}
}

func TestWindowsAccountObservationRejectsIncompleteContradictoryOrReboundReceipts(t *testing.T) {
	changes := map[string]func(map[string]any){
		"old-protocol":        func(v map[string]any) { delete(v, "observation_version") },
		"wrong-schema":        func(v map[string]any) { v["schema_version"] = 2 },
		"wrong-user":          func(v map[string]any) { v["username"] = "vca111111111111" },
		"wrong-sid":           func(v map[string]any) { v["sid"] = "S-1-5-21-1-2-3-1002" },
		"missing-exists":      func(v map[string]any) { delete(v, "exists") },
		"null-exists":         func(v map[string]any) { v["exists"] = nil },
		"missing-disabled":    func(v map[string]any) { delete(v, "disabled") },
		"null-disabled":       func(v map[string]any) { v["disabled"] = nil },
		"missing-expiry":      func(v map[string]any) { delete(v, "expires_unix_seconds") },
		"null-expiry":         func(v map[string]any) { v["expires_unix_seconds"] = nil },
		"negative-expiry":     func(v map[string]any) { v["expires_unix_seconds"] = -1 },
		"overflow-expiry":     func(v map[string]any) { v["expires_unix_seconds"] = uint64(0x100000000) },
		"string-expiry":       func(v map[string]any) { v["expires_unix_seconds"] = "1" },
		"missing-with-status": func(v map[string]any) { v["exists"] = false },
		"missing-sessions":    func(v map[string]any) { delete(v, "sessions") },
		"null-sessions":       func(v map[string]any) { v["sessions"] = nil },
		"session-zero":        func(v map[string]any) { v["sessions"] = []string{"windows:0:0000000000000001"} },
		"invalid-luid":        func(v map[string]any) { v["sessions"] = []string{"windows:1:0000000000000000"} },
		"duplicate": func(v map[string]any) {
			v["sessions"] = []string{"windows:1:0000000000000001", "windows:1:0000000000000001"}
		},
		"two-incarnations": func(v map[string]any) {
			v["sessions"] = []string{"windows:1:0000000000000001", "windows:1:0000000000000002"}
		},
		"too-many": func(v map[string]any) { v["sessions"] = make([]string, 1025) },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			value := windowsObservationFixture()
			change(value)
			raw, _ := json.Marshal(value)
			state, err := parseWindowsAccountObservation(string(raw), "vca0123456789ab", "S-1-5-21-1-2-3-1001")
			if !errors.Is(err, ErrUnavailable) || state.LoginStopped() {
				t.Fatal("invalid observation acknowledged", state, err)
			}
		})
	}
	for _, raw := range []string{"", "private-sentinel", "{} {}", strings.Repeat(" ", 65537)} {
		_, err := parseWindowsAccountObservation(raw, "vca0123456789ab", "S-1-5-21-1-2-3-1001")
		if err == nil || strings.Contains(err.Error(), "private-sentinel") {
			t.Fatal("invalid raw receipt accepted or exposed", err)
		}
	}
}

func TestWindowsExecutorAccountObservationNeverMutatesOrTreatsFailureAsAbsence(t *testing.T) {
	for _, test := range []struct {
		name, fail string
		old, lost  bool
	}{
		{name: "valid"}, {name: "lost-read", lost: true}, {name: "old", old: true}, {name: "denied", fail: "account-inspect"},
	} {
		t.Run(test.name, func(t *testing.T) {
			guest := newWindowsExecutorGuest()
			guest.fail = test.fail
			if !test.old {
				raw, _ := json.Marshal(windowsObservationFixture())
				guest.account = string(raw)
			}
			if test.lost {
				guest.lostRead = "account-inspect"
			}
			state, err := NewWindowsSessionExecutor(guest).ObserveWindowsAgentAccount(t.Context(), pve.VM{}, guest.target.Username, guest.target.SID)
			if test.old || test.fail != "" {
				if err == nil || state.LoginStopped() {
					t.Fatal("unproven logoff acknowledged", state, err)
				}
			} else if err != nil || !state.LoginStopped() {
				t.Fatal("explicit state lost", state, err)
			}
			for _, command := range guest.commands {
				if len(command) < 2 || !slices.Contains([]string{"computer-v2-capabilities", "computer-v2-account-inspect"}, command[1]) {
					t.Fatal("observation mutated Guest", command)
				}
			}
			if len(guest.input) != 0 || len(guest.writtenPaths) != 0 {
				t.Fatal("observation wrote Guest data")
			}
		})
	}
}

func TestWindowsAccountObservationRequiresAnAlreadyBoundAgentSID(t *testing.T) {
	for _, invalid := range [][2]string{{"vdi", "S-1-5-21-1-2-3-1001"}, {"vcw0123456789ab", "S-1-5-21-1-2-3-1001"}, {"vca0123456789ab", ""}, {"vca0123456789ab", "S-1-5-18"}} {
		guest := newWindowsExecutorGuest()
		state, err := NewWindowsSessionExecutor(guest).ObserveWindowsAgentAccount(t.Context(), pve.VM{}, invalid[0], invalid[1])
		if !errors.Is(err, ErrInvalid) || state.LoginStopped() || len(guest.commands) != 0 {
			t.Fatal("unbound account observation reached Guest", state, err)
		}
	}
}
