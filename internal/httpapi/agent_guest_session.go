package httpapi

import (
	"context"
	"fmt"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/computer"
	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
)

// Caller holds the distributed VM gate throughout bootstrap and the action.
func (s *Server) prepareAgentGuestSession(ctx context.Context, machine pve.VM, lease store.DesktopLease) (computer.SessionTarget, error) {
	executor, ok := s.computer.(computer.SessionExecutor)
	lifecycle, hasLifecycle := s.computer.(computer.AgentSessionLifecycle)
	_, hasLegacyFence := s.computer.(computer.AuthorityController)
	if !ok || !hasLifecycle || !hasLegacyFence {
		return computer.SessionTarget{}, computer.ErrUnavailable
	}
	desktop, err := s.store.ManagedDesktopByVMID(ctx, machine.VMID)
	if err != nil {
		return computer.SessionTarget{}, err
	}
	if desktop.OSFamily != "linux" {
		return computer.SessionTarget{}, fmt.Errorf("%w: per-Agent Windows sessions are not yet implemented", computer.ErrUnavailable)
	}
	profileID := desktop.IdentityProfileID
	if profileID == "" {
		profileID = "managed-local-linux"
	}
	profile, err := s.store.IdentityProfileByID(ctx, profileID)
	if err != nil || !profile.Enabled || profile.Mode != "managed_local" || profile.Platform != "linux" {
		return computer.SessionTarget{}, fmt.Errorf("%w: Agent sessions require a managed-local Linux profile", computer.ErrUnavailable)
	}
	binding, err := s.store.BeginAgentGuestSession(ctx, lease)
	if err != nil {
		return computer.SessionTarget{}, err
	}
	prepared := false
	defer func() {
		if prepared {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.store.QueueAgentGuestSessionRevocation(cleanup, binding); err != nil {
			s.logger.Warn("queue failed Agent bootstrap cleanup", "vmid", machine.VMID, "agent_id", lease.AgentID)
		}
	}()
	policy, err := s.store.EnsureDesktopAccessPolicy(ctx, machine.VMID)
	if err != nil {
		return computer.SessionTarget{}, err
	}
	policyCurrent := policy.State == "applied" && policy.AppliedRevision == policy.DesiredRevision
	// A ready session does not reconfigure xrdp or upload wallpaper on every
	// keystroke. A new identity still receives its privilege policy before login.
	if binding.State != "ready" {
		// Upgrading an existing Guest can leave an active shared vdi Helper
		// even with no cleanup row in a newly migrated database.
		if err := s.revokeAllComputerAuthority(ctx, machine); err != nil {
			return computer.SessionTarget{}, err
		}
		identity, err := lifecycle.PrepareAgentAccount(ctx, machine, binding.GuestUsername)
		if err != nil {
			return computer.SessionTarget{}, err
		}
		if identity.Username != binding.GuestUsername {
			return computer.SessionTarget{}, computer.ErrUnavailable
		}
		binding.GuestUID, binding.GuestSID = identity.UID, identity.SID
		if err := s.store.BindAgentGuestAccount(ctx, binding); err != nil {
			return computer.SessionTarget{}, err
		}
		if policyCurrent {
			command, err := desktopAccessPolicyCommandForUser("linux", policy.PrivilegeMode, binding.GuestUsername)
			if err != nil {
				return computer.SessionTarget{}, err
			}
			result, err := s.pve.ExecGuest(ctx, machine.Node, machine.VMID, command)
			if err != nil || result.ExitCode != 0 {
				return computer.SessionTarget{}, computer.ErrUnavailable
			}
		}
	}
	if !policyCurrent {
		if _, err := s.reconcileDesktopAccessPolicy(ctx, machine); err != nil {
			return computer.SessionTarget{}, err
		}
	}
	current, err := s.store.DesktopLeaseByID(ctx, lease.ID)
	if err != nil || current.State != "active" || current.ControlEpoch != lease.ControlEpoch {
		return computer.SessionTarget{}, store.ErrConflict
	}
	observed, err := lifecycle.InspectAgentAccount(ctx, machine, binding.GuestUsername)
	if err != nil {
		return computer.SessionTarget{}, err
	}
	intent := agentAccountLease(binding)
	if observed.Identity == nil || *observed.Identity != intent.Identity || !observed.Exists {
		return computer.SessionTarget{}, store.ErrConflict
	}
	var target computer.SessionTarget
	needLogin := binding.LoginGeneration == 0
	if needLogin {
		// Fresh/previously cleaned account only. Never adopt a live unversioned
		// session or infer a login version from a Helper which happens to exist.
		if !observed.Disabled || observed.ProcessesAbsent == nil || !*observed.ProcessesAbsent ||
			observed.LoginWritersAbsent == nil || !*observed.LoginWritersAbsent ||
			(observed.Lifecycle != nil && observed.Lifecycle.ControlEpoch >= binding.ControlEpoch) {
			return target, computer.ErrUnavailable
		}
	} else {
		if !observed.Matches(intent, "sealed") || observed.Disabled || observed.ExpiryDay == nil ||
			*observed.ExpiryDay != uint64((intent.ExpiresUnixSeconds+86399)/86400) {
			return target, computer.ErrUnavailable
		}
		target, err = executor.DiscoverSession(ctx, machine, binding.GuestUsername)
		if err != nil {
			// A transient Helper error is not proof the user's applications died.
			// Normal logind teardown may outlive the last UID process. Wait
			// read-only for that exact login to close before reserving a version.
			if err := awaitAgentLoginExit(ctx, intent, observed, func(wait context.Context) (computer.LinuxAccountObservation, error) {
				return lifecycle.InspectAgentAccount(wait, machine, binding.GuestUsername)
			}); err != nil {
				return target, err
			}
			needLogin = true
		}
	}
	if needLogin {
		next, err := s.store.BeginAgentGuestLogin(ctx, binding)
		if err != nil {
			return target, err
		}
		binding = next
		intent = agentAccountLease(binding)
		target, err = lifecycle.StartAgentSession(ctx, machine, intent)
		if err != nil {
			return target, err
		}
		observed, err = lifecycle.InspectAgentAccount(ctx, machine, binding.GuestUsername)
		if err != nil || !observed.Matches(intent, "sealed") || observed.Disabled {
			return target, computer.ErrUnavailable
		}
	}
	if target.Username != binding.GuestUsername || target.UID != binding.GuestUID || target.SID != binding.GuestSID {
		return computer.SessionTarget{}, store.ErrConflict
	}
	if target.Validate() != nil {
		return computer.SessionTarget{}, computer.ErrUnavailable
	}
	binding.GuestUID, binding.GuestSID, binding.SessionID, binding.InstanceID = target.UID, target.SID, target.SessionID, target.InstanceID
	if err := s.store.MarkAgentGuestSessionReady(ctx, binding); err != nil {
		return computer.SessionTarget{}, err
	}
	if err := executor.ActivateSessionAuthority(ctx, machine, target, computer.Authority{
		SchemaVersion: computer.SchemaVersion, LeaseID: lease.ID, ControlEpoch: lease.ControlEpoch, State: "active", ExpiresUnixMS: lease.ExpiresAt.UnixMilli(),
	}); err != nil {
		return computer.SessionTarget{}, err
	}
	prepared = true
	return target, nil
}

func awaitAgentLoginExit(ctx context.Context, intent computer.AccountLease, observed computer.LinuxAccountObservation,
	inspect func(context.Context) (computer.LinuxAccountObservation, error),
) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Never turn missing/changed identity, an expired lease, or a live UID
		// into permission to retry login. Every poll binds the same sealed
		// generation; the database CAS still revalidates before credentials.
		if !observed.Exists || !observed.Matches(intent, "sealed") || observed.Disabled ||
			observed.ExpiryDay == nil || *observed.ExpiryDay != uint64((intent.ExpiresUnixSeconds+86399)/86400) ||
			intent.ExpiresUnixSeconds <= time.Now().Unix() || observed.ProcessesAbsent == nil || !*observed.ProcessesAbsent ||
			observed.LoginWritersAbsent == nil {
			return computer.ErrUnavailable
		}
		if *observed.LoginWritersAbsent {
			return nil
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		var err error
		observed, err = inspect(ctx)
		if err != nil {
			return err
		}
	}
}

func agentAccountLease(binding store.AgentGuestSession) computer.AccountLease {
	return computer.AccountLease{SchemaVersion: 1, Identity: computer.AccountIdentity{Username: binding.GuestUsername, UID: binding.GuestUID, SID: binding.GuestSID},
		LeaseID: binding.LeaseID, ControlEpoch: binding.ControlEpoch, LoginGeneration: binding.LoginGeneration, ExpiresUnixSeconds: binding.ExpiresAt.Unix()}
}

func (s *Server) revokeAgentGuestSessions(ctx context.Context, machine pve.VM) error {
	bindings, err := s.store.AgentGuestSessions(ctx, machine.VMID)
	if err != nil {
		return err
	}
	if len(bindings) == 0 {
		return nil
	}
	executor, ok := s.computer.(computer.SessionExecutor)
	lifecycle, hasLifecycle := s.computer.(computer.AgentSessionLifecycle)
	if !ok || !hasLifecycle {
		return computer.ErrUnavailable
	}
	epoch, err := s.store.ComputerRevocationEpoch(ctx, machine.VMID)
	if err != nil {
		return err
	}
	if err := executor.RevokeSessionAuthority(ctx, machine, computer.Authority{SchemaVersion: computer.SchemaVersion, LeaseID: "lease_revoked_session_tombstone", ControlEpoch: epoch, State: "revoked"}); err != nil {
		return err
	}
	for _, binding := range bindings {
		if binding.State != "revoking" {
			continue
		}
		observed, err := lifecycle.InspectAgentAccount(ctx, machine, binding.GuestUsername)
		if err != nil {
			return err
		}
		if observed.Identity == nil {
			// No binding and no reserved login means this controller could never
			// have installed a credential. A late provision can only stay disabled.
			if binding.LoginGeneration != 0 || binding.GuestUID != 0 || binding.GuestSID != "" {
				return computer.ErrUnavailable
			}
			if err := s.store.CompleteAgentGuestSessionRevocation(ctx, binding); err != nil {
				return err
			}
			continue
		}
		binding.GuestUID, binding.GuestSID = observed.Identity.UID, observed.Identity.SID
		if err := s.store.BindRevokingAgentGuestAccount(ctx, binding); err != nil {
			return err
		}
		intent := agentAccountLease(binding)
		if intent.LoginGeneration == 0 {
			// Terminal barrier for a never-opened/pre-upgrade account, not a
			// fabricated login attempt. Retries may observe this exact tombstone.
			intent.LoginGeneration = 1
			tombstone := intent
			tombstone.ExpiresUnixSeconds = 0
			if observed.Lifecycle != nil && observed.Lifecycle.ControlEpoch >= intent.ControlEpoch && !observed.Matches(tombstone, "revoked") {
				return computer.ErrUnavailable
			}
		}
		if err := lifecycle.StopAgentSession(ctx, machine, intent); err != nil {
			return err
		}
		if err := s.store.CompleteAgentGuestSessionRevocation(ctx, binding); err != nil {
			return err
		}
	}
	return nil
}
