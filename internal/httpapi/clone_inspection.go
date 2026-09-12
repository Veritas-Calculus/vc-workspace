package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Veritas-Calculus/vc-workspace/internal/store"
)

// Inspection is evidence only. A matching mutable description neither proves
// task completion nor authorizes importing a task handle or granting access.
func (s *Server) inspectCloneTarget(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	w.Header().Set("Cache-Control", "no-store")
	if session.User.Role != "platform_admin" {
		writeError(w, http.StatusForbidden, "permission_denied", "Administrator role required")
		return
	}
	job, err := s.store.JobByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "job_not_found", "Job was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "inspection_unavailable", "Unable to read the job")
		return
	}
	var request desktopCloneRequest
	if job.Operation != "pve.template_clone" || job.TargetVMID <= 0 || job.TargetNode == "" || json.Unmarshal(job.Request, &request) != nil || request.Name == "" {
		writeError(w, http.StatusConflict, "clone_inspection_unavailable", "Job has no inspectable clone target")
		return
	}
	if s.pve == nil {
		writeError(w, http.StatusServiceUnavailable, "inspection_unavailable", "PVE is not configured")
		return
	}
	status, err := s.observeCloneTarget(r.Context(), job, request)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "inspection_unavailable", "Unable to inspect the clone target")
		return
	}
	s.auditRequest(r, session.User.ID, "job.clone_inspected", "job", job.ID, map[string]any{"status": status})
	writeJSON(w, http.StatusOK, map[string]any{"job_id": job.ID, "target_vmid": job.TargetVMID, "status": status})
}

// Refuse post-clone writes when current target evidence no longer matches.
// This does not lock PVE against out-of-band administration.
func (s *Server) requireCloneTarget(ctx context.Context, job store.Job, request desktopCloneRequest) error {
	status, err := s.observeCloneTarget(ctx, job, request)
	if err != nil {
		return err
	}
	if status != "marker_matches" {
		return errors.New("clone target is not verified: " + status)
	}
	return nil
}

// Observations are not an atomic snapshot or authorization to adopt a task.
func (s *Server) observeCloneTarget(ctx context.Context, job store.Job, request desktopCloneRequest) (string, error) {
	summary, err := s.pve.Summary(ctx)
	if err != nil {
		return "", err
	}
	status := "target_absent"
	matches := 0
	for _, vm := range summary.VMs {
		if vm.VMID == job.TargetVMID {
			matches++
		}
	}
	if matches > 1 {
		return "", errors.New("ambiguous clone target")
	}
	for _, vm := range summary.VMs {
		if vm.VMID != job.TargetVMID {
			continue
		}
		status = "target_mismatch"
		if vm.Node != job.TargetNode || vm.Kind != "qemu" || vm.Template {
			break
		}
		configuration, err := s.pve.VMConfiguration(ctx, vm.Node, vm.VMID)
		if err != nil {
			return "", err
		}
		if configuration.Template || configuration.Name != request.Name || configuration.Description != "VC Workspace clone job: "+job.ID {
			break
		}
		status = "marker_matches"
		if configuration.Lock != "" {
			status = "target_locked"
		}
		break
	}
	return status, nil
}
