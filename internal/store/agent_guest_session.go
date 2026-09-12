package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/guestidentity"
	"github.com/jackc/pgx/v5"
)

type AgentGuestSession struct {
	DesktopVMID     int
	AgentID         string
	GuestUsername   string
	OSFamily        string
	LeaseID         string
	ControlEpoch    int64
	Generation      int64
	LoginGeneration int64
	State           string
	GuestUID        uint32
	GuestSID        string
	SessionID       string
	InstanceID      string
	ExpiresAt       time.Time
}

// Agent subjects are deliberately separate from the human vcw namespace.
func AgentGuestUsername(agentID string) string {
	digest := sha256.Sum256([]byte(agentID))
	return fmt.Sprintf("vca%x", digest[:6])
}

const agentGuestColumns = `desktop_vmid,agent_id,guest_username,os_family,lease_id,control_epoch,generation,login_generation,state,guest_uid,guest_sid,session_id,instance_id,expires_at`

func scanAgentGuest(row pgx.Row) (AgentGuestSession, error) {
	var item AgentGuestSession
	err := row.Scan(&item.DesktopVMID, &item.AgentID, &item.GuestUsername, &item.OSFamily, &item.LeaseID, &item.ControlEpoch, &item.Generation, &item.LoginGeneration, &item.State, &item.GuestUID, &item.GuestSID, &item.SessionID, &item.InstanceID, &item.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return item, ErrNotFound
	}
	return item, err
}

// Caller holds the VM control gate; this short transaction also synchronizes
// with assignment/disable changes. No network work is performed inside it.
func (s *Store) BeginAgentGuestSession(ctx context.Context, lease DesktopLease) (AgentGuestSession, error) {
	tx, err := s.beginAccessChange(ctx)
	if err != nil {
		return AgentGuestSession{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var vmid int
	var platform string
	err = tx.QueryRow(ctx, `SELECT d.vmid,d.os_family FROM desktop_leases l
	JOIN agent_principals p ON p.id=l.agent_id
	JOIN managed_desktops d ON d.vmid::text=l.desktop_id
	JOIN agent_desktop_assignments a ON a.agent_id=p.id AND a.desktop_vmid=d.vmid
	WHERE l.id=$1 AND l.agent_id=$2 AND l.control_epoch=$3 AND l.state='active' AND l.expires_at>now()
	AND p.enabled AND d.enabled AND d.present AND NOT EXISTS (SELECT 1 FROM pve_jobs clone_job WHERE clone_job.target_vmid=d.vmid AND clone_job.operation='pve.template_clone' AND clone_job.state<>'succeeded') AND d.os_family IN ('linux','windows')
	AND NOT EXISTS(SELECT 1 FROM desktop_connection_sessions c WHERE c.desktop_vmid=d.vmid AND c.state IN ('active','revoking'))
	AND NOT EXISTS(SELECT 1 FROM desktop_computer_revocations r WHERE r.desktop_vmid=d.vmid AND r.requested_revision>r.completed_revision)
	AND NOT EXISTS(SELECT 1 FROM agent_guest_sessions g WHERE g.desktop_vmid=d.vmid AND
	 (g.state='revoking' OR (g.state IN ('ready','provisioning') AND g.lease_id<>l.id))) FOR UPDATE OF l`, lease.ID, lease.AgentID, lease.ControlEpoch).Scan(&vmid, &platform)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentGuestSession{}, ErrConflict
	}
	if err != nil {
		return AgentGuestSession{}, err
	}
	item, err := scanAgentGuest(tx.QueryRow(ctx, `SELECT `+agentGuestColumns+` FROM agent_guest_sessions WHERE desktop_vmid=$1 AND agent_id=$2`, vmid, lease.AgentID))
	if err == nil && item.OSFamily != platform {
		return AgentGuestSession{}, ErrConflict
	}
	if err == nil && item.LeaseID == lease.ID && item.ControlEpoch == lease.ControlEpoch && (item.State == "ready" || item.State == "provisioning") {
		return item, tx.Commit(ctx)
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return AgentGuestSession{}, err
	}
	item, err = scanAgentGuest(tx.QueryRow(ctx, `INSERT INTO agent_guest_sessions(desktop_vmid,agent_id,guest_username,os_family,lease_id,control_epoch,expires_at)
	SELECT $1,$2,$3,$5,id,control_epoch,expires_at FROM desktop_leases WHERE id=$4
	ON CONFLICT(desktop_vmid,agent_id) DO UPDATE SET lease_id=excluded.lease_id,control_epoch=excluded.control_epoch,
	generation=agent_guest_sessions.generation+1,login_generation=0,state='provisioning',session_id='',instance_id='',expires_at=excluded.expires_at,updated_at=now()
	WHERE agent_guest_sessions.state='disabled' AND agent_guest_sessions.os_family=excluded.os_family
	RETURNING `+agentGuestColumns, vmid, lease.AgentID, AgentGuestUsername(lease.AgentID), lease.ID, platform))
	if err != nil {
		return AgentGuestSession{}, err
	}
	return item, tx.Commit(ctx)
}

func (s *Store) MarkAgentGuestSessionReady(ctx context.Context, item AgentGuestSession) error {
	if item.LoginGeneration < 1 || item.GuestUsername != AgentGuestUsername(item.AgentID) ||
		!guestidentity.ValidSession(item.OSFamily, item.GuestUsername, item.GuestUID, item.GuestSID, item.SessionID, item.InstanceID) {
		return ErrConflict
	}
	tx, err := s.beginAccessChange(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `UPDATE agent_guest_sessions g SET state='ready',guest_uid=$5,session_id=$6,instance_id=$7,guest_sid=$8,updated_at=now()
	WHERE g.desktop_vmid=$1 AND g.agent_id=$2 AND g.lease_id=$3 AND g.generation=$4 AND g.state IN ('provisioning','ready')
	AND g.os_family=$9 AND g.control_epoch=$10 AND g.guest_username=$11
	AND g.guest_uid=$5 AND g.guest_sid=$8 AND g.login_generation=$12
	AND EXISTS(SELECT 1 FROM desktop_leases l JOIN agent_principals p ON p.id=l.agent_id
	JOIN agent_desktop_assignments a ON a.agent_id=p.id AND a.desktop_vmid=g.desktop_vmid
	JOIN managed_desktops d ON d.vmid=a.desktop_vmid
	WHERE l.id=g.lease_id AND l.control_epoch=g.control_epoch AND l.state='active' AND l.expires_at>now() AND p.enabled AND d.enabled AND d.present AND NOT EXISTS (SELECT 1 FROM pve_jobs clone_job WHERE clone_job.target_vmid=d.vmid AND clone_job.operation='pve.template_clone' AND clone_job.state<>'succeeded') AND d.os_family=g.os_family)`,
		item.DesktopVMID, item.AgentID, item.LeaseID, item.Generation, item.GuestUID, item.SessionID, item.InstanceID, item.GuestSID, item.OSFamily, item.ControlEpoch, item.GuestUsername, item.LoginGeneration)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return tx.Commit(ctx)
}

func (s *Store) AgentGuestSessions(ctx context.Context, vmid int) ([]AgentGuestSession, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+agentGuestColumns+` FROM agent_guest_sessions WHERE ($1=0 OR desktop_vmid=$1) ORDER BY desktop_vmid,agent_id`, vmid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []AgentGuestSession
	for rows.Next() {
		item, err := scanAgentGuest(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// A failed or canceled bootstrap may already have created an OS login. Queue
// its cleanup durably without logging any credential or Guest output.
func (s *Store) QueueAgentGuestSessionRevocation(ctx context.Context, item AgentGuestSession) error {
	// A failed bootstrap may have published authority before losing its result.
	// Close that exact lease and allocate a new VM epoch in the same transaction
	// as both cleanup queues. Reusing its old epoch after a tombstone is forbidden.
	_, err := s.pool.Exec(ctx, `UPDATE desktop_leases l SET state='revoked',updated_at=now()
	WHERE l.id=$3 AND l.state='active' AND EXISTS (
	 SELECT 1 FROM agent_guest_sessions g WHERE g.desktop_vmid=$1 AND g.agent_id=$2
	 AND g.lease_id=l.id AND g.generation=$4 AND g.login_generation=$5 AND g.control_epoch=$6 AND g.state IN ('ready','provisioning'))`, item.DesktopVMID, item.AgentID, item.LeaseID, item.Generation, item.LoginGeneration, item.ControlEpoch)
	return err
}

func (s *Store) CompleteAgentGuestSessionRevocation(ctx context.Context, item AgentGuestSession) error {
	tag, err := s.pool.Exec(ctx, `WITH changed AS (
	UPDATE agent_guest_sessions SET state='disabled',session_id='',instance_id='',updated_at=now()
	WHERE desktop_vmid=$1 AND agent_id=$2 AND lease_id=$3 AND generation=$4 AND login_generation=$5 AND control_epoch=$6
	AND guest_uid=$7 AND guest_sid=$8 AND state='revoking' RETURNING desktop_vmid,agent_id,lease_id,generation,login_generation
	) INSERT INTO audit_events(event_type,outcome,target_type,target_id,detail)
	SELECT 'agent.guest_session_revoked','success','virtual_machine',desktop_vmid::text,
	jsonb_build_object('agent_id',agent_id,'lease_id',lease_id,'generation',generation,'login_generation',login_generation) FROM changed`, item.DesktopVMID, item.AgentID, item.LeaseID, item.Generation, item.LoginGeneration, item.ControlEpoch, item.GuestUID, item.GuestSID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Store) QueueExpiredComputerLeases(ctx context.Context, vmid int) error {
	_, err := s.pool.Exec(ctx, `UPDATE desktop_leases SET state='expired',control_epoch=control_epoch+1,updated_at=now()
	WHERE state='active' AND expires_at<=now() AND ($1=0 OR desktop_id=($1::bigint)::text)`, vmid)
	return err
}
