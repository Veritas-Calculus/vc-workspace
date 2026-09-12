package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Serialize short authorization transactions with connection issuance. Never
// hold this lock while calling PVE/Guest Agent; those use the per-desktop lock.
func (s *Store) beginAccessChange(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(72914405)`); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

func commitAccessChange(ctx context.Context, tx pgx.Tx) error {
	if err := queueGuestIdentityRevocations(ctx, tx, `b.state <> 'disabled' AND NOT EXISTS (
		SELECT 1 FROM effective_user_desktop_access a WHERE a.user_id=b.user_id AND a.desktop_vmid=b.desktop_vmid
	)`, "access_removed"); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE desktop_connection_sessions s
		SET state='revoking',termination_required=true,updated_at=now()
		WHERE s.state IN ('active','revoking') AND NOT EXISTS (
		  SELECT 1 FROM effective_user_desktop_access a WHERE a.user_id=s.user_id AND a.desktop_vmid=s.desktop_vmid
		)`)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE desktop_leases l SET state='revoked',control_epoch=control_epoch+1,updated_at=now()
		WHERE l.state='active' AND NOT EXISTS(SELECT 1 FROM agent_desktop_assignments a
		JOIN agent_principals p ON p.id=a.agent_id JOIN managed_desktops d ON d.vmid=a.desktop_vmid
		WHERE a.agent_id=l.agent_id AND d.vmid::text=l.desktop_id AND p.enabled AND d.enabled AND d.present AND NOT EXISTS (SELECT 1 FROM pve_jobs clone_job WHERE clone_job.target_vmid=d.vmid AND clone_job.operation='pve.template_clone' AND clone_job.state<>'succeeded'))`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) DesktopConnectionTerminationRequired(ctx context.Context, id string) (bool, error) {
	var required bool
	err := s.pool.QueryRow(ctx, `SELECT termination_required OR expires_at <= now() FROM desktop_connection_sessions WHERE id=$1`, id).Scan(&required)
	return required, err
}
