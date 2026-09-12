package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"regexp"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/guestidentity"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// NativeGuestAccount records OS identity separately from connection credentials.
// It contains no password. Pending is durable intent, never permission to replay
// a Guest command after losing its receipt. Callers retain the exact revision.
type NativeGuestAccount struct {
	DesktopVMID   int
	UserID        string
	GuestUsername string
	OSFamily      string
	GuestUID      uint32
	GuestSID      string
	Revision      int64
	Operation     string
	State         string
	ConnectionID  string
	ExpiresAt     *time.Time
}

func NativeGuestUsername(userID string) string {
	digest := sha256.Sum256([]byte(userID))
	return fmt.Sprintf("vcw%x", digest[:6])
}

func (a NativeGuestAccount) validIdentity() bool {
	return a.DesktopVMID > 0 && a.UserID != "" && a.GuestUsername == NativeGuestUsername(a.UserID) &&
		guestidentity.ValidAccount(a.OSFamily, a.GuestUID, a.GuestSID)
}

const nativeAccountColumns = `desktop_vmid,user_id,guest_username,os_family,guest_uid,guest_sid,revision,operation,state,connection_id,expires_at`

func scanNativeGuestAccount(row pgx.Row) (NativeGuestAccount, error) {
	var a NativeGuestAccount
	err := row.Scan(&a.DesktopVMID, &a.UserID, &a.GuestUsername, &a.OSFamily, &a.GuestUID, &a.GuestSID,
		&a.Revision, &a.Operation, &a.State, &a.ConnectionID, &a.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

func (s *Store) NativeGuestAccount(ctx context.Context, vmid int, userID string) (NativeGuestAccount, error) {
	return scanNativeGuestAccount(s.pool.QueryRow(ctx, `SELECT `+nativeAccountColumns+` FROM native_guest_accounts WHERE desktop_vmid=$1 AND user_id=$2`, vmid, userID))
}

func (s *Store) NativeGuestAccountsForDesktop(ctx context.Context, vmid int) ([]NativeGuestAccount, error) {
	if vmid <= 0 {
		return nil, ErrConflict
	}
	rows, err := s.pool.Query(ctx, `SELECT `+nativeAccountColumns+` FROM native_guest_accounts WHERE desktop_vmid=$1 ORDER BY user_id`, vmid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []NativeGuestAccount
	for rows.Next() {
		item, err := scanNativeGuestAccount(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// The caller must independently prove Guest ownership before using this method;
// neither a username nor an RDP success receipt proves account provenance.
// This deliberately does not adopt or backfill pre-028 Guest identities.
func (s *Store) BindNativeGuestAccount(ctx context.Context, a NativeGuestAccount, nativeDigest []byte) (NativeGuestAccount, error) {
	if !a.validIdentity() || len(nativeDigest) == 0 {
		return NativeGuestAccount{}, ErrConflict
	}
	tx, err := s.beginAccessChange(ctx)
	if err != nil {
		return NativeGuestAccount{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	bound, err := scanNativeGuestAccount(tx.QueryRow(ctx, `INSERT INTO native_guest_accounts(desktop_vmid,user_id,guest_username,os_family,guest_uid,guest_sid)
 SELECT $1,$2,$3,$4,$5,$6 WHERE `+nativeAccountAuthorization+`
 ON CONFLICT(desktop_vmid,user_id) DO UPDATE SET updated_at=now()
 WHERE native_guest_accounts.guest_username=excluded.guest_username AND native_guest_accounts.os_family=excluded.os_family
   AND native_guest_accounts.guest_uid=excluded.guest_uid AND native_guest_accounts.guest_sid=excluded.guest_sid
 RETURNING `+nativeAccountColumns, a.DesktopVMID, a.UserID, a.GuestUsername, a.OSFamily, a.GuestUID, a.GuestSID, nativeDigest))
	if errors.Is(err, ErrNotFound) {
		err = ErrConflict
	}
	if err != nil {
		return NativeGuestAccount{}, err
	}
	return bound, tx.Commit(ctx)
}

// Positional parameters 1..7 are the immutable identity plus Native token hash.
const nativeAccountAuthorization = `EXISTS(
 SELECT 1 FROM guest_identity_bindings b JOIN managed_desktops d ON d.vmid=b.desktop_vmid
 JOIN identity_profiles p ON p.id=COALESCE(b.profile_id,'managed-local-'||d.os_family)
 JOIN effective_user_desktop_access e ON e.desktop_vmid=b.desktop_vmid AND e.user_id=b.user_id
 JOIN native_sessions n ON n.user_id=b.user_id AND n.token_digest=$7 AND n.expires_at>now()
 WHERE b.desktop_vmid=$1 AND b.user_id=$2 AND b.guest_username=$3 AND b.state IN ('provisioning','ready')
   AND d.os_family=$4 AND d.enabled AND d.present AND p.platform=$4 AND p.mode='managed_local' AND p.enabled
   AND p.id=COALESCE(d.identity_profile_id,'managed-local-'||d.os_family)
) AND NOT EXISTS(SELECT 1 FROM guest_identity_revocations WHERE desktop_vmid=$1 AND requested_revision>completed_revision)`

var nativeConnectionID = regexp.MustCompile(`^conn_[A-Za-z0-9_-]{8,128}$`)

// Reserve before installing credentials. Ordinary reconnect requires a settled
// retirement, not account revocation: the future Guest adapter preserves the OS
// desktop when retiring a password. Fresh issue cannot replace unresolved work.
func (s *Store) BeginNativeGuestCredential(ctx context.Context, a NativeGuestAccount, connectionID string, expiresAt time.Time, nativeDigest []byte) (NativeGuestAccount, error) {
	if !a.validIdentity() || a.Revision < 0 || a.Revision == math.MaxInt64 || !nativeConnectionID.MatchString(connectionID) ||
		connectionID == a.ConnectionID || len(nativeDigest) == 0 || !expiresAt.After(time.Now()) || expiresAt.After(time.Now().Add(8*time.Hour)) || expiresAt.Nanosecond() != 0 {
		return NativeGuestAccount{}, ErrConflict
	}
	tx, err := s.beginAccessChange(ctx)
	if err != nil {
		return NativeGuestAccount{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	next, err := scanNativeGuestAccount(tx.QueryRow(ctx, `UPDATE native_guest_accounts SET revision=revision+1,
 operation='issue',state='pending',connection_id=$11,expires_at=$12,native_session_digest=$7,updated_at=now()
 WHERE desktop_vmid=$1 AND user_id=$2 AND guest_username=$3 AND os_family=$4 AND guest_uid=$5 AND guest_sid=$6
 AND revision=$8 AND operation=$9 AND state=$10 AND (state='idle' OR (state='applied' AND operation IN ('retire','revoke')))
 AND (operation<>'retire' OR expires_at>now())
 AND `+nativeAccountAuthorization+`
 AND NOT EXISTS(SELECT 1 FROM desktop_connection_sessions WHERE id=$11 OR (desktop_vmid=$1 AND state IN ('active','revoking')))
 AND NOT EXISTS(SELECT 1 FROM desktop_computer_revocations WHERE desktop_vmid=$1 AND requested_revision>completed_revision)
 AND NOT EXISTS(SELECT 1 FROM agent_guest_sessions WHERE desktop_vmid=$1 AND state IN ('provisioning','ready','revoking'))
 RETURNING `+nativeAccountColumns, a.DesktopVMID, a.UserID, a.GuestUsername, a.OSFamily, a.GuestUID, a.GuestSID,
		nativeDigest, a.Revision, a.Operation, a.State, connectionID, expiresAt))
	var conflict *pgconn.PgError
	if errors.Is(err, ErrNotFound) || (errors.As(err, &conflict) && conflict.Code == "23505") {
		err = ErrConflict
	}
	if err != nil {
		return NativeGuestAccount{}, err
	}
	return next, tx.Commit(ctx)
}

func (s *Store) RetireNativeGuestCredential(ctx context.Context, a NativeGuestAccount) (NativeGuestAccount, error) {
	if a.Operation != "issue" || a.State != "applied" {
		return NativeGuestAccount{}, ErrConflict
	}
	return s.beginNativeGuestCleanup(ctx, a, "retire")
}

// Revocation supersedes an unacknowledged issue or retirement with a higher
// durable revision. It never retries that old credential write. Applied revoke
// is terminal for this connection; a later issue needs a different connection.
func (s *Store) RevokeNativeGuestAccount(ctx context.Context, a NativeGuestAccount) (NativeGuestAccount, error) {
	if a.State == "idle" || a.Operation == "revoke" {
		return NativeGuestAccount{}, ErrConflict
	}
	return s.beginNativeGuestCleanup(ctx, a, "revoke")
}

func (s *Store) beginNativeGuestCleanup(ctx context.Context, a NativeGuestAccount, op string) (NativeGuestAccount, error) {
	if !a.validIdentity() || a.Revision <= 0 || a.Revision == math.MaxInt64 {
		return NativeGuestAccount{}, ErrConflict
	}
	// No current HTTP authorization is required to finish old cleanup. The
	// immutable account, connection, version and exact preceding intent are.
	next, err := scanNativeGuestAccount(s.pool.QueryRow(ctx, `UPDATE native_guest_accounts SET revision=revision+1,
 operation=$12,state='pending',expires_at=CASE WHEN $12='revoke' THEN NULL ELSE expires_at END,
 native_session_digest=NULL,updated_at=now()
 WHERE desktop_vmid=$1 AND user_id=$2 AND guest_username=$3 AND os_family=$4 AND guest_uid=$5 AND guest_sid=$6
 AND revision=$7 AND operation=$8 AND state=$9 AND connection_id=$10 AND expires_at IS NOT DISTINCT FROM $11
 AND ($12<>'retire' OR expires_at>now())
 RETURNING `+nativeAccountColumns, a.DesktopVMID, a.UserID, a.GuestUsername, a.OSFamily, a.GuestUID, a.GuestSID,
		a.Revision, a.Operation, a.State, a.ConnectionID, a.ExpiresAt, op))
	if errors.Is(err, ErrNotFound) {
		err = ErrConflict
	}
	return next, err
}

// A database acknowledgement is not proof of Guest convergence. The adapter
// calls this only after independently validating the exact OS receipt. Native
// token/access/queue state is checked again before acknowledging issued creds.
func (s *Store) CompleteNativeGuestAccountOperation(ctx context.Context, a NativeGuestAccount) error {
	_, err := s.completeNativeGuestAccountOperation(ctx, a, nil)
	return err
}

// The connection and Guest acknowledgement are one transaction. Losing the
// commit response is uncertain; a caller must not issue the password again.
func (s *Store) CompleteNativeGuestConnection(ctx context.Context, a NativeGuestAccount) (DesktopConnectionSession, error) {
	if a.Operation != "issue" {
		return DesktopConnectionSession{}, ErrConflict
	}
	return s.completeNativeGuestAccountOperation(ctx, a, nil)
}

// A Gateway connection acknowledges the OS intent, inserts the logical session,
// and mints its sole ticket in ONE transaction. A ticket failure rolls back the
// acknowledgement too, leaving a durable pending intent for crash recovery.
func (s *Store) CompleteNativeGuestGatewayConnection(ctx context.Context, a NativeGuestAccount, request NativeGatewayTicketRequest) (DesktopConnectionSession, NativeGatewayTicket, error) {
	if a.Operation != "issue" || request.ConnectionID != a.ConnectionID {
		return DesktopConnectionSession{}, NativeGatewayTicket{}, ErrConflict
	}
	var ticket NativeGatewayTicket
	connection, err := s.completeNativeGuestAccountOperation(ctx, a, func(tx pgx.Tx, c DesktopConnectionSession) error {
		var err error
		ticket, err = createNativeGatewayTicket(ctx, tx, request)
		return err
	})
	if err != nil {
		return DesktopConnectionSession{}, NativeGatewayTicket{}, err
	}
	return connection, ticket, nil
}

func (s *Store) completeNativeGuestAccountOperation(ctx context.Context, a NativeGuestAccount, finalize func(pgx.Tx, DesktopConnectionSession) error) (DesktopConnectionSession, error) {
	if !a.validIdentity() || a.Revision <= 0 || a.State != "pending" {
		return DesktopConnectionSession{}, ErrConflict
	}
	tx, err := s.beginAccessChange(ctx)
	if err != nil {
		return DesktopConnectionSession{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Preserve the exact initiating Native session before the pending intent
	// clears it. Never derive device ownership later from another user session.
	var initiatingDigest []byte
	if a.Operation == "issue" {
		if err := tx.QueryRow(ctx, `SELECT native_session_digest FROM native_guest_accounts
 WHERE desktop_vmid=$1 AND user_id=$2`, a.DesktopVMID, a.UserID).Scan(&initiatingDigest); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return DesktopConnectionSession{}, ErrConflict
			}
			return DesktopConnectionSession{}, err
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE native_guest_accounts a SET state='applied',native_session_digest=NULL,updated_at=now()
 WHERE a.desktop_vmid=$1 AND a.user_id=$2 AND a.guest_username=$3 AND a.os_family=$4 AND a.guest_uid=$5 AND a.guest_sid=$6
 AND a.revision=$7 AND a.operation=$8 AND a.state='pending' AND a.connection_id=$9 AND a.expires_at IS NOT DISTINCT FROM $10
 AND (a.operation<>'issue' OR (a.expires_at>now() AND EXISTS(
   SELECT 1 FROM native_sessions n JOIN effective_user_desktop_access e ON e.user_id=n.user_id
   JOIN managed_desktops d ON d.vmid=e.desktop_vmid JOIN guest_identity_bindings b ON b.desktop_vmid=d.vmid AND b.user_id=n.user_id
   JOIN identity_profiles p ON p.id=COALESCE(b.profile_id,'managed-local-'||d.os_family)
   WHERE n.token_digest=a.native_session_digest AND n.user_id=a.user_id AND n.expires_at>now()
   AND e.desktop_vmid=a.desktop_vmid AND d.os_family=a.os_family AND d.enabled AND d.present AND p.platform=a.os_family AND p.enabled AND p.mode='managed_local'
   AND p.id=COALESCE(d.identity_profile_id,'managed-local-'||d.os_family)
   AND b.guest_username=a.guest_username AND b.state IN ('provisioning','ready')
 ) AND NOT EXISTS(SELECT 1 FROM guest_identity_revocations q WHERE q.desktop_vmid=a.desktop_vmid AND q.requested_revision>q.completed_revision)))`,
		a.DesktopVMID, a.UserID, a.GuestUsername, a.OSFamily, a.GuestUID, a.GuestSID, a.Revision, a.Operation, a.ConnectionID, a.ExpiresAt)
	if err != nil {
		return DesktopConnectionSession{}, err
	}
	if tag.RowsAffected() != 1 {
		return DesktopConnectionSession{}, ErrConflict
	}
	var connection DesktopConnectionSession
	if a.Operation == "issue" {
		if a.ExpiresAt == nil {
			return connection, ErrConflict
		}
		// The UPDATE above checked the exact pending Native token and current
		// access/profile under this same transaction's authorization lock.
		connection, err = insertDesktopConnectionSession(ctx, tx, DesktopConnectionSession{
			ID: a.ConnectionID, UserID: a.UserID, DesktopVMID: a.DesktopVMID,
			GuestUsername: a.GuestUsername, ExpiresAt: *a.ExpiresAt,
		}, initiatingDigest, true)
	} else {
		_, err = tx.Exec(ctx, `UPDATE desktop_connection_sessions SET state=CASE WHEN expires_at<=now() THEN 'expired' ELSE 'revoked' END,
 closed_at=now(),updated_at=now(),termination_required=termination_required OR $5='revoke'
 WHERE id=$1 AND desktop_vmid=$2 AND user_id=$3 AND guest_username=$4 AND state IN ('active','revoking')`,
			a.ConnectionID, a.DesktopVMID, a.UserID, a.GuestUsername, a.Operation)
		if err == nil && a.Operation == "revoke" {
			_, err = tx.Exec(ctx, `UPDATE guest_identity_bindings SET session_expires_at=NULL,updated_at=now()
 WHERE desktop_vmid=$1 AND user_id=$2 AND guest_username=$3`, a.DesktopVMID, a.UserID, a.GuestUsername)
		}
	}
	if err != nil {
		return DesktopConnectionSession{}, err
	}
	if finalize != nil {
		if err := finalize(tx, connection); err != nil {
			return DesktopConnectionSession{}, err
		}
	}
	return connection, tx.Commit(ctx)
}

// Restart recovery enumerates durable intents, not in-memory work. Issue/retire
// pending is an uncertain Guest outcome: inspect it or supersede with a revoke,
// never blindly resend a password. Expired retained desktops also need cleanup.
func (s *Store) NativeGuestAccountsNeedingRecovery(ctx context.Context, limit int) ([]NativeGuestAccount, error) {
	if limit < 1 || limit > 1000 {
		return nil, ErrConflict
	}
	rows, err := s.pool.Query(ctx, `SELECT `+nativeAccountColumns+` FROM native_guest_accounts
 WHERE state='pending' OR (operation IN ('issue','retire') AND expires_at<=now())
 OR (operation='issue' AND state='applied' AND NOT EXISTS(SELECT 1 FROM desktop_connection_sessions c
 WHERE c.id=native_guest_accounts.connection_id AND c.state IN ('active','revoking')))
 ORDER BY updated_at,desktop_vmid,user_id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]NativeGuestAccount, 0)
	for rows.Next() {
		item, err := scanNativeGuestAccount(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
