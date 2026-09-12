package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Current VM fence for legacy cleanup / first-time human takeover. Existing
// counters are never reset, including when no lease remains in the database.
func (s *Store) ComputerControlEpoch(ctx context.Context, vmid int) (int64, error) {
	var epoch int64
	err := s.pool.QueryRow(ctx, `INSERT INTO desktop_computer_epochs(desktop_vmid,control_epoch)
 SELECT vmid,1 FROM managed_desktops WHERE vmid=$1
 ON CONFLICT(desktop_vmid) DO UPDATE SET control_epoch=desktop_computer_epochs.control_epoch
 RETURNING control_epoch`, vmid).Scan(&epoch)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return epoch, err
}

// Caller holds the VM control gate. Use the pending/last closing epoch, not
// the current counter: it might already belong to a newly acquired lease.
func (s *Store) ComputerRevocationEpoch(ctx context.Context, vmid int) (int64, error) {
	var epoch int64
	err := s.pool.QueryRow(ctx, `SELECT control_epoch FROM desktop_computer_revocations WHERE desktop_vmid=$1`, vmid).Scan(&epoch)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return epoch, err
}

type ComputerRevocation struct {
	DesktopVMID int
	Revision    int64
}

func (s *Store) PendingComputerRevocations(ctx context.Context, vmid int) ([]ComputerRevocation, error) {
	rows, err := s.pool.Query(ctx, `SELECT desktop_vmid,requested_revision FROM desktop_computer_revocations
		WHERE requested_revision>completed_revision AND ($1=0 OR desktop_vmid=$1) ORDER BY updated_at,desktop_vmid LIMIT 100`, vmid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]ComputerRevocation, 0)
	for rows.Next() {
		var item ComputerRevocation
		if err := rows.Scan(&item.DesktopVMID, &item.Revision); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) CompleteComputerRevocation(ctx context.Context, item ComputerRevocation) error {
	tag, err := s.pool.Exec(ctx, `WITH completed AS (
		UPDATE desktop_computer_revocations SET completed_revision=$2,updated_at=now()
		WHERE desktop_vmid=$1 AND requested_revision=$2 AND completed_revision<$2 RETURNING desktop_vmid
	) INSERT INTO audit_events(event_type,outcome,target_type,target_id,detail)
	SELECT 'agent.computer_authority_revoked','success','virtual_machine',desktop_vmid::text,
	jsonb_build_object('vmid',desktop_vmid,'revision',$2::bigint) FROM completed`, item.DesktopVMID, item.Revision)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Store) DeferComputerRevocation(ctx context.Context, item ComputerRevocation) {
	// Failed/offline VMs move behind other queued desktops on the next scan.
	_, _ = s.pool.Exec(ctx, `UPDATE desktop_computer_revocations SET updated_at=now()
		WHERE desktop_vmid=$1 AND requested_revision=$2 AND completed_revision<$2`, item.DesktopVMID, item.Revision)
}
