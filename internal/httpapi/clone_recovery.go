package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/auth"
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
)

func (s *Server) recoverCloneTask(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	w.Header().Set("Cache-Control", "no-store")
	if session.User.Role != "platform_admin" {
		writeError(w, 403, "permission_denied", "Administrator role required")
		return
	}
	if session.CSRFToken == "" || !auth.ConstantTimeEqual(r.Header.Get("X-CSRF-Token"), session.CSRFToken) {
		writeError(w, 403, "invalid_csrf_token", "CSRF token is invalid")
		return
	}
	var input struct {
		UPID   string `json:"upid"`
		Reason string `json:"reason"`
	}
	if decodeJSON(r, &input) != nil {
		writeError(w, 400, "invalid_request", "Invalid recovery request")
		return
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if !strings.HasPrefix(input.UPID, "UPID:") || len(input.UPID) > 1024 || strings.ContainsAny(input.UPID, "/\\?#\r\n\x00") || input.Reason == "" || len([]rune(input.Reason)) > 500 {
		writeError(w, 422, "invalid_recovery", "Provide a task handle and recovery reason")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	job, err := s.store.JobByID(ctx, r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, 404, "job_not_found", "Job was not found")
		return
	}
	if err != nil {
		writeError(w, 503, "recovery_unavailable", "Unable to read job")
		return
	}
	var request desktopCloneRequest
	if job.Operation != "pve.template_clone" || job.State != "accepted" || job.UPID != "" || job.SourceVMID <= 0 || job.TargetVMID <= 0 || job.TaskNode == "" || job.TargetNode == "" || job.CreatedAt.IsZero() || json.Unmarshal(job.Request, &request) != nil || request.Name == "" || request.PVEPrincipal == "" {
		writeError(w, 409, "recovery_state_changed", "An unresolved clone with original identity and target is required")
		return
	}
	if s.pve == nil {
		writeError(w, 503, "recovery_unavailable", "PVE is not configured")
		return
	}
	unlock, err := s.store.AcquireDesktopControlLock(ctx, job.TargetVMID)
	if err != nil {
		writeError(w, 503, "recovery_unavailable", "Unable to lock target")
		return
	}
	defer unlock()
	e, err := s.pve.InspectTask(ctx, job.TaskNode, input.UPID)
	if err != nil {
		writeError(w, 503, "recovery_unavailable", "Unable to inspect task")
		return
	}
	principal := e.User
	if e.TokenID != "" {
		principal += "!" + e.TokenID
	}
	if e.Type != "qmclone" || e.ID != strconv.Itoa(job.SourceVMID) || principal != request.PVEPrincipal || e.StartTime < job.CreatedAt.Add(-30*time.Second).Unix() || e.StartTime > job.CreatedAt.Add(15*time.Minute).Unix() || e.Status != "stopped" || e.ExitStatus != "OK" {
		writeError(w, 409, "recovery_evidence_mismatch", "Task identity, time or outcome does not match")
		return
	}
	source, target, recognized, err := s.pve.CloneLogTarget(ctx, job.TaskNode, input.UPID)
	if err != nil {
		writeError(w, 503, "recovery_unavailable", "Unable to inspect task log")
		return
	}
	if !recognized || source != job.SourceVMID || target != job.TargetVMID {
		writeError(w, 409, "recovery_evidence_mismatch", "Explicit clone target declaration is required")
		return
	}
	status, err := s.observeCloneTarget(ctx, job, request)
	if err != nil {
		writeError(w, 503, "recovery_unavailable", "Unable to inspect target")
		return
	}
	if status != "marker_matches" {
		writeError(w, 409, "recovery_evidence_mismatch", "An unlocked target with the original job marker is required")
		return
	}
	err = s.store.CommitCloneRecovery(ctx, job, input.UPID, session.User.ID, input.Reason)
	if errors.Is(err, store.ErrConflict) {
		writeError(w, 409, "recovery_state_changed", "Job or authorization changed; read the original job")
		return
	}
	if err != nil {
		writeError(w, 503, "recovery_commit_uncertain", "Unable to confirm recovery; read the original job before retrying")
		return
	}
	job.State = "running"
	job.UPID = input.UPID
	job.Error = ""
	go s.watchPVEJob(job.ID)
	writeJSON(w, 202, job)
}
