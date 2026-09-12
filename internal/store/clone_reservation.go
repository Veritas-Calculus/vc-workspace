package store

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// RecordCloneTaskHandle never replaces another task or reopens terminal work.
// An identical persisted handle is safe to confirm after a lost DB response.
func (s *Store) RecordCloneTaskHandle(ctx context.Context, expected Job, upid string) error {
	if expected.Operation != "pve.template_clone" || expected.ID == "" || strings.TrimSpace(upid) != upid || upid == "" || len(upid) > 4096 {
		return ErrConflict
	}
	tag, err := s.pool.Exec(ctx, `UPDATE pve_jobs SET state='running',upid=$2,error=NULL,updated_at=now()
 WHERE id=$1 AND operation='pve.template_clone' AND source_vmid=$3 AND target_vmid=$4
 AND target_node=$5 AND task_node=$6
 AND ((state='accepted' AND upid IS NULL) OR (state='running' AND upid=$2))`, expected.ID, upid, expected.SourceVMID, expected.TargetVMID, expected.TargetNode, expected.TaskNode)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

// ReserveDesktopCloneJob persists target ownership before any PVE mutation.
// A failed or uncertain attempt retains ownership: retries observe the same
// scoped job, while another request must obtain a different VMID.
func (s *Store) ReserveDesktopCloneJob(ctx context.Context, job Job) (Job, bool, error) {
	return s.reserveDesktopCloneJob(ctx, job, nil, false)
}

// ReserveAvailableDesktopCloneJob searches a bounded range starting at PVE's
// suggestion, excluding the observed cluster inventory and durable ownership.
// PVE remains authoritative if an external creator races this snapshot.
func (s *Store) ReserveAvailableDesktopCloneJob(ctx context.Context, job Job, occupiedVMIDs []int) (Job, bool, error) {
	return s.reserveDesktopCloneJob(ctx, job, occupiedVMIDs, true)
}

func (s *Store) reserveDesktopCloneJob(ctx context.Context, job Job, occupiedVMIDs []int, choose bool) (Job, bool, error) {
	if job.Operation != "pve.template_clone" || job.State != "accepted" || job.TargetVMID <= 0 || job.SourceVMID <= 0 || job.TargetVMID == job.SourceVMID || job.ID == "" || job.IdempotencyKey == "" {
		return Job{}, false, ErrConflict
	}
	tx, err := s.beginAccessChange(ctx)
	if err != nil {
		return Job{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var fingerprint string
	err = tx.QueryRow(ctx, `SELECT request_fingerprint FROM pve_jobs WHERE idempotency_key=$1`, job.IdempotencyKey).Scan(&fingerprint)
	if err == nil {
		if fingerprint != job.RequestFingerprint {
			return Job{}, false, ErrConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return Job{}, false, err
		}
		prior, err := s.JobByIdempotencyKey(ctx, job.IdempotencyKey)
		return prior, false, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Job{}, false, err
	}
	if choose {
		// Fixed scan budget prevents a fragmented range from holding the
		// authorization lock indefinitely. Never delete old reservations.
		err = tx.QueryRow(ctx, `SELECT candidate FROM generate_series($1::bigint,$1::bigint+1023) candidate
 WHERE candidate<>$2 AND NOT (candidate=ANY(COALESCE($3::bigint[],'{}'::bigint[])))
 AND NOT EXISTS(SELECT 1 FROM managed_desktops WHERE vmid=candidate)
 AND NOT EXISTS(SELECT 1 FROM pve_jobs WHERE target_vmid=candidate AND operation IN ('pve.template_clone','image.build'))
 ORDER BY candidate LIMIT 1`, job.TargetVMID, job.SourceVMID, occupiedVMIDs).Scan(&job.TargetVMID)
		if errors.Is(err, pgx.ErrNoRows) {
			return Job{}, false, ErrConflict
		}
		if err != nil {
			return Job{}, false, err
		}
	}
	var occupied bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM managed_desktops WHERE vmid=$1)
 OR EXISTS(SELECT 1 FROM pve_jobs WHERE target_vmid=$1 AND operation='pve.template_clone')`, job.TargetVMID).Scan(&occupied)
	if err != nil {
		return Job{}, false, err
	}
	if occupied {
		return Job{}, false, ErrConflict
	}
	_, err = tx.Exec(ctx, `INSERT INTO pve_jobs(id,idempotency_key,operation,state,source_vmid,target_vmid,target_node,task_node,request,created_by,request_fingerprint)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, job.ID, job.IdempotencyKey, job.Operation, job.State, job.SourceVMID, job.TargetVMID, nullableString(job.TargetNode), nullableString(job.TaskNode), job.Request, nullableString(job.CreatedBy), job.RequestFingerprint)
	if err != nil {
		var dbErr *pgconn.PgError
		if errors.As(err, &dbErr) && dbErr.Code == "23505" {
			return Job{}, false, ErrConflict
		}
		return Job{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Job{}, false, err
	}
	stored, err := s.JobByID(ctx, job.ID)
	return stored, true, err
}
