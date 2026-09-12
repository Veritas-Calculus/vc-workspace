package store

import (
	"context"
	"errors"
	"math"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// CommitNativeGuestRecovery must be called under the desktop control lock after
// a fresh Guest inspection proves the same immutable identity is fully revoked
// with no processes or login writers. It never authorizes or changes the Guest.
// The expected account is a CAS snapshot; all prior database work must be settled.
func (s *Store) CommitNativeGuestRecovery(ctx context.Context, expected NativeGuestAccount, revision int64, connectionID, actorID, receiptID, reason string) (NativeGuestAccount, error) {
	reason = strings.TrimSpace(reason)
	if !expected.validIdentity() || expected.OSFamily != "linux" || expected.Operation != "revoke" || expected.State != "applied" || expected.Revision < 1 ||
		revision <= expected.Revision || revision == math.MaxInt64 || !nativeConnectionID.MatchString(connectionID) || connectionID == expected.ConnectionID ||
		actorID == "" || receiptID == "" || len(receiptID) > 128 || reason == "" || len([]rune(reason)) > 500 {
		return NativeGuestAccount{}, ErrConflict
	}
	tx, err := s.beginAccessChange(ctx)
	if err != nil {
		return NativeGuestAccount{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var safe bool
	err = tx.QueryRow(ctx, `SELECT
 EXISTS(SELECT 1 FROM users WHERE id=$2 AND role='platform_admin' AND NOT disabled)
 AND NOT EXISTS(SELECT 1 FROM desktop_connection_sessions WHERE desktop_vmid=$1 AND state IN ('active','revoking'))
 AND NOT EXISTS(SELECT 1 FROM native_guest_accounts WHERE desktop_vmid=$1 AND (state='pending' OR operation IN ('issue','retire')))
 AND NOT EXISTS(SELECT 1 FROM desktop_leases WHERE desktop_id=$1::bigint::text AND state='active')
 AND NOT EXISTS(SELECT 1 FROM agent_guest_sessions WHERE desktop_vmid=$1 AND state IN ('provisioning','ready','revoking'))
 AND NOT EXISTS(SELECT 1 FROM guest_identity_revocations WHERE desktop_vmid=$1 AND requested_revision>completed_revision)
 AND NOT EXISTS(SELECT 1 FROM desktop_computer_revocations WHERE desktop_vmid=$1 AND requested_revision>completed_revision)`, expected.DesktopVMID, actorID).Scan(&safe)
	if err != nil {
		return NativeGuestAccount{}, err
	}
	if !safe {
		return NativeGuestAccount{}, ErrConflict
	}
	_, err = tx.Exec(ctx, `INSERT INTO native_guest_recoveries(id,desktop_vmid,user_id,actor_user_id,reason,previous_revision,previous_connection_id,recovered_revision,recovered_connection_id)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, receiptID, expected.DesktopVMID, expected.UserID, actorID, reason, expected.Revision, expected.ConnectionID, revision, connectionID)
	if err != nil {
		return NativeGuestAccount{}, nativeRecoveryError(err)
	}
	updated, err := scanNativeGuestAccount(tx.QueryRow(ctx, `UPDATE native_guest_accounts SET revision=$10,connection_id=$11,updated_at=now()
 WHERE desktop_vmid=$1 AND user_id=$2 AND guest_username=$3 AND os_family=$4 AND guest_uid=$5 AND guest_sid=$6
 AND revision=$7 AND connection_id=$8 AND operation='revoke' AND state=$9
 RETURNING `+nativeAccountColumns, expected.DesktopVMID, expected.UserID, expected.GuestUsername, expected.OSFamily, expected.GuestUID, expected.GuestSID, expected.Revision, expected.ConnectionID, expected.State, revision, connectionID))
	if err != nil {
		return NativeGuestAccount{}, nativeRecoveryError(err)
	}
	return updated, tx.Commit(ctx)
}

func nativeRecoveryError(err error) error {
	var dbErr *pgconn.PgError
	if errors.Is(err, ErrNotFound) || (errors.As(err, &dbErr) && (dbErr.Code == "23505" || dbErr.Code == "23514" || dbErr.Code == "23503")) {
		return ErrConflict
	}
	return err
}
