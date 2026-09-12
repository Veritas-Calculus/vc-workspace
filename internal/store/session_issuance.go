package store

import (
	"context"
	"time"
)

func (s *Store) CreateSessionForVerifiedUser(ctx context.Context, digest []byte, user User, csrf string, expiresAt time.Time) error {
	return s.createVerifiedLoginSession(ctx, digest, user, csrf, expiresAt, false)
}

func (s *Store) CreateNativeSessionForVerifiedUser(ctx context.Context, digest []byte, user User, expiresAt time.Time) error {
	return s.createVerifiedLoginSession(ctx, digest, user, "", expiresAt, true)
}

// Password verification/IdP exchange happen outside the transaction. A reset
// or disable must not allow that stale result to mint a new session afterward.
func (s *Store) createVerifiedLoginSession(ctx context.Context, digest []byte, user User, csrf string, expiresAt time.Time, native bool) error {
	tx, err := s.beginAccessChange(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var current bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND disabled=false AND COALESCE(password_hash,'')=$2)`, user.ID, user.PasswordHash).Scan(&current); err != nil {
		return err
	}
	if !current || !expiresAt.After(time.Now()) {
		return ErrNotFound
	}
	if native {
		_, err = tx.Exec(ctx, `INSERT INTO native_sessions(token_digest,user_id,expires_at) VALUES($1,$2,$3)`, digest, user.ID, expiresAt)
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO web_sessions(token_digest,user_id,csrf_token,expires_at) VALUES($1,$2,$3,$4)`, digest, user.ID, csrf, expiresAt)
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
