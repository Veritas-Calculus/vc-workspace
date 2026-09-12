package httpapi

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
)

func (s *Server) enableGuestAccount(ctx context.Context, node string, vmid int, username string) error {
	desktop, err := s.store.ManagedDesktopByVMID(ctx, vmid)
	if err != nil {
		return err
	}
	command, err := enableGuestAccountCommand(desktop.OSFamily, username)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	result, err := s.pve.ExecGuest(ctx, node, vmid, command)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return errors.New("Guest account activation did not complete")
	}
	return nil
}

// Called only after a fresh random password has been installed.
func enableGuestAccountCommand(osFamily, username string) ([]string, error) {
	if !guestUsernamePattern.MatchString(username) || len(username) < 4 || username[:3] != "vcw" {
		return nil, errors.New("managed guest username is invalid")
	}
	switch osFamily {
	case "linux":
		return []string{"/bin/sh", "-c", fmt.Sprintf("set -eu\nusermod --unlock --expiredate '' '%s'", username)}, nil
	case "windows":
		return []string{`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", fmt.Sprintf("$ErrorActionPreference='Stop'; Enable-LocalUser -Name '%s'", username)}, nil
	default:
		return nil, errors.New("guest account activation is unsupported for this operating system")
	}
}

func (s *Server) revokeGuestAccount(ctx context.Context, desktop store.ManagedDesktop, username string) error {
	command, err := terminateGuestSessionCommand(desktop.OSFamily, username)
	if err != nil {
		return err
	}
	execContext, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	result, err := s.pve.ExecGuest(execContext, desktop.Node, desktop.VMID, command)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return errors.New("Guest account revocation did not complete")
	}
	return nil
}

// The caller holds the desktop lock. Read the queue after acquiring that lock,
// never execute a snapshot collected by another worker before it was acquired.
func (s *Server) revokeGuestIdentitiesForDesktop(ctx context.Context, vmid int) error {
	if err := s.store.QueueExpiredGuestIdentitiesForDesktop(ctx, vmid); err != nil {
		return err
	}
	items, err := s.store.PendingGuestIdentityRevocations(ctx, vmid)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	desktop, err := s.store.ManagedDesktopByVMID(ctx, vmid)
	if err != nil {
		return err
	}
	for _, item := range items {
		account, err := s.store.NativeGuestAccount(ctx, vmid, item.UserID)
		if err == nil {
			if account.GuestUsername != item.GuestUsername {
				return store.ErrConflict
			}
			err = s.revokeNativeGuestAccount(ctx, pve.VM{VMID: vmid, Node: desktop.Node}, account)
		} else if errors.Is(err, store.ErrNotFound) {
			// Pre-upgrade identities retain the explicit legacy drain path;
			// pinned Native identities can never fall through to it.
			err = s.revokeGuestAccount(ctx, desktop, item.GuestUsername)
		}
		if err != nil {
			_ = s.store.RecordGuestIdentityRevocationFailure(ctx, item)
			return err
		}
		if err := s.store.CompleteGuestIdentityRevocation(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) revokeDueGuestIdentities(ctx context.Context) {
	if err := s.store.QueueExpiredGuestIdentities(ctx); err != nil {
		if ctx.Err() == nil {
			s.logger.Warn("queue expired Guest identities", "error", err)
		}
		return
	}
	items, err := s.store.PendingGuestIdentityRevocations(ctx, 0)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Warn("read Guest revocation queue", "error", err)
		}
		return
	}
	seen := make(map[int]bool)
	for _, item := range items {
		if seen[item.DesktopVMID] {
			continue
		}
		seen[item.DesktopVMID] = true
		err := s.store.WithDesktopConnectionLock(ctx, item.DesktopVMID, func() error { return s.revokeGuestIdentitiesForDesktop(ctx, item.DesktopVMID) })
		if err != nil && ctx.Err() == nil {
			s.logger.Warn("revoke Guest identity", "vmid", item.DesktopVMID, "error", err)
		}
	}
}

// Managed local identities only; never accept an arbitrary shell argument or
// terminate a system/directory account based on a user-supplied display name.
func terminateGuestSessionCommand(osFamily, username string) ([]string, error) {
	if !guestUsernamePattern.MatchString(username) || len(username) < 4 || username[:3] != "vcw" {
		return nil, errors.New("managed guest username is invalid")
	}
	switch osFamily {
	case "linux":
		return []string{"/bin/sh", "-c", fmt.Sprintf(`set -eu
username='%s'
if ! id "$username" >/dev/null 2>&1; then exit 0; fi
uid=$(id -u "$username")
[ "$uid" -ge 1000 ] || exit 1
# Expire the local account as well as its password. This denies PAM logins
# using other credentials, including SSH keys, until a new authorized issue.
usermod --lock --expiredate 1 "$username"
# PAM/systemd sessions are removed before checking for orphaned processes.
loginctl terminate-user "$username" >/dev/null 2>&1 || true
if pgrep -u "$uid" >/dev/null; then
  pkill -KILL -u "$uid" || [ "$?" -eq 1 ]
fi
if pgrep -u "$uid" >/dev/null; then exit 1; fi
`, username)}, nil
	case "windows":
		// WTS enumerates actual session IDs; parsing localized quser columns is
		// unsafe. Match both user and local machine domain before logging off.
		script := fmt.Sprintf(`$ErrorActionPreference='Stop'
$user=Get-LocalUser -Name '%s' -ErrorAction SilentlyContinue
if (-not $user) { exit 0 }
Disable-LocalUser -InputObject $user
`, username) + `
Add-Type -TypeDefinition @'
using System;
using System.ComponentModel;
using System.Runtime.InteropServices;
public static class VCWorkspaceSessionRevocation {
  [StructLayout(LayoutKind.Sequential)] struct Session { public int Id; public IntPtr Station; public int State; }
  [DllImport("wtsapi32.dll", EntryPoint="WTSEnumerateSessionsW", SetLastError=true)] static extern bool Enumerate(IntPtr server, int reserved, int version, out IntPtr sessions, out int count);
  [DllImport("wtsapi32.dll", EntryPoint="WTSQuerySessionInformationW", SetLastError=true)] static extern bool Query(IntPtr server, int id, int info, out IntPtr value, out int bytes);
  [DllImport("wtsapi32.dll", SetLastError=true)] static extern bool WTSLogoffSession(IntPtr server, int id, bool wait);
  [DllImport("wtsapi32.dll")] static extern void WTSFreeMemory(IntPtr value);
  static string Read(int id, int info) {
    IntPtr value; int bytes;
    if (!Query(IntPtr.Zero,id,info,out value,out bytes)) throw new Win32Exception(Marshal.GetLastWin32Error());
    try { return Marshal.PtrToStringUni(value) ?? ""; } finally { WTSFreeMemory(value); }
  }
  public static void Terminate(string username) {
    IntPtr sessions; int count;
    if (!Enumerate(IntPtr.Zero,0,1,out sessions,out count)) throw new Win32Exception(Marshal.GetLastWin32Error());
    try {
      int size=Marshal.SizeOf(typeof(Session));
      for (int i=0;i<count;i++) {
        var session=(Session)Marshal.PtrToStructure(IntPtr.Add(sessions,i*size),typeof(Session));
        if (session.Id==0) continue;
        if (!String.Equals(Read(session.Id,5),username,StringComparison.OrdinalIgnoreCase)) continue;
        if (!String.Equals(Read(session.Id,7),Environment.MachineName,StringComparison.OrdinalIgnoreCase)) continue;
        if (!WTSLogoffSession(IntPtr.Zero,session.Id,true)) throw new Win32Exception(Marshal.GetLastWin32Error());
      }
    } finally { WTSFreeMemory(sessions); }
  }
}
'@
`
		script += fmt.Sprintf("[VCWorkspaceSessionRevocation]::Terminate('%s')", username)
		return []string{`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script}, nil
	default:
		return nil, errors.New("guest session termination is unsupported for this operating system")
	}
}
