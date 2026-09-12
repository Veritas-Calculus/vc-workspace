package store

import (
	"context"
	"math"

	"github.com/Veritas-Calculus/vc-workspace/internal/guestidentity"
)

// Identity must be persisted before any login credential can be installed.
// A Helper observation can refresh an instance, but cannot first bind a UID/SID.
func (s *Store) BindAgentGuestAccount(ctx context.Context, item AgentGuestSession) error {
	return s.bindAgentGuestAccount(ctx, item, false)
}

// A lost provisioning receipt may leave a Guest-owned account but no DB UID.
// Cleanup may pin its independently observed identity for this exact closed
// generation; this never grants a login or changes an already pinned identity.
func (s *Store) BindRevokingAgentGuestAccount(ctx context.Context, item AgentGuestSession) error {
	return s.bindAgentGuestAccount(ctx, item, true)
}

func (s *Store) bindAgentGuestAccount(ctx context.Context, item AgentGuestSession, revoking bool) error {
	if item.GuestUsername != AgentGuestUsername(item.AgentID) || !guestidentity.ValidAccount(item.OSFamily, item.GuestUID, item.GuestSID) {
		return ErrConflict
	}
	tx, err := s.beginAccessChange(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `UPDATE agent_guest_sessions g SET guest_uid=$5,guest_sid=$6,updated_at=now()
      WHERE g.desktop_vmid=$1 AND g.agent_id=$2 AND g.lease_id=$3 AND g.generation=$4 AND g.login_generation=$7
      AND g.os_family=$8 AND g.control_epoch=$9 AND g.guest_username=$10
      AND ((g.guest_uid=0 AND g.guest_sid='') OR (g.guest_uid=$5 AND g.guest_sid=$6))
      AND (($11 AND g.state='revoking') OR (NOT $11 AND g.state IN ('provisioning','ready') AND EXISTS(
        SELECT 1 FROM desktop_leases l JOIN agent_principals p ON p.id=l.agent_id
        JOIN agent_desktop_assignments a ON a.agent_id=p.id AND a.desktop_vmid=g.desktop_vmid
        JOIN managed_desktops d ON d.vmid=a.desktop_vmid
        WHERE l.id=g.lease_id AND l.control_epoch=g.control_epoch AND l.state='active' AND l.expires_at>now()
        AND p.enabled AND d.enabled AND d.present AND NOT EXISTS (SELECT 1 FROM pve_jobs clone_job WHERE clone_job.target_vmid=d.vmid AND clone_job.operation='pve.template_clone' AND clone_job.state<>'succeeded') AND d.os_family=g.os_family)))`,
		item.DesktopVMID, item.AgentID, item.LeaseID, item.Generation, item.GuestUID, item.GuestSID, item.LoginGeneration, item.OSFamily, item.ControlEpoch, item.GuestUsername, revoking)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return tx.Commit(ctx)
}

// Compare-and-swap reserves exactly one new login version before dispatch.
// Callers must retain the returned value even on transport failure; regenerating
// an attempt after a lost receipt could replace a login which already happened.
func (s *Store) BeginAgentGuestLogin(ctx context.Context, item AgentGuestSession) (AgentGuestSession, error) {
	if item.LoginGeneration < 0 || item.LoginGeneration == math.MaxInt64 || item.GuestUsername != AgentGuestUsername(item.AgentID) ||
		!guestidentity.ValidAccount(item.OSFamily, item.GuestUID, item.GuestSID) {
		return AgentGuestSession{}, ErrConflict
	}
	tx, err := s.beginAccessChange(ctx)
	if err != nil {
		return AgentGuestSession{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	next, err := scanAgentGuest(tx.QueryRow(ctx, `UPDATE agent_guest_sessions g SET login_generation=login_generation+1,
      state='provisioning',session_id='',instance_id='',updated_at=now()
      WHERE g.desktop_vmid=$1 AND g.agent_id=$2 AND g.lease_id=$3 AND g.generation=$4 AND g.login_generation=$5
      AND g.guest_uid=$6 AND g.guest_sid=$7 AND g.os_family=$8 AND g.control_epoch=$9 AND g.guest_username=$10
      AND g.state IN ('provisioning','ready') AND EXISTS(
        SELECT 1 FROM desktop_leases l JOIN agent_principals p ON p.id=l.agent_id
        JOIN agent_desktop_assignments a ON a.agent_id=p.id AND a.desktop_vmid=g.desktop_vmid
        JOIN managed_desktops d ON d.vmid=a.desktop_vmid
        WHERE l.id=g.lease_id AND l.control_epoch=g.control_epoch AND l.state='active' AND l.expires_at>now()
        AND p.enabled AND d.enabled AND d.present AND NOT EXISTS (SELECT 1 FROM pve_jobs clone_job WHERE clone_job.target_vmid=d.vmid AND clone_job.operation='pve.template_clone' AND clone_job.state<>'succeeded') AND d.os_family=g.os_family)
      RETURNING `+agentGuestColumns, item.DesktopVMID, item.AgentID, item.LeaseID, item.Generation, item.LoginGeneration, item.GuestUID, item.GuestSID, item.OSFamily, item.ControlEpoch, item.GuestUsername))
	if err == ErrNotFound {
		return AgentGuestSession{}, ErrConflict
	}
	if err != nil {
		return AgentGuestSession{}, err
	}
	return next, tx.Commit(ctx)
}
