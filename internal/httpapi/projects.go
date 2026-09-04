package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/KanterLabs/helm/internal/auth"
	"github.com/KanterLabs/helm/internal/store"
)

func (s *Server) projects(w http.ResponseWriter, r *http.Request, identity auth.Identity) {
	switch r.Method {
	case http.MethodGet:
		if !requireScope(w, identity, "projects:read") {
			return
		}
		limit, offset, err := parsePagination(r, 50)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
			return
		}
		includeArchived, err := parseOptionalBool(r, "archived")
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
			return
		}
		favoriteOnly, err := parseOptionalBool(r, "favorite")
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
			return
		}
		projects, err := s.Store.ListProjectsFilteredWithExtra(r.Context(), limit+1, offset, store.ProjectFilter{IncludeArchived: includeArchived, FavoriteOnly: favoriteOnly, ProjectIDs: scopedProjectIDs(identity)})
		if err != nil {
			s.writeInternal(w, err)
			return
		}
		hasMore := len(projects) > limit
		if hasMore {
			projects = projects[:limit]
		}
		next := ""
		if hasMore {
			next = encodeCursor(offset + len(projects))
		}
		s.writeCollection(w, projects, next)
	case http.MethodPost:
		if !requireScope(w, identity, "projects:write") {
			return
		}
		// A project-scoped token may manage only its existing allow-list. It
		// cannot authorize a brand-new project whose ID is necessarily outside
		// that ceiling; an unscoped projects:write token remains able to create.
		if identity.IsToken && identity.Token.ProjectsScoped {
			s.writeError(w, http.StatusForbidden, "forbidden", "project-scoped tokens cannot create projects", nil)
			return
		}
		if s.idempotencyReplay(w, r, identity) {
			return
		}
		var payload struct {
			Key                       *string `json:"key"`
			Slug                      *string `json:"slug"`
			Name                      *string `json:"name"`
			Description               *string `json:"description"`
			Color                     *string `json:"color"`
			Favorite                  *bool   `json:"favorite"`
			ChecklistCompletionPolicy *string `json:"checklist_completion_policy"`
		}
		fields, err := decodeJSONObject(r, &payload)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_json", "request body is invalid", nil)
			return
		}
		if err := requireJSONFields(fields, "key", "name"); err != nil {
			s.writeStoreError(w, err)
			return
		}
		if err := rejectJSONNull(fields, "key", "slug", "name", "description", "color", "favorite", "checklist_completion_policy"); err != nil {
			s.writeStoreError(w, err)
			return
		}
		if _, present := fields["color"]; present {
			if payload.Color == nil || !validHTTPColor(*payload.Color) {
				s.writeStoreError(w, taskInputError("color must be a six-digit hexadecimal value"))
				return
			}
		}
		projectInput := store.ProjectInput{Key: payload.Key, Slug: payload.Slug, Name: payload.Name, Description: payload.Description, Color: payload.Color, Favorite: payload.Favorite, ChecklistCompletionPolicy: payload.ChecklistCompletionPolicy}
		s.mutation(w, r, identity, func() (int, []byte, string, error) {
			project, err := s.Store.CreateProject(r.Context(), projectInput, identity.Actor.ID)
			if err != nil {
				return 0, nil, "", err
			}
			w.Header().Set("Location", "/api/v1/projects/"+project.ID)
			body, err := marshalProjectForIdentity(identity, project)
			if err != nil {
				return 0, nil, "", err
			}
			return http.StatusCreated, body, projectETag(project.Version), nil
		})
	default:
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
	}
}

// scopedProjectIDs returns the effective allow-list for a bearer identity in
// stable order. A nil result means the identity is not project-scoped; a
// non-nil empty result intentionally matches no rows in SQL.
func scopedProjectIDs(identity auth.Identity) []string {
	if !identity.IsToken || !identity.Token.ProjectsScoped {
		return nil
	}
	result := make([]string, 0, len(identity.Token.Projects))
	for projectID := range identity.Token.Projects {
		result = append(result, projectID)
	}
	sort.Strings(result)
	return result
}

func (s *Server) project(w http.ResponseWriter, r *http.Request, identity auth.Identity, reference string) {
	if r.Method == http.MethodPatch {
		if !requireScope(w, identity, "projects:write") {
			return
		}
		if s.idempotencyReplay(w, r, identity) {
			return
		}
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
	switch r.Method {
	case http.MethodGet:
		if !requireScope(w, identity, "projects:read") {
			return
		}
		w.Header().Set("ETag", projectETag(project.Version))
		s.writeJSON(w, http.StatusOK, project)
	case http.MethodPatch:
		var payload struct {
			Key                       *string `json:"key"`
			Slug                      *string `json:"slug"`
			Name                      *string `json:"name"`
			Description               *string `json:"description"`
			Color                     *string `json:"color"`
			Favorite                  *bool   `json:"favorite"`
			Archived                  *bool   `json:"archived"`
			ChecklistCompletionPolicy *string `json:"checklist_completion_policy"`
		}
		fields, err := decodeJSONObject(r, &payload)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_json", "request body is invalid", nil)
			return
		}
		if err := rejectJSONNull(fields, "key", "slug", "name", "description", "color", "favorite", "archived", "checklist_completion_policy"); err != nil {
			s.writeStoreError(w, err)
			return
		}
		if _, present := fields["color"]; present {
			if payload.Color == nil || !validHTTPColor(*payload.Color) {
				s.writeStoreError(w, taskInputError("color must be a six-digit hexadecimal value"))
				return
			}
		}
		if payload.Key == nil && payload.Slug == nil && payload.Name == nil && payload.Description == nil && payload.Color == nil && payload.Favorite == nil && payload.Archived == nil && payload.ChecklistCompletionPolicy == nil {
			s.writeError(w, http.StatusBadRequest, "invalid_request", "patch must include at least one project field", nil)
			return
		}
		input := store.ProjectInput{Key: payload.Key, Slug: payload.Slug, Name: payload.Name, Description: payload.Description, Color: payload.Color, Favorite: payload.Favorite, Archived: payload.Archived, ChecklistCompletionPolicy: payload.ChecklistCompletionPolicy}
		var expected *int64
		if strings.TrimSpace(r.Header.Get("If-Match")) != "" {
			version, versionErr := parseVersion(r)
			if versionErr != nil {
				s.writeStoreError(w, versionErr)
				return
			}
			expected = &version
		}
		s.mutation(w, r, identity, func() (int, []byte, string, error) {
			updated, err := s.Store.UpdateProjectWithVersion(r.Context(), project.ID, input, identity.Actor.ID, expected)
			if err != nil {
				return 0, nil, "", err
			}
			body, err := marshalProjectForIdentity(identity, updated)
			if err != nil {
				return 0, nil, "", err
			}
			return http.StatusOK, body, projectETag(updated.Version), nil
		})
	default:
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
	}
}

func (s *Server) columns(w http.ResponseWriter, r *http.Request, identity auth.Identity, reference string) {
	if r.Method == http.MethodPost {
		if !requireScope(w, identity, "projects:write") {
			return
		}
		if s.idempotencyReplay(w, r, identity) {
			return
		}
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
	switch r.Method {
	case http.MethodGet:
		if !requireScope(w, identity, "projects:read") {
			return
		}
		limit, offset, err := parsePagination(r, 50)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
			return
		}
		includeArchived, err := parseOptionalBool(r, "archived")
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
			return
		}
		columns, more, err := s.Store.ListColumnsPageFiltered(r.Context(), project.ID, limit, offset, includeArchived)
		if err != nil {
			s.writeInternal(w, err)
			return
		}
		next := ""
		if more && len(columns) > 0 {
			next = encodeCursor(offset + len(columns))
		}
		s.writeCollection(w, columns, next)
	case http.MethodPost:
		var payload struct {
			Name          *string `json:"name"`
			SemanticState *string `json:"semantic_state"`
			Position      *int    `json:"position"`
		}
		fields, err := decodeJSONObject(r, &payload)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_json", "request body is invalid", nil)
			return
		}
		if err := requireJSONFields(fields, "name", "semantic_state"); err != nil {
			s.writeStoreError(w, err)
			return
		}
		if err := rejectJSONNull(fields, "name", "semantic_state", "position"); err != nil {
			s.writeStoreError(w, err)
			return
		}
		input := store.ColumnInput{Name: payload.Name, SemanticState: payload.SemanticState, Position: payload.Position}
		s.mutation(w, r, identity, func() (int, []byte, string, error) {
			column, err := s.Store.CreateColumn(r.Context(), project.ID, input, identity.Actor.ID)
			if err != nil {
				return 0, nil, "", err
			}
			w.Header().Set("Location", "/api/v1/columns/"+column.ID)
			body, err := marshalColumnForIdentity(identity, column)
			if err != nil {
				return 0, nil, "", err
			}
			return http.StatusCreated, body, columnETag(column.Version), nil
		})
	default:
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
	}
}

func (s *Server) column(w http.ResponseWriter, r *http.Request, identity auth.Identity, id string) {
	if r.Method == http.MethodPatch {
		if !requireScope(w, identity, "projects:write") {
			return
		}
		if s.idempotencyReplay(w, r, identity) {
			return
		}
	}
	column, err := s.Store.GetColumn(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if !identity.CanProject(column.ProjectID) {
		s.writeError(w, http.StatusForbidden, "forbidden", "token is not scoped to this project", nil)
		return
	}
	if r.Method != http.MethodPatch {
		if r.Method == http.MethodGet {
			if !requireScope(w, identity, "projects:read") {
				return
			}
			w.Header().Set("ETag", columnETag(column.Version))
			s.writeJSON(w, http.StatusOK, column)
			return
		}
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
		return
	}
	var payload struct {
		Name          *string `json:"name"`
		SemanticState *string `json:"semantic_state"`
		Position      *int    `json:"position"`
		Archived      *bool   `json:"archived"`
	}
	fields, err := decodeJSONObject(r, &payload)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_json", "request body is invalid", nil)
		return
	}
	if err := rejectJSONNull(fields, "name", "semantic_state", "position", "archived"); err != nil {
		s.writeStoreError(w, err)
		return
	}
	if payload.Name == nil && payload.SemanticState == nil && payload.Position == nil && payload.Archived == nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", "patch must include at least one column field", nil)
		return
	}
	input := store.ColumnInput{Name: payload.Name, SemanticState: payload.SemanticState, Position: payload.Position, Archived: payload.Archived}
	var expected *int64
	if strings.TrimSpace(r.Header.Get("If-Match")) != "" {
		version, versionErr := parseVersion(r)
		if versionErr != nil {
			s.writeStoreError(w, versionErr)
			return
		}
		expected = &version
	}
	s.mutation(w, r, identity, func() (int, []byte, string, error) {
		updated, err := s.Store.UpdateColumnWithVersion(r.Context(), id, input, identity.Actor.ID, !identity.IsToken && identity.Actor.Admin, expected)
		if err != nil {
			return 0, nil, "", err
		}
		body, err := marshalColumnForIdentity(identity, updated)
		if err != nil {
			return 0, nil, "", err
		}
		return http.StatusOK, body, columnETag(updated.Version), nil
	})
}

func (s *Server) labels(w http.ResponseWriter, r *http.Request, identity auth.Identity, reference string) {
	if r.Method == http.MethodPost {
		if !requireScope(w, identity, "projects:write") {
			return
		}
		if s.idempotencyReplay(w, r, identity) {
			return
		}
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
	switch r.Method {
	case http.MethodGet:
		if !requireScope(w, identity, "tasks:read") {
			return
		}
		limit, offset, err := parsePagination(r, 50)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
			return
		}
		labels, more, err := s.Store.ListLabelsPage(r.Context(), project.ID, limit, offset)
		if err != nil {
			s.writeInternal(w, err)
			return
		}
		next := ""
		if more && len(labels) > 0 {
			next = encodeCursor(offset + len(labels))
		}
		s.writeCollection(w, labels, next)
	case http.MethodPost:
		var payload store.LabelInput
		fields, err := decodeJSONObject(r, &payload)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_json", "request body is invalid", nil)
			return
		}
		if err := requireJSONFields(fields, "name"); err != nil {
			s.writeStoreError(w, err)
			return
		}
		if err := rejectJSONNull(fields, "name"); err != nil {
			s.writeStoreError(w, err)
			return
		}
		if raw, present := fields["color"]; present && !isJSONNull(raw) {
			if !validHTTPColor(payload.Color) {
				s.writeStoreError(w, taskInputError("color must be a six-digit hexadecimal value"))
				return
			}
		}
		s.mutation(w, r, identity, func() (int, []byte, string, error) {
			label, err := s.Store.CreateLabel(r.Context(), project.ID, payload, identity.Actor.ID)
			if err != nil {
				return 0, nil, "", err
			}
			w.Header().Set("Location", "/api/v1/labels/"+label.ID)
			body, _ := json.Marshal(label)
			return http.StatusCreated, body, "", nil
		})
	default:
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
	}
}

func (s *Server) deleteLabel(w http.ResponseWriter, r *http.Request, identity auth.Identity, id string) {
	if !requireScope(w, identity, "projects:write") {
		return
	}
	if s.idempotencyReplay(w, r, identity) {
		return
	}
	labelProject, err := s.labelProject(r, id)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if !identity.CanProject(labelProject) {
		s.writeError(w, http.StatusForbidden, "forbidden", "token is not scoped to this project", nil)
		return
	}
	s.mutation(w, r, identity, func() (int, []byte, string, error) {
		err := s.Store.DeleteLabel(r.Context(), id, identity.Actor.ID)
		if err != nil {
			return 0, nil, "", err
		}
		return http.StatusNoContent, nil, "", nil
	})
}

func (s *Server) labelProject(r *http.Request, id string) (string, error) {
	var projectID string
	err := s.Store.DB.QueryRowContext(r.Context(), `SELECT project_id FROM labels WHERE id=?`, id).Scan(&projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", store.ErrNotFound
	}
	return projectID, err
}

func validHTTPColor(value string) bool {
	if len(value) != 7 || value[0] != '#' {
		return false
	}
	for _, char := range value[1:] {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return false
		}
	}
	return true
}

func projectETag(version int64) string { return `"v` + strconv.FormatInt(version, 10) + `"` }

func columnETag(version int64) string { return `"v` + strconv.FormatInt(version, 10) + `"` }

// marshalProjectForIdentity keeps successful project mutations least-privilege.
// A bearer credential with projects:write but without projects:read may still
// update a project, but must not receive its private fields in the mutation
// response. Humans and read-capable bearer credentials retain the full object.
func marshalProjectForIdentity(identity auth.Identity, project store.Project) ([]byte, error) {
	if identity.IsToken && !identity.HasScope("projects:read") {
		return json.Marshal(map[string]any{"id": project.ID})
	}
	return json.Marshal(project)
}

// marshalColumnForIdentity applies the same response boundary to column
// mutations. project_id is retained so a scoped writer can correlate the
// newly changed column without exposing its name, state, or timestamps.
func marshalColumnForIdentity(identity auth.Identity, column store.Column) ([]byte, error) {
	if identity.IsToken && !identity.HasScope("projects:read") {
		return json.Marshal(map[string]any{"id": column.ID, "project_id": column.ProjectID})
	}
	return json.Marshal(column)
}
