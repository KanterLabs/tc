package httpapi

import (
	"net/http"
	"time"

	"github.com/KanterLabs/helm/internal/auth"
)

const reopenedIssueWindowDays = 7

type issueMetricsResponse struct {
	Reopened   int    `json:"reopened"`
	WindowDays int    `json:"window_days"`
	Since      string `json:"since"`
	AsOf       string `json:"as_of"`
}

type sidebarCountsResponse struct {
	Issues int    `json:"issues"`
	MyWork int    `json:"my_work"`
	View   string `json:"view"`
}

// issueMetrics serves bounded, aggregate issue health data. Reopened is the
// number of distinct live bug tasks with a bug.reopened event in the last
// seven UTC days, inclusive of both interval boundaries. A project-scoped
// bearer token may aggregate only its permitted project ceiling; an optional
// project narrows that ceiling to one permitted project.
func (s *Server) issueMetrics(w http.ResponseWriter, r *http.Request, identity auth.Identity) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
		return
	}
	if !requireScope(w, identity, "tasks:read") {
		return
	}
	projectIDs, ok := s.metricProjectIDs(w, r, identity)
	if !ok {
		return
	}
	asOf := time.Now().UTC()
	since := asOf.Add(-reopenedIssueWindowDays * 24 * time.Hour)
	reopened, err := s.Store.CountReopenedIssues(r.Context(), projectIDs, since, asOf)
	if err != nil {
		s.writeInternal(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, issueMetricsResponse{
		Reopened:   reopened,
		WindowDays: reopenedIssueWindowDays,
		Since:      since.Format(time.RFC3339Nano),
		AsOf:       asOf.Format(time.RFC3339Nano),
	})
}

// sidebarCounts returns only the scalar values needed by the primary
// navigation. It intentionally avoids task rows and aggregates across the
// same effective project ceiling as issueMetrics.
func (s *Server) sidebarCounts(w http.ResponseWriter, r *http.Request, identity auth.Identity) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
		return
	}
	if !requireScope(w, identity, "tasks:read") {
		return
	}
	view, err := parseOptionalEnum(r, "view", map[string]struct{}{"assigned": {}, "live": {}})
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	if view == "" {
		view = "live"
	}
	projectIDs, ok := s.metricProjectIDs(w, r, identity)
	if !ok {
		return
	}
	issues, err := s.Store.CountIssues(r.Context(), projectIDs)
	if err != nil {
		s.writeInternal(w, err)
		return
	}
	myWork, err := s.Store.CountMyWork(r.Context(), identity.Actor.ID, projectIDs, view == "live")
	if err != nil {
		s.writeInternal(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, sidebarCountsResponse{Issues: issues, MyWork: myWork, View: view})
}

func (s *Server) metricProjectIDs(w http.ResponseWriter, r *http.Request, identity auth.Identity) ([]string, bool) {
	if reference, err := parseOptionalIdentifier(r, "project"); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return nil, false
	} else if reference != "" {
		project, err := s.Store.GetProject(r.Context(), reference)
		if err != nil {
			s.writeStoreError(w, err)
			return nil, false
		}
		if !identity.CanProject(project.ID) {
			s.writeError(w, http.StatusForbidden, "forbidden", "token is not scoped to this project", nil)
			return nil, false
		}
		return []string{project.ID}, true
	}
	return scopedProjectIDs(identity), true
}
