package httpapi

import (
	"net/http"
	"strings"

	"github.com/KanterLabs/helm/internal/auth"
)

// taskReorder handles precise board placement. It intentionally shares the
// guarded request shape with /move, but has its own route so audit/apply
// clients retain the legacy append-only semantics of /move when they omit
// anchors.
func (s *Server) taskReorder(w http.ResponseWriter, r *http.Request, identity auth.Identity, reference string) {
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
		s.writeError(w, http.StatusBadRequest, "idempotency_required", "Idempotency-Key is required for task reorders", nil)
		return
	}
	// Check replays before resolving the task or decoding anchors. A retry must
	// return the original response even if the board has since changed.
	if s.idempotencyReplay(w, r, identity) {
		return
	}

	current, err := s.Store.ResolveTaskReference(r.Context(), reference)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if !identity.CanProject(current.ProjectID) {
		s.writeError(w, http.StatusForbidden, "forbidden", "token is not scoped to this project", nil)
		return
	}
	input, err := decodeTaskMoveInput(r)
	if err != nil {
		writeTaskInputError(w, err)
		return
	}

	s.mutation(w, r, identity, func() (int, []byte, string, error) {
		updated, reorderErr := s.Store.ReorderTask(r.Context(), current.ID, input, version, identity.Actor.ID)
		if reorderErr != nil {
			return 0, nil, "", reorderErr
		}
		body, marshalErr := marshalTaskForIdentity(identity, updated)
		if marshalErr != nil {
			return 0, nil, "", marshalErr
		}
		return http.StatusOK, body, taskETag(updated), nil
	})
}
