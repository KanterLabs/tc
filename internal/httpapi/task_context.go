package httpapi

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/KanterLabs/helm/internal/auth"
)

const maxTaskContextQueryBytes = 4_000

type taskContextRequest struct {
	Query *string `json:"query"`
}

func (s *Server) taskContext(w http.ResponseWriter, r *http.Request, identity auth.Identity, reference string) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
		return
	}
	if !requireScope(w, identity, "tasks:read") {
		return
	}
	project, err := s.Store.GetProject(r.Context(), reference)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if !identity.CanProject(project.ID) {
		s.writeError(w, http.StatusForbidden, "forbidden", "token is not scoped to this project", nil)
		return
	}
	var input taskContextRequest
	fields, err := decodeJSONObject(r, &input)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", "request body must contain only a query string", nil)
		return
	}
	if _, ok := fields["query"]; !ok || input.Query == nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", "query is required", nil)
		return
	}
	query := strings.TrimSpace(*input.Query)
	if query == "" || len(query) > maxTaskContextQueryBytes || !utf8.ValidString(query) {
		s.writeError(w, http.StatusBadRequest, "invalid_request", "query must be valid UTF-8 between 1 and 4000 bytes", nil)
		return
	}
	contextPack, err := s.Store.TaskDraftContext(r.Context(), project.ID, query)
	if err != nil {
		s.writeInternal(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, contextPack)
}
