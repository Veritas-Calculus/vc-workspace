package computer

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/auth"
	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
)

// Only the controller's persisted account/lease/login intent is accepted.
// HTTP/MCP callers cannot choose an OS username, identity, or login version.
type AgentSessionLifecycle interface {
	PrepareAgentAccount(context.Context, pve.VM, string) (AccountIdentity, error)
	InspectAgentAccount(context.Context, pve.VM, string) (LinuxAccountObservation, error)
	StartAgentSession(context.Context, pve.VM, AccountLease) (SessionTarget, error)
	StopAgentSession(context.Context, pve.VM, AccountLease) error
}

func validAgentUser(username string) bool {
	return managedSessionUserRE.MatchString(username) && strings.HasPrefix(username, "vca")
}

func (e *PVEExecutor) PrepareAgentAccount(ctx context.Context, machine pve.VM, username string) (AccountIdentity, error) {
	if !validAgentUser(username) {
		return AccountIdentity{}, ErrInvalid
	}
	if err := e.requireAccountFence(ctx, machine); err != nil {
		return AccountIdentity{}, err
	}
	if _, err := e.sessionCommand(ctx, machine, "account-provision", "--guest-user", username); err != nil {
		return AccountIdentity{}, err
	}
	value, err := e.InspectAgentAccount(ctx, machine, username)
	if err != nil {
		return AccountIdentity{}, err
	}
	if value.Identity == nil || !value.Exists {
		return AccountIdentity{}, ErrUnavailable
	}
	return *value.Identity, nil
}

func (e *PVEExecutor) StartAgentSession(ctx context.Context, machine pve.VM, lease AccountLease) (SessionTarget, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	random, err := auth.OpaqueToken(24)
	if err != nil {
		return SessionTarget{}, err
	}
	password := []byte("Vcw1!" + random)
	defer clear(password)
	if _, err := e.changeLinuxAccount(ctx, machine, lease, true, password); err != nil {
		return SessionTarget{}, err
	}
	// The single Guest mutation performed the real sesman login and retired
	// its credential while holding the account gate. No password replays here.
	deadline := time.Now().Add(20 * time.Second)
	for {
		if target, err := e.DiscoverSession(ctx, machine, lease.Identity.Username); err == nil {
			if target.UID != lease.Identity.UID || target.SID != lease.Identity.SID {
				return SessionTarget{}, ErrUnavailable
			}
			return target, nil
		}
		if !time.Now().Before(deadline) {
			return SessionTarget{}, fmt.Errorf("%w: interactive Helper did not become ready", ErrUnavailable)
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return SessionTarget{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func (e *PVEExecutor) StopAgentSession(ctx context.Context, machine pve.VM, lease AccountLease) error {
	lease.ExpiresUnixSeconds = 0
	if _, err := e.changeLinuxAccount(ctx, machine, lease, false, nil); err != nil {
		return err
	}
	value, err := e.InspectAgentAccount(ctx, machine, lease.Identity.Username)
	if err != nil {
		return err
	}
	if !value.Matches(lease, "revoked") || !value.LoginStopped() {
		return fmt.Errorf("%w: account revocation did not converge", ErrUnavailable)
	}
	return nil
}

var _ AgentSessionLifecycle = (*PVEExecutor)(nil)
