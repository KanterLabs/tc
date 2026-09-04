package httpapi

import (
	"net/http"

	"github.com/KanterLabs/helm/internal/auth"
)

// boardDescriptor is intentionally a virtual view over today's project. The
// v1 decision is one board per project; exposing the stable project ID here
// gives navigation and permission-aware clients a board seam without changing
// existing /projects/{ref} URLs or introducing a speculative migration.
type boardDescriptor struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	Slug      string `json:"slug"`
	Default   bool   `json:"default"`
	Enabled   bool   `json:"enabled"`
}

func (s *Server) boards(w http.ResponseWriter, r *http.Request, identity auth.Identity, reference string) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
		return
	}
	if !requireScope(w, identity, "projects:read") {
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
	board := boardDescriptor{ID: project.ID, ProjectID: project.ID, Name: project.Name, Slug: project.Slug, Default: true, Enabled: true}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"data":        []boardDescriptor{board},
		"next_cursor": "",
		"multi_board": map[string]any{
			"enabled":            false,
			"decision":           "deferred",
			"migration_required": true,
			"url_compatibility":  "existing project URLs remain canonical",
		},
	})
}
