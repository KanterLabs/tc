package httpapi

import (
	"net/http"
	"strings"

	"github.com/KanterLabs/helm/internal/auth"
)

// restoreTask is the guarded counterpart to DELETE /tasks/{id}. It resolves
// soft-deleted references before authorization, requires the deleted task
// version and an idempotency key, and returns the restored task snapshot so a
// browser Undo can reinsert it without a refresh.
func (s *Server) restoreTask(w http.ResponseWriter, r *http.Request, identity auth.Identity, reference string) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
		return
	}
	if !requireScope(w, identity, "tasks:write") {
		return
	}
	version, err := parseVersion(r)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if strings.TrimSpace(r.Header.Get("Idempotency-Key")) == "" {
		s.writeError(w, http.StatusBadRequest, "idempotency_required", "Idempotency-Key is required for task restore", nil)
		return
	}
	// Replays are checked before resolving the soft-deleted row. The original
	// response remains available even after a successful restore.
	if s.idempotencyReplay(w, r, identity) {
		return
	}

	current, _, err := s.Store.GetTaskForRestore(r.Context(), reference)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if !identity.CanProject(current.ProjectID) {
		s.writeError(w, http.StatusForbidden, "forbidden", "token is not scoped to this project", nil)
		return
	}

	s.mutation(w, r, identity, func() (int, []byte, string, error) {
		restored, restoreErr := s.Store.RestoreTask(r.Context(), current.ID, version, identity.Actor.ID)
		if restoreErr != nil {
			return 0, nil, "", restoreErr
		}
		body, marshalErr := marshalTaskForIdentity(identity, restored)
		if marshalErr != nil {
			return 0, nil, "", marshalErr
		}
		return http.StatusOK, body, taskETag(restored), nil
	})
}
