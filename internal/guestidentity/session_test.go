package guestidentity

import (
	"strings"
	"testing"
)

func TestNativeSessionIdentityValidation(t *testing.T) {
	instanceID := strings.Repeat("a", 64)
	for _, good := range []struct {
		platform, sid, session string
		uid                    uint32
	}{
		{"linux", "", "linux:1001:123::10.0", 1001},
		{"windows", "S-1-5-21-1-2-3-1001", "windows:2:000000000001a2b3", 0},
		{"windows", "S-1-5-21-4294967295-0-1-4294967295", "windows:4294967295:ffffffffffffffff", 0},
	} {
		if !ValidSession(good.platform, "vca0123456789ab", good.uid, good.sid, good.session, instanceID) {
			t.Fatalf("rejected canonical identity %+v", good)
		}
	}
	for _, bad := range []struct {
		name, platform, user, sid, session, instance string
		uid                                          uint32
	}{
		{"mixed-linux", "linux", "vca0123456789ab", "S-1-5-21-1-2-3-1001", "linux:1001:123::10", instanceID, 1001},
		{"mixed-windows", "windows", "vca0123456789ab", "S-1-5-21-1-2-3-1001", "windows:2:000000000001a2b3", instanceID, 1001},
		{"system", "windows", "vca0123456789ab", "S-1-5-18", "windows:2:000000000001a2b3", instanceID, 0},
		{"root", "linux", "vca0123456789ab", "", "linux:0:123::10", instanceID, 0},
		{"shared", "linux", "vdi", "", "linux:1001:123::10", instanceID, 1001},
		{"platform", "unknown", "vca0123456789ab", "", "linux:1001:123::10", instanceID, 1001},
		{"pid-alias", "linux", "vca0123456789ab", "", "linux:1001:0123::10", instanceID, 1001},
		{"session-zero", "windows", "vca0123456789ab", "S-1-5-21-1-2-3-1001", "windows:0:000000000001a2b3", instanceID, 0},
		{"session-alias", "windows", "vca0123456789ab", "S-1-5-21-1-2-3-1001", "windows:02:000000000001a2b3", instanceID, 0},
		{"luid-zero", "windows", "vca0123456789ab", "S-1-5-21-1-2-3-1001", "windows:2:0000000000000000", instanceID, 0},
		{"luid-plus", "windows", "vca0123456789ab", "S-1-5-21-1-2-3-1001", "windows:2:+000000000000001", instanceID, 0},
		{"luid-upper", "windows", "vca0123456789ab", "S-1-5-21-1-2-3-1001", "windows:2:000000000001A2B3", instanceID, 0},
		{"instance", "linux", "vca0123456789ab", "", "linux:1001:123::10", "short", 1001},
	} {
		t.Run(bad.name, func(t *testing.T) {
			if ValidSession(bad.platform, bad.user, bad.uid, bad.sid, bad.session, bad.instance) {
				t.Fatal("accepted invalid session binding")
			}
		})
	}
}

func TestSIDRejectsAliasesAndPrivilegedAccounts(t *testing.T) {
	for _, sid := range []string{
		"", "S-1-5-18", "S-1-5-32-544", "S-1-5-21-1-2-3-500", "S-1-5-21-1-2-3-999",
		"S-1-5-21-01-2-3-1001", "S-1-5-21-1-2-3-+1001", "S-1-5-21-1-2-3-4294967296",
		"S-1-5-21-4294967296-2-3-1001", "S-1-5-21-1-2-3-1001\n", "S-1-5-21-1-2-3-1001)(A;;GA;;;WD)",
	} {
		if ValidWindowsAccountSID(sid) {
			t.Fatalf("accepted SID %q", sid)
		}
	}
}

func TestWindowsLogonBindingDoesNotInventAHelperInstance(t *testing.T) {
	for _, good := range []string{"windows:1:0000000000000001", "windows:4294967295:ffffffffffffffff"} {
		if !ValidWindowsLogonBinding(good) {
			t.Fatal("valid kernel logon rejected", good)
		}
	}
	for _, bad := range []string{"", "windows:0:0000000000000001", "windows:01:0000000000000001", "windows:4294967296:0000000000000001", "windows:1:0000000000000000", "windows:1:000000000000000A", "windows:1:+000000000000001", "windows:1:1", "windows:1:0000000000000001:extra", "linux:1000:123::10", strings.Repeat("a", 129)} {
		if ValidWindowsLogonBinding(bad) {
			t.Fatal("invalid kernel logon accepted", bad)
		}
	}
}
