package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/store"
)

func (s *Server) cloneTaskCandidates(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	w.Header().Set("Cache-Control", "no-store")
	if session.User.Role != "platform_admin" {
		writeError(w, 403, "permission_denied", "Administrator role required")
		return
	}
	job, err := s.store.JobByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, 404, "job_not_found", "Job was not found")
		return
	}
	if err != nil {
		writeError(w, 503, "inspection_unavailable", "Unable to read the job")
		return
	}
	if job.Operation != "pve.template_clone" || job.State != "accepted" || job.UPID != "" || job.SourceVMID <= 0 || job.TaskNode == "" || job.CreatedAt.IsZero() {
		writeError(w, 409, "clone_discovery_unavailable", "An unresolved clone reservation is required")
		return
	}
	if s.pve == nil {
		writeError(w, 503, "inspection_unavailable", "PVE is not configured")
		return
	}
	var request desktopCloneRequest
	if json.Unmarshal(job.Request, &request) != nil || request.PVEPrincipal == "" || request.Name == "" || job.TargetVMID <= 0 || job.TargetNode == "" {
		writeError(w, 409, "clone_discovery_unavailable", "The job lacks a recorded submitting principal or target specification; independent review is required")
		return
	}
	// Task creation should closely follow reservation. A fixed window does not
	// widen with the age of an unresolved Job and cannot enumerate arbitrary work.
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	since := job.CreatedAt.Add(-30 * time.Second).Unix()
	until := job.CreatedAt.Add(15 * time.Minute).Unix()
	candidates, err := s.pve.CloneTaskCandidates(ctx, job.TaskNode, job.SourceVMID, since, until)
	if err != nil {
		writeError(w, 503, "inspection_unavailable", "Unable to obtain a complete bounded candidate list")
		return
	}
	type candidate struct {
		UPID            string `json:"upid"`
		StartTime       int64  `json:"start_time"`
		Status          string `json:"status"`
		ExitStatus      string `json:"exit_status,omitempty"`
		LogTargetStatus string `json:"log_target_status"`
	}
	result := make([]candidate, 0, len(candidates))
	for _, item := range candidates {
		principal := item.User
		if item.TokenID != "" {
			principal += "!" + item.TokenID
		}
		if principal != request.PVEPrincipal {
			continue
		}
		// Re-read the selected task: list results are neither completion proof
		// nor sufficient evidence of the task's immutable identity.
		evidence, err := s.pve.InspectTask(ctx, job.TaskNode, item.UPID)
		if err != nil || evidence.Type != item.Type || evidence.ID != item.ID || evidence.User != item.User || evidence.TokenID != item.TokenID || evidence.StartTime != item.StartTime {
			writeError(w, 503, "inspection_unavailable", "Unable to verify complete candidate evidence")
			return
		}
		source, target, recognized, err := s.pve.CloneLogTarget(ctx, job.TaskNode, item.UPID)
		if err != nil {
			writeError(w, 503, "inspection_unavailable", "Unable to read bounded clone log evidence")
			return
		}
		logStatus := "unrecognized"
		if recognized {
			logStatus = "mismatch"
			if source == job.SourceVMID && target == job.TargetVMID {
				logStatus = "matches"
			}
		}
		result = append(result, candidate{item.UPID, item.StartTime, evidence.Status, evidence.ExitStatus, logStatus})
	}
	targetStatus, err := s.observeCloneTarget(ctx, job, request)
	if err != nil {
		writeError(w, 503, "inspection_unavailable", "Unable to inspect the clone target")
		return
	}
	s.auditRequest(r, session.User.ID, "job.clone_candidates_inspected", "job", job.ID, map[string]any{"candidate_count": len(result), "target_status": targetStatus})
	writeJSON(w, 200, map[string]any{"job_id": job.ID, "candidates": result, "target_status": targetStatus})
}
