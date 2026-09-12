package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

// CommitCloneRecovery only attaches a freshly verified task to its reservation.
// The caller must verify principal, task identity/time, explicit log target and
// target configuration under the desktop control lock. This transaction does
// not mark the clone successful, enable a desktop, or mutate PVE.
// A lost response is resolved by reading the Job; retries cannot re-open work.
func (s *Store) CommitCloneRecovery(ctx context.Context, expected Job, upid, actorID, reason string) error {
	reason = strings.TrimSpace(reason)
	if expected.Operation != "pve.template_clone" || expected.ID == "" || expected.State != "accepted" || expected.UPID != "" || expected.SourceVMID <= 0 || expected.TargetVMID <= 0 || expected.SourceVMID == expected.TargetVMID || expected.TargetNode == "" || expected.TaskNode == "" || !json.Valid(expected.Request) ||
		!strings.HasPrefix(upid, "UPID:") || len(upid) > 1024 || strings.ContainsAny(upid, "/\\?#\r\n\x00") || strings.TrimSpace(upid) != upid || actorID == "" || reason == "" || len([]rune(reason)) > 500 {
		return ErrConflict
	}
	tx, err := s.beginAccessChange(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var authorized bool
	if err := tx.QueryRow(ctx, `SELECT role='platform_admin' AND NOT disabled FROM users WHERE id=$1 FOR SHARE`, actorID).Scan(&authorized); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		return ErrConflict
	}
	if !authorized {
		return ErrConflict
	}
	tag, err := tx.Exec(ctx, `UPDATE pve_jobs SET state='running',upid=$2,error=NULL,updated_at=now()
 WHERE id=$1 AND operation='pve.template_clone' AND state='accepted' AND upid IS NULL
 AND source_vmid=$3 AND target_vmid=$4 AND target_node=$5 AND task_node=$6 AND request=$7::jsonb`, expected.ID, upid, expected.SourceVMID, expected.TargetVMID, expected.TargetNode, expected.TaskNode, expected.Request)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	hash := sha256.Sum256([]byte(upid))
	detail, err := json.Marshal(sanitizeAuditValue(map[string]any{"reason": reason, "task_handle_sha256": hex.EncodeToString(hash[:]), "source_vmid": expected.SourceVMID, "target_vmid": expected.TargetVMID}, 0))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_id,event_type,outcome,target_type,target_id,detail) VALUES($1,'job.clone_recovered','success','job',$2,$3)`, actorID, expected.ID, detail)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
