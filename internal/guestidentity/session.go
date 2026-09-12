// Package guestidentity validates native OS identities shared by persistence
// and the session protocol. It does not discover or adopt Guest accounts.
package guestidentity

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var managedUser = regexp.MustCompile(`^(vca|vcw)[a-f0-9]{12}$`)
var instance = regexp.MustCompile(`^[a-f0-9]{64}$`)
var linuxDisplay = regexp.MustCompile(`^:[0-9]+(\.[0-9]+)?$`)

// ValidAccount excludes system/built-in accounts and mixed UID/SID identities.
func ValidAccount(platform string, uid uint32, sid string) bool {
	switch platform {
	case "linux":
		return uid >= 1000 && sid == ""
	case "windows":
		return uid == 0 && ValidWindowsAccountSID(sid)
	default:
		return false
	}
}

func ValidSession(platform, username string, uid uint32, sid, sessionID, instanceID string) bool {
	if !managedUser.MatchString(username) || !instance.MatchString(instanceID) ||
		!ValidAccount(platform, uid, sid) || len(sessionID) > 128 {
		return false
	}
	if platform == "windows" {
		return ValidWindowsLogonBinding(sessionID)
	}
	rest, ok := strings.CutPrefix(sessionID, fmt.Sprintf("linux:%d:", uid))
	pid, display, found := strings.Cut(rest, ":")
	id, err := strconv.ParseUint(pid, 10, 32)
	return ok && found && err == nil && id > 0 && strconv.FormatUint(id, 10) == pid &&
		len(display) <= 16 && linuxDisplay.MatchString(display)
}

// A kernel logon binding is independent of Helper readiness/instance. It can
// describe a locked or disconnected desktop, including one whose SAM user was
// deleted. Callers must additionally verify the full account SID.
func ValidWindowsLogonBinding(sessionID string) bool {
	if len(sessionID) > 128 {
		return false
	}
	rest, ok := strings.CutPrefix(sessionID, "windows:")
	parts := strings.Split(rest, ":")
	if !ok || len(parts) != 2 || len(parts[1]) != 16 || parts[1] == "0000000000000000" {
		return false
	}
	id, err := strconv.ParseUint(parts[0], 10, 32)
	if err != nil || id == 0 || strconv.FormatUint(id, 10) != parts[0] {
		return false
	}
	luid, err := strconv.ParseUint(parts[1], 16, 64)
	return err == nil && fmt.Sprintf("%016x", luid) == parts[1]
}

// The full SID is the identity, not its final RID or an account display name.
func ValidWindowsAccountSID(sid string) bool {
	if len(sid) > 56 {
		return false
	}
	parts := strings.Split(sid, "-")
	if len(parts) != 8 || strings.Join(parts[:4], "-") != "S-1-5-21" {
		return false
	}
	for index, part := range parts[4:] {
		value, err := strconv.ParseUint(part, 10, 32)
		if err != nil || strconv.FormatUint(value, 10) != part || (index == 3 && value < 1000) {
			return false
		}
	}
	return true
}
