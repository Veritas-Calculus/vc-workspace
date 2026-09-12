package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type GuestIdentityRevocation struct {
	DesktopVMID   int
	UserID        string
	GuestUsername string
	Revision      int64
	Reason        string
}

// predicate is a fixed internal SQL fragment, never caller-supplied input.
func queueGuestIdentityRevocations(ctx context.Context, tx pgx.Tx, predicate, reason string, args ...any) error {
	_, err := tx.Exec(ctx, `WITH affected AS (
		UPDATE guest_identity_bindings b SET state='disabled',updated_at=now()
		WHERE `+predicate+` RETURNING desktop_vmid,user_id,guest_username
	) INSERT INTO guest_identity_revocations(desktop_vmid,user_id,guest_username,reason)
	SELECT desktop_vmid,user_id,guest_username,'`+reason+`' FROM affected
	ON CONFLICT(desktop_vmid,user_id,guest_username) DO UPDATE
	SET requested_revision=guest_identity_revocations.requested_revision+1,
	    reason=excluded.reason,last_error='',updated_at=now()`, args...)
	return err
}

func (s *Store) QueueExpiredGuestIdentities(ctx context.Context) error {
	return s.QueueExpiredGuestIdentitiesForDesktop(ctx, 0)
}

// vmid zero is the maintenance scan. Connection preparation uses its own VM so
// reconnecting just before the next timer cannot revive an expired OS login.
func (s *Store) QueueExpiredGuestIdentitiesForDesktop(ctx context.Context, vmid int) error {
	tx, err := s.beginAccessChange(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := queueGuestIdentityRevocations(ctx, tx, `b.state <> 'disabled' AND b.session_expires_at <= now() AND ($1=0 OR b.desktop_vmid=$1)`, "expired", vmid); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) PendingGuestIdentityRevocations(ctx context.Context, vmid int) ([]GuestIdentityRevocation, error) {
	rows, err := s.pool.Query(ctx, `SELECT desktop_vmid,user_id,guest_username,requested_revision,reason
		FROM guest_identity_revocations WHERE requested_revision > completed_revision AND ($1=0 OR desktop_vmid=$1)
		ORDER BY updated_at,desktop_vmid,user_id LIMIT 100`, vmid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]GuestIdentityRevocation, 0)
	for rows.Next() {
		var item GuestIdentityRevocation
		if err := rows.Scan(&item.DesktopVMID, &item.UserID, &item.GuestUsername, &item.Revision, &item.Reason); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// Caller holds the desktop lock. Revision matching prevents an earlier Guest
// operation from acknowledging a newer revocation event queued while it ran.
func (s *Store) CompleteGuestIdentityRevocation(ctx context.Context, item GuestIdentityRevocation) error {
	tx, err := s.beginAccessChange(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `UPDATE guest_identity_revocations SET completed_revision=$4,last_error='',updated_at=now()
		WHERE desktop_vmid=$1 AND user_id=$2 AND guest_username=$3 AND requested_revision=$4 AND completed_revision<$4`, item.DesktopVMID, item.UserID, item.GuestUsername, item.Revision)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	// The old OS login is now gone. Do not let its past deadline queue a
	// second revocation while the next connection provisions the same account.
	if _, err := tx.Exec(ctx, `UPDATE guest_identity_bindings SET session_expires_at=NULL,state='disabled',updated_at=now()
		WHERE desktop_vmid=$1 AND user_id=$2 AND guest_username=$3`, item.DesktopVMID, item.UserID, item.GuestUsername); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE desktop_connection_sessions SET state=CASE WHEN expires_at<=now() THEN 'expired' ELSE 'revoked' END,
		closed_at=now(),updated_at=now(),termination_required=true
		WHERE desktop_vmid=$1 AND user_id=$2 AND guest_username=$3 AND state IN ('active','revoking')`, item.DesktopVMID, item.UserID, item.GuestUsername); err != nil {
		return err
	}
	// Completion and its system audit event commit together. Retrying a
	// completed revision neither repeats Guest work nor duplicates the event.
	if _, err := tx.Exec(ctx, `INSERT INTO audit_events(event_type,outcome,target_type,target_id,detail)
		VALUES('guest_identity.revoked','success','virtual_machine',($1::bigint)::text,
		jsonb_build_object('vmid',$1::bigint,'user_id',$2::text,'reason',$3::text,'revision',$4::bigint))`, item.DesktopVMID, item.UserID, item.Reason, item.Revision); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) RecordGuestIdentityRevocationFailure(ctx context.Context, item GuestIdentityRevocation) error {
	_, err := s.pool.Exec(ctx, `UPDATE guest_identity_revocations SET last_error='Guest revocation is pending',updated_at=now()
		WHERE desktop_vmid=$1 AND user_id=$2 AND guest_username=$3 AND requested_revision=$4 AND completed_revision<$4`, item.DesktopVMID, item.UserID, item.GuestUsername, item.Revision)
	return err
}

// Logout and password reset commit before any remote work. This also invalidates
// a connection request that passed HTTP authentication but is still preparing.
func (s *Store) RevokeNativeSession(ctx context.Context, digest []byte) error {
	tx, err := s.beginAccessChange(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var userID string
	err = tx.QueryRow(ctx, `DELETE FROM native_sessions WHERE token_digest=$1 RETURNING user_id`, digest).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE desktop_connection_sessions SET state='revoking',termination_required=true,updated_at=now()
		WHERE user_id=$1 AND state IN ('active','revoking')`, userID); err != nil {
		return err
	}
	if err := queueGuestIdentityRevocations(ctx, tx, `b.user_id=$1`, "credential_revoked", userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) CreateNativeDesktopConnectionSession(ctx context.Context, session DesktopConnectionSession, nativeDigest []byte) (DesktopConnectionSession, error) {
	if len(nativeDigest) == 0 || !session.ExpiresAt.After(time.Now()) {
		return DesktopConnectionSession{}, ErrNotFound
	}
	return s.createDesktopConnectionSession(ctx, session, nativeDigest)
}
