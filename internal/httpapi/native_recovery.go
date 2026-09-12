package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Veritas-Calculus/vc-workspace/internal/auth"
	"github.com/Veritas-Calculus/vc-workspace/internal/computer"
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
)

// Diagnostic only: a closed newer Guest is evidence of lost database history,
// not authorization to import it or reset the Guest's monotonic fence.
type nativeRecoveryReport struct {
	UserID           string `json:"user_id"`
	DatabaseRevision int64  `json:"database_revision"`
	GuestRevision    *int64 `json:"guest_revision,omitempty"`
	Status           string `json:"status"`
}

func (s *Server) recoverNativeAccount(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	w.Header().Set("Cache-Control", "no-store")
	if session.User.Role != "platform_admin" {
		writeError(w, http.StatusForbidden, "permission_denied", "Administrator role required")
		return
	}
	if !auth.ConstantTimeEqual(r.Header.Get("X-CSRF-Token"), session.CSRFToken) {
		writeError(w, http.StatusForbidden, "invalid_csrf_token", "CSRF token is invalid")
		return
	}
	vmid, err := strconv.Atoi(r.PathValue("vmid"))
	if err != nil || vmid <= 0 || r.PathValue("user_id") == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "Invalid account target")
		return
	}
	var input struct {
		DatabaseRevision int64  `json:"database_revision"`
		GuestRevision    int64  `json:"guest_revision"`
		Reason           string `json:"reason"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Invalid recovery request")
		return
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if input.DatabaseRevision < 1 || input.GuestRevision <= input.DatabaseRevision || input.Reason == "" || len([]rune(input.Reason)) > 500 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_recovery", "Provide the inspected revisions and a recovery reason")
		return
	}
	unlock, err := s.store.AcquireDesktopControlLock(r.Context(), vmid)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "recovery_unavailable", "Unable to lock the desktop")
		return
	}
	defer unlock()
	machine, err := s.managedVirtualMachine(r.Context(), vmid)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "desktop_not_found", "Managed desktop not found")
		return
	}
	if err != nil || machine.Status != "running" {
		writeError(w, http.StatusConflict, "recovery_unavailable", "A running managed desktop is required")
		return
	}
	a, err := s.store.NativeGuestAccount(r.Context(), vmid, r.PathValue("user_id"))
	if err != nil || a.Revision != input.DatabaseRevision || a.Operation != "revoke" || a.State != "applied" {
		writeError(w, http.StatusConflict, "recovery_state_changed", "The account is not at the inspected settled revocation")
		return
	}
	lifecycle, err := s.nativeLifecycle(a)
	if err != nil {
		writeError(w, http.StatusConflict, "recovery_unavailable", "Native recovery is unavailable for this account")
		return
	}
	observed, err := lifecycle.InspectNativeAccount(r.Context(), machine, a.GuestUsername)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "recovery_inspection_unavailable", "Unable to inspect Guest state")
		return
	}
	if report := compareNativeRecovery(a, observed); report.Status != "guest_ahead_closed" || report.GuestRevision == nil || *report.GuestRevision != input.GuestRevision {
		writeError(w, http.StatusConflict, "recovery_state_changed", "Guest state no longer matches the inspected closed account")
		return
	}
	id, err := auth.OpaqueToken(18)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to allocate recovery receipt")
		return
	}
	updated, err := s.store.CommitNativeGuestRecovery(r.Context(), a, input.GuestRevision, observed.Lifecycle.ConnectionID, session.User.ID, "recovery_"+id, input.Reason)
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "recovery_state_changed", "Outstanding work or changed authorization prevents recovery")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to commit recovery")
		return
	}
	writeJSON(w, http.StatusOK, compareNativeRecovery(updated, observed))
}

func compareNativeRecovery(a store.NativeGuestAccount, o computer.NativeAccountObservation) nativeRecoveryReport {
	r := nativeRecoveryReport{UserID: a.UserID, DatabaseRevision: a.Revision, Status: "identity_mismatch"}
	if !o.Exists || o.Identity != nativeCredential(a).Identity {
		return r
	}
	r.Status = "unversioned"
	if o.Lifecycle == nil {
		return r
	}
	if o.Lifecycle.Identity != o.Identity {
		r.Status = "identity_mismatch"
		return r
	}
	v := o.Lifecycle.Revision
	r.GuestRevision = &v
	r.Status = "lifecycle_mismatch"
	if a.State == "applied" && a.Operation == "revoke" && o.Matches(nativeCredential(a), "revoked") && o.Closed() {
		r.Status = "aligned_closed"
	} else if v > a.Revision && o.Lifecycle.Phase == "revoked" && o.Lifecycle.ExpiresUnixSeconds == 0 && o.Closed() {
		r.Status = "guest_ahead_closed"
	}
	return r
}

func (s *Server) inspectNativeRecovery(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	w.Header().Set("Cache-Control", "no-store")
	if session.User.Role != "platform_admin" {
		writeError(w, http.StatusForbidden, "permission_denied", "Administrator role required")
		return
	}
	vmid, err := strconv.Atoi(r.PathValue("vmid"))
	if err != nil || vmid <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_vmid", "Invalid desktop identifier")
		return
	}
	unlock, err := s.store.AcquireDesktopControlLock(r.Context(), vmid)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "recovery_inspection_unavailable", "Unable to lock the desktop for inspection")
		return
	}
	defer unlock()
	machine, err := s.managedVirtualMachine(r.Context(), vmid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "desktop_not_found", "Managed desktop not found")
		} else {
			writeError(w, http.StatusServiceUnavailable, "recovery_inspection_unavailable", "Unable to verify managed desktop inventory")
		}
		return
	}
	if machine.Status != "running" {
		writeError(w, http.StatusConflict, "desktop_not_running", "Start the desktop before inspecting Guest state")
		return
	}
	accounts, err := s.store.NativeGuestAccountsForDesktop(r.Context(), vmid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to read account bindings")
		return
	}
	results := make([]nativeRecoveryReport, 0, len(accounts))
	for _, a := range accounts {
		report := nativeRecoveryReport{UserID: a.UserID, DatabaseRevision: a.Revision, Status: "inspection_unavailable"}
		lifecycle, err := s.nativeLifecycle(a)
		if err == nil {
			observed, inspectErr := lifecycle.InspectNativeAccount(r.Context(), machine, a.GuestUsername)
			if inspectErr == nil {
				report = compareNativeRecovery(a, observed)
			}
		}
		results = append(results, report)
	}
	s.auditRequest(r, session.User.ID, "desktop.native_recovery_inspected", "desktop", strconv.Itoa(vmid), map[string]any{"account_count": len(results)})
	writeJSON(w, http.StatusOK, map[string]any{"vmid": vmid, "accounts": results})
}
