package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Veritas-Calculus/vc-workspace/internal/store"
)

// Call only after authorizing the target, including on replays. Keys are scoped
// to a principal and endpoint; request fingerprints never contain plaintext.
func (s *Server) prepareJobRequest(w http.ResponseWriter, r *http.Request, principal string) (key, fingerprint string, proceed bool) {
	rawKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(rawKey) < 8 || len(rawKey) > 128 {
		writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key must be 8–128 characters")
		return
	}
	if r.Body == nil {
		r.Body = http.NoBody
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024+1))
	if err != nil || len(body) > 1024*1024 {
		writeError(w, http.StatusBadRequest, "invalid_request", "Request body is invalid or too large")
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	canonical := bytes.TrimSpace(body)
	if len(canonical) > 0 {
		var value any
		decoder := json.NewDecoder(bytes.NewReader(canonical))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil || !json.Valid(canonical) {
			writeError(w, http.StatusBadRequest, "invalid_request", "Request body must be valid JSON")
			return
		}
		canonical, _ = json.Marshal(value)
	}
	namespace, _ := json.Marshal([]string{principal, r.Method, r.URL.EscapedPath(), rawKey})
	key = fmt.Sprintf("sha256:%x", sha256.Sum256(namespace))
	fingerprint = fmt.Sprintf("sha256:%x", sha256.Sum256(canonical))
	existing, err := s.store.JobForRequest(r.Context(), key, fingerprint)
	switch {
	case err == nil:
		writeJSON(w, http.StatusAccepted, existing)
	case errors.Is(err, store.ErrNotFound):
		// Pre-upgrade jobs used a global raw key with no request fingerprint.
		// They cannot be safely replayed or repeated after namespacing the key.
		// Return only a conflict, never the legacy job's potentially private data.
		if _, legacyErr := s.store.JobByIdempotencyKey(r.Context(), rawKey); legacyErr == nil {
			writeJobConflict(w)
			return
		} else if !errors.Is(legacyErr, store.ErrNotFound) {
			writeError(w, http.StatusInternalServerError, "internal_error", "Unable to inspect existing request")
			return
		}
		proceed = true
	case errors.Is(err, store.ErrConflict):
		writeJobConflict(w)
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to inspect existing request")
	}
	return
}

func writeJobConflict(w http.ResponseWriter) {
	writeError(w, http.StatusConflict, "idempotency_key_conflict", "This idempotency key was already used for a different request")
}
