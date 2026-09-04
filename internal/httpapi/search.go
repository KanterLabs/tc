package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/KanterLabs/helm/internal/auth"
	"github.com/KanterLabs/helm/internal/store"
)

// searchCollection is intentionally task-shaped in data so existing API
// clients can consume global search exactly like a task collection. Projects
// and saved views are included as optional command-palette results without
// making a second uncached request for every keystroke.
type searchCollection struct {
	Data       []store.Task      `json:"data"`
	NextCursor string            `json:"next_cursor"`
	Projects   []store.Project   `json:"projects,omitempty"`
	Views      []store.SavedView `json:"views,omitempty"`
}

// search handles the global task search surface. It keeps project and view
// metadata in the same response for command search while applying the task
// project ceiling in the SQL query before pagination.
func (s *Server) search(w http.ResponseWriter, r *http.Request, identity auth.Identity, viewReference string) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
		return
	}
	if !requireScope(w, identity, "tasks:read") {
		return
	}
	filter, err := s.parseSearchFilter(r, identity, viewReference)
	if err != nil {
		if errors.Is(err, store.ErrForbidden) || errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrInvalid) {
			s.writeStoreError(w, err)
		} else {
			s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		}
		return
	}
	tasks, more, err := s.Store.ListSearchTasksWithExtra(r.Context(), filter)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	next := ""
	if more && len(tasks) > 0 {
		next = encodeCursor(filter.Cursor + len(tasks))
	}
	result := searchCollection{Data: tasks, NextCursor: next}
	// q is also useful for project and view navigation. Those records are
	// independently filtered by the same project ceiling; task pagination is
	// intentionally not affected by their count.
	if filter.Query != "" {
		q := strings.ToLower(strings.TrimSpace(filter.Query))
		projects, projectErr := s.Store.ListSearchProjects(r.Context(), filter.ProjectIDs, q)
		if projectErr != nil {
			s.writeInternal(w, projectErr)
			return
		}
		for _, project := range projects {
			result.Projects = append(result.Projects, project)
		}
		views, viewErr := s.listVisibleSavedViews(r, identity)
		if viewErr != nil {
			s.writeInternal(w, viewErr)
			return
		}
		for _, view := range views {
			if strings.Contains(strings.ToLower(view.Name), q) || strings.Contains(strings.ToLower(view.Description), q) {
				result.Views = append(result.Views, view)
			}
		}
		if result.Projects == nil {
			result.Projects = []store.Project{}
		}
		if result.Views == nil {
			result.Views = []store.SavedView{}
		}
	}
	s.writeJSON(w, http.StatusOK, result)
}

func (s *Server) parseSearchFilter(r *http.Request, identity auth.Identity, viewReference string) (store.SearchFilter, error) {
	limit, offset, err := parsePagination(r, 50)
	if err != nil {
		return store.SearchFilter{}, err
	}
	filter := store.SearchFilter{Limit: limit, Cursor: offset, ProjectIDs: scopedProjectIDs(identity)}
	if viewReference == "" {
		if reference, parseErr := parseOptionalIdentifier(r, "view"); parseErr != nil {
			return store.SearchFilter{}, parseErr
		} else if reference != "" {
			viewReference = reference
		}
	}
	if viewReference != "" {
		view, err := s.Store.GetSavedView(r.Context(), viewReference)
		if err != nil {
			return store.SearchFilter{}, err
		}
		if !s.savedViewVisible(r.Context(), identity, view) {
			return store.SearchFilter{}, &store.Error{Kind: store.ErrForbidden, Message: "view is not available to this actor"}
		}
		filter, err = s.searchFilterFromView(r, identity, view)
		if err != nil {
			return store.SearchFilter{}, err
		}
		filter.Cursor, filter.Limit = offset, limit
	}
	// Explicit query parameters override matching saved-view filters. This
	// makes a view URL useful as a starting point without mutating the view.
	if value, present, parseErr := searchTermValue(r, "q"); parseErr != nil {
		return store.SearchFilter{}, parseErr
	} else if present {
		filter.Query = value
	}
	for _, field := range []struct {
		name string
		dest *string
	}{
		{"key", &filter.Key}, {"title", &filter.Title}, {"description", &filter.Description},
		{"label", &filter.Label}, {"assignee", &filter.Assignee}, {"claim_owner", &filter.ClaimOwner}, {"claimed_by", &filter.ClaimOwner},
	} {
		value, present, parseErr := searchTermValue(r, field.name)
		if parseErr != nil {
			return store.SearchFilter{}, parseErr
		}
		if present {
			*field.dest = value
		}
	}
	if filter.State, err = overrideOptionalEnum(r, "state", semanticStates, filter.State); err != nil {
		return store.SearchFilter{}, err
	}
	if filter.Priority, err = overrideOptionalEnum(r, "priority", taskPriorities, filter.Priority); err != nil {
		return store.SearchFilter{}, err
	}
	if project, parseErr := parseOptionalIdentifier(r, "project"); parseErr != nil {
		return store.SearchFilter{}, parseErr
	} else if project != "" {
		resolved, resolveErr := s.Store.GetProject(r.Context(), project)
		if resolveErr != nil {
			return store.SearchFilter{}, resolveErr
		}
		if !identity.CanProject(resolved.ID) {
			return store.SearchFilter{}, &store.Error{Kind: store.ErrForbidden, Message: "token is not scoped to this project"}
		}
		filter.Project = resolved.ID
		filter.ProjectIDs = []string{resolved.ID}
	}
	for _, aliases := range [][2]string{{"due_from", "due_after"}, {"due_to", "due_before"}} {
		parsed, present, parseErr := optionalTimestampAliases(r, aliases[0], aliases[1])
		if parseErr != nil {
			return store.SearchFilter{}, parseErr
		}
		if present {
			if aliases[0] == "due_from" {
				filter.DueFrom = parsed
			} else {
				filter.DueTo = parsed
			}
		}
	}
	if filter.DueFrom != nil && filter.DueTo != nil && filter.DueFrom.After(*filter.DueTo) {
		return store.SearchFilter{}, errors.New("due_from must be before due_to")
	}
	if sortValue, present, parseErr := queryValue(r, "sort"); parseErr != nil {
		return store.SearchFilter{}, parseErr
	} else if present {
		filter.Sort, err = parseSearchSort(sortValue)
		if err != nil {
			return store.SearchFilter{}, err
		}
	}
	return filter, nil
}

func searchTermValue(r *http.Request, name string) (string, bool, error) {
	value, present, err := queryValue(r, name)
	if err != nil {
		return "", false, err
	}
	if !present {
		return "", false, nil
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false, fmt.Errorf("%s must not be empty", name)
	}
	if utf8.RuneCountInString(value) > 200 {
		return "", false, fmt.Errorf("%s is too long", name)
	}
	return value, true, nil
}

func overrideOptionalEnum(r *http.Request, name string, allowed map[string]struct{}, current string) (string, error) {
	value, present, err := queryValue(r, name)
	if err != nil {
		return "", err
	}
	if !present {
		return current, nil
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s must not be empty", name)
	}
	if _, ok := allowed[value]; !ok {
		return "", fmt.Errorf("%s is invalid", name)
	}
	return value, nil
}

func optionalTimestampAliases(r *http.Request, first, second string) (*time.Time, bool, error) {
	firstValue, firstPresent, err := queryValue(r, first)
	if err != nil {
		return nil, false, err
	}
	secondValue, secondPresent, err := queryValue(r, second)
	if err != nil {
		return nil, false, err
	}
	if firstPresent && secondPresent {
		return nil, false, fmt.Errorf("%s and %s cannot both be supplied", first, second)
	}
	if !firstPresent && !secondPresent {
		return nil, false, nil
	}
	name, value := first, firstValue
	if secondPresent {
		name, value = second, secondValue
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, false, fmt.Errorf("%s must be RFC3339", name)
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, false, fmt.Errorf("%s must be RFC3339", name)
	}
	return &parsed, true, nil
}

func parseSearchSort(value string) ([]store.SearchSort, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("sort must not be empty")
	}
	parts := strings.Split(value, ",")
	if len(parts) > 8 {
		return nil, errors.New("sort has too many fields")
	}
	result := make([]store.SearchSort, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, errors.New("sort contains an empty field")
		}
		pieces := strings.SplitN(part, ":", 2)
		field := strings.TrimSpace(pieces[0])
		direction := "asc"
		if len(pieces) == 2 {
			direction = strings.TrimSpace(pieces[1])
		}
		if direction != "asc" && direction != "desc" {
			return nil, errors.New("sort direction must be asc or desc")
		}
		result = append(result, store.SearchSort{Field: field, Direction: direction})
	}
	return result, nil
}

func (s *Server) searchFilterFromView(r *http.Request, identity auth.Identity, view store.SavedView) (store.SearchFilter, error) {
	filter := store.SearchFilter{ProjectIDs: scopedProjectIDs(identity), Sort: append([]store.SearchSort(nil), view.Sort...)}
	var explicitProjects []string
	explicitProjectsSet := false
	var dueFromKey, dueToKey string
	for key, raw := range view.Filters {
		canonicalKey := strings.ToLower(strings.TrimSpace(key))
		if !savedViewFilterKeys[canonicalKey] {
			return store.SearchFilter{}, fmt.Errorf("view filter %s is not supported", key)
		}
		if canonicalKey == "project_ids" || canonicalKey == "projects" {
			projects, err := s.resolveSavedViewProjects(r, identity, raw)
			if err != nil {
				return store.SearchFilter{}, err
			}
			if explicitProjectsSet {
				explicitProjects = intersectProjectIDs(explicitProjects, projects)
			} else {
				explicitProjects = projects
				explicitProjectsSet = true
			}
			continue
		}
		value, ok := raw.(string)
		if !ok {
			return store.SearchFilter{}, fmt.Errorf("view filter %s must be a string", key)
		}
		value, err := savedViewFilterValue(canonicalKey, value)
		if err != nil {
			return store.SearchFilter{}, err
		}
		switch canonicalKey {
		case "q", "query":
			filter.Query = value
		case "key":
			filter.Key = value
		case "title":
			filter.Title = value
		case "description":
			filter.Description = value
		case "label":
			filter.Label = value
		case "state":
			if _, ok := semanticStates[value]; !ok {
				return store.SearchFilter{}, fmt.Errorf("state is invalid")
			}
			filter.State = value
		case "priority":
			if _, ok := taskPriorities[value]; !ok {
				return store.SearchFilter{}, fmt.Errorf("priority is invalid")
			}
			filter.Priority = value
		case "assignee":
			filter.Assignee = value
		case "claim_owner", "claimed_by":
			filter.ClaimOwner = value
		case "project", "project_id":
			project, err := s.Store.GetProject(r.Context(), value)
			if err != nil {
				return store.SearchFilter{}, err
			}
			if !identity.CanProject(project.ID) {
				return store.SearchFilter{}, &store.Error{Kind: store.ErrForbidden, Message: "view is not available to this actor"}
			}
			filter.Project = project.ID
			if explicitProjectsSet {
				explicitProjects = intersectProjectIDs(explicitProjects, []string{project.ID})
			} else {
				explicitProjects = []string{project.ID}
				explicitProjectsSet = true
			}
		case "due_from", "due_after":
			parsed, err := time.Parse(time.RFC3339, value)
			if err != nil {
				return store.SearchFilter{}, fmt.Errorf("%s must be RFC3339", key)
			}
			if dueFromKey != "" {
				return store.SearchFilter{}, fmt.Errorf("%s and %s cannot both be supplied", dueFromKey, key)
			}
			dueFromKey = key
			filter.DueFrom = &parsed
		case "due_to", "due_before":
			parsed, err := time.Parse(time.RFC3339, value)
			if err != nil {
				return store.SearchFilter{}, fmt.Errorf("%s must be RFC3339", key)
			}
			if dueToKey != "" {
				return store.SearchFilter{}, fmt.Errorf("%s and %s cannot both be supplied", dueToKey, key)
			}
			dueToKey = key
			filter.DueTo = &parsed
		}
	}
	if explicitProjectsSet {
		if filter.ProjectIDs == nil {
			filter.ProjectIDs = explicitProjects
		} else {
			filter.ProjectIDs = intersectProjectIDs(filter.ProjectIDs, explicitProjects)
		}
	}
	if filter.DueFrom != nil && filter.DueTo != nil && filter.DueFrom.After(*filter.DueTo) {
		return store.SearchFilter{}, errors.New("due_from must be before due_to")
	}
	return filter, nil
}

var savedViewFilterKeys = map[string]bool{
	"q": true, "query": true, "key": true, "title": true, "description": true,
	"label": true, "state": true, "priority": true, "assignee": true,
	"claim_owner": true, "claimed_by": true, "project": true, "project_id": true,
	"project_ids": true, "projects": true, "due_from": true, "due_after": true,
	"due_to": true, "due_before": true,
}

func savedViewFilterValue(key, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("view filter %s must not be empty", key)
	}
	if utf8.RuneCountInString(value) > 200 {
		return "", fmt.Errorf("view filter %s is too long", key)
	}
	return value, nil
}

func (s *Server) resolveSavedViewProjects(r *http.Request, identity auth.Identity, raw any) ([]string, error) {
	values, ok := raw.([]any)
	if !ok {
		if strings, stringOK := raw.([]string); stringOK {
			values = make([]any, len(strings))
			for i := range strings {
				values[i] = strings[i]
			}
		} else {
			return nil, errors.New("view project_ids must be an array")
		}
	}
	if len(values) > 200 {
		return nil, errors.New("view project_ids has too many projects")
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, rawValue := range values {
		value, ok := rawValue.(string)
		if !ok {
			return nil, errors.New("view project_ids must contain identifiers")
		}
		value, err := savedViewFilterValue("project_id", value)
		if err != nil {
			return nil, err
		}
		project, err := s.Store.GetProject(r.Context(), value)
		if err != nil {
			return nil, err
		}
		if !identity.CanProject(project.ID) {
			return nil, &store.Error{Kind: store.ErrForbidden, Message: "view is not available to this actor"}
		}
		if _, ok := seen[project.ID]; ok {
			continue
		}
		seen[project.ID] = struct{}{}
		result = append(result, project.ID)
	}
	return result, nil
}

func intersectProjectIDs(left, right []string) []string {
	if left == nil {
		return append([]string(nil), right...)
	}
	if right == nil {
		return append([]string(nil), left...)
	}
	allowed := make(map[string]struct{}, len(right))
	for _, id := range right {
		allowed[id] = struct{}{}
	}
	result := make([]string, 0, len(left))
	for _, id := range left {
		if _, ok := allowed[id]; ok {
			result = append(result, id)
		}
	}
	return result
}

func (s *Server) listVisibleSavedViews(r *http.Request, identity auth.Identity) ([]store.SavedView, error) {
	result, _, _, err := s.listVisibleSavedViewsPage(r, identity, 200, 0)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(result, func(i, j int) bool {
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})
	return result, nil
}

const (
	// Saved-view visibility depends on JSON filters, so it cannot be pushed
	// into the base SQL query without duplicating the HTTP authorization rules.
	// Scan bounded datastore pages instead. The source offset is retained in
	// the returned cursor so hidden rows never consume an authorized page slot
	// or cause a visible row to be skipped.
	savedViewScanPageSize = 200
	savedViewScanBudget   = 2000
)

// listVisibleSavedViewsPage returns one page of the authorized saved-view
// stream. The datastore page is intentionally larger than the public page so
// a run of hidden shared views does not require one query per hidden row. If
// the bounded scan reaches its budget before filling the public page, the
// returned source offset remains resumable through next_cursor.
func (s *Server) listVisibleSavedViewsPage(r *http.Request, identity auth.Identity, limit, offset int) ([]store.SavedView, int, bool, error) {
	if limit <= 0 {
		limit = 50
	}
	visible := make([]store.SavedView, 0, limit)
	sourceOffset := offset
	scanned := 0
	for scanned < savedViewScanBudget {
		batchLimit := savedViewScanPageSize
		if remaining := savedViewScanBudget - scanned; remaining < batchLimit {
			batchLimit = remaining
		}
		views, sourceMore, err := s.Store.ListSavedViews(r.Context(), identity.Actor.ID, batchLimit, sourceOffset)
		if err != nil {
			return nil, sourceOffset, false, err
		}
		if len(views) == 0 {
			return visible, sourceOffset, false, nil
		}
		for index, view := range views {
			scanned++
			if !s.savedViewVisible(r.Context(), identity, view) {
				continue
			}
			visible = append(visible, view)
			if len(visible) < limit {
				continue
			}
			// Rows after the last returned visible view remain part of the
			// authorized stream, even when the datastore page has no sentinel.
			nextOffset := sourceOffset + index + 1
			return visible, nextOffset, index+1 < len(views) || sourceMore, nil
		}
		sourceOffset += len(views)
		if !sourceMore {
			return visible, sourceOffset, false, nil
		}
	}
	// A cursor is still returned when every scanned row was hidden. The caller
	// can safely resume the authorized stream without learning any row data.
	return visible, sourceOffset, true, nil
}

func (s *Server) savedViewVisible(ctx context.Context, identity auth.Identity, view store.SavedView) bool {
	if view.ActorID == identity.Actor.ID {
		return true
	}
	if !view.Shared {
		return false
	}
	if !identity.IsToken || !identity.Token.ProjectsScoped {
		return true
	}
	// Saved filters are JSON and therefore can contain harmless casing or
	// whitespace differences in keys. Normalize aliases here as the execution
	// path does, otherwise a scoped actor could receive the metadata for a
	// shared view whose project ceiling was hidden behind `Project_ID`.
	for key, raw := range view.Filters {
		canonicalKey := strings.ToLower(strings.TrimSpace(key))
		switch canonicalKey {
		case "project", "project_id":
			value, ok := raw.(string)
			if !ok {
				return false
			}
			project, err := s.Store.GetProject(ctx, value)
			if err != nil || !identity.CanProject(project.ID) {
				return false
			}
		case "project_ids", "projects":
			values, ok := raw.([]any)
			if !ok {
				if strings, stringOK := raw.([]string); stringOK {
					values = make([]any, len(strings))
					for i := range strings {
						values[i] = strings[i]
					}
				} else {
					return false
				}
			}
			for _, rawValue := range values {
				value, ok := rawValue.(string)
				if !ok {
					return false
				}
				project, err := s.Store.GetProject(ctx, value)
				if err != nil || !identity.CanProject(project.ID) {
					return false
				}
			}
		}
	}
	return true
}

type savedViewPayload struct {
	Name        *string         `json:"name"`
	Description *string         `json:"description"`
	Filters     json.RawMessage `json:"filters"`
	Sort        json.RawMessage `json:"sort"`
	Shared      *bool           `json:"shared"`
}

func decodeSavedViewInput(r *http.Request, creating bool) (store.SavedViewInput, error) {
	payload := savedViewPayload{}
	fields, err := decodeJSONObject(r, &payload)
	if err != nil {
		return store.SavedViewInput{}, err
	}
	if creating {
		if err := requireJSONFields(fields, "name", "filters"); err != nil {
			return store.SavedViewInput{}, err
		}
	}
	if err := rejectJSONNull(fields, "name", "description", "filters", "sort", "shared"); err != nil {
		return store.SavedViewInput{}, err
	}
	input := store.SavedViewInput{Name: payload.Name, Description: payload.Description, Shared: payload.Shared}
	if raw, ok := fields["filters"]; ok {
		var filters map[string]any
		if err := json.Unmarshal(raw, &filters); err != nil || filters == nil {
			return store.SavedViewInput{}, errors.New("filters must be a JSON object")
		}
		input.Filters, input.FiltersSet = filters, true
	}
	if raw, ok := fields["sort"]; ok {
		var terms []store.SearchSort
		if err := json.Unmarshal(raw, &terms); err != nil {
			// A single object is convenient for callers saving one sort field.
			var one store.SearchSort
			if objectErr := json.Unmarshal(raw, &one); objectErr != nil || one.Field == "" {
				return store.SavedViewInput{}, errors.New("sort must be an array or object")
			}
			terms = []store.SearchSort{one}
		}
		input.Sort, input.SortSet = terms, true
	}
	return input, nil
}

func (s *Server) savedViews(w http.ResponseWriter, r *http.Request, identity auth.Identity) {
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
		visible, nextOffset, more, err := s.listVisibleSavedViewsPage(r, identity, limit, offset)
		if err != nil {
			s.writeInternal(w, err)
			return
		}
		next := ""
		if more {
			next = encodeCursor(nextOffset)
		}
		s.writeCollection(w, visible, next)
	case http.MethodPost:
		if !requireScope(w, identity, "tasks:write") {
			return
		}
		if s.idempotencyReplay(w, r, identity) {
			return
		}
		input, err := decodeSavedViewInput(r, true)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_json", "request body is invalid", nil)
			return
		}
		if err := s.validateSavedViewProjectCeiling(r, identity, input.Filters); err != nil {
			s.writeStoreError(w, err)
			return
		}
		if err := s.validateSavedViewFilters(r, identity, input.Filters, input.Sort); err != nil {
			s.writeSavedViewValidationError(w, err)
			return
		}
		s.mutation(w, r, identity, func() (int, []byte, string, error) {
			view, createErr := s.Store.CreateSavedView(r.Context(), identity.Actor.ID, input)
			if createErr != nil {
				return 0, nil, "", createErr
			}
			body, marshalErr := json.Marshal(view)
			if marshalErr != nil {
				return 0, nil, "", marshalErr
			}
			w.Header().Set("Location", "/api/v1/views/"+view.ID)
			return http.StatusCreated, body, "", nil
		})
	default:
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
	}
}

func (s *Server) savedView(w http.ResponseWriter, r *http.Request, identity auth.Identity, id string) {
	switch r.Method {
	case http.MethodGet:
		if !requireScope(w, identity, "tasks:read") {
			return
		}
	case http.MethodPatch, http.MethodDelete:
		if !requireScope(w, identity, "tasks:write") {
			return
		}
	}
	view, err := s.Store.GetSavedView(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if !s.savedViewVisible(r.Context(), identity, view) {
		s.writeError(w, http.StatusNotFound, "not_found", "view not found", nil)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.writeJSON(w, http.StatusOK, view)
	case http.MethodPatch:
		if view.ActorID != identity.Actor.ID {
			s.writeError(w, http.StatusForbidden, "forbidden", "only the view owner can update it", nil)
			return
		}
		if s.idempotencyReplay(w, r, identity) {
			return
		}
		input, decodeErr := decodeSavedViewInput(r, false)
		if decodeErr != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_json", "request body is invalid", nil)
			return
		}
		if len(inputNameFields(input)) == 0 {
			s.writeError(w, http.StatusBadRequest, "invalid_request", "patch must include at least one view field", nil)
			return
		}
		if input.FiltersSet {
			if err := s.validateSavedViewProjectCeiling(r, identity, input.Filters); err != nil {
				s.writeStoreError(w, err)
				return
			}
			if err := s.validateSavedViewFilters(r, identity, input.Filters, input.Sort); err != nil {
				s.writeSavedViewValidationError(w, err)
				return
			}
		}
		if !input.FiltersSet && input.SortSet {
			if err := s.validateSavedViewFilters(r, identity, view.Filters, input.Sort); err != nil {
				s.writeSavedViewValidationError(w, err)
				return
			}
		}
		s.mutation(w, r, identity, func() (int, []byte, string, error) {
			updated, updateErr := s.Store.UpdateSavedView(r.Context(), id, identity.Actor.ID, input)
			if updateErr != nil {
				return 0, nil, "", updateErr
			}
			body, marshalErr := json.Marshal(updated)
			if marshalErr != nil {
				return 0, nil, "", marshalErr
			}
			return http.StatusOK, body, "", nil
		})
	case http.MethodDelete:
		if view.ActorID != identity.Actor.ID {
			s.writeError(w, http.StatusForbidden, "forbidden", "only the view owner can delete it", nil)
			return
		}
		if s.idempotencyReplay(w, r, identity) {
			return
		}
		s.mutation(w, r, identity, func() (int, []byte, string, error) {
			if deleteErr := s.Store.DeleteSavedView(r.Context(), id, identity.Actor.ID); deleteErr != nil {
				return 0, nil, "", deleteErr
			}
			return http.StatusNoContent, nil, "", nil
		})
	default:
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
	}
}

func inputNameFields(input store.SavedViewInput) []string {
	fields := make([]string, 0, 5)
	if input.Name != nil {
		fields = append(fields, "name")
	}
	if input.Description != nil {
		fields = append(fields, "description")
	}
	if input.FiltersSet {
		fields = append(fields, "filters")
	}
	if input.SortSet {
		fields = append(fields, "sort")
	}
	if input.Shared != nil {
		fields = append(fields, "shared")
	}
	return fields
}

func (s *Server) validateSavedViewProjectCeiling(r *http.Request, identity auth.Identity, filters map[string]any) error {
	if !identity.IsToken || !identity.Token.ProjectsScoped {
		return nil
	}
	for key, raw := range filters {
		canonicalKey := strings.ToLower(strings.TrimSpace(key))
		switch canonicalKey {
		case "project", "project_id":
			value, ok := raw.(string)
			if !ok || strings.TrimSpace(value) == "" {
				return &store.Error{Kind: store.ErrInvalid, Message: canonicalKey + " must be a project identifier"}
			}
			project, err := s.Store.GetProject(r.Context(), value)
			if err != nil {
				return err
			}
			if !identity.CanProject(project.ID) {
				return &store.Error{Kind: store.ErrForbidden, Message: "token is not scoped to this project"}
			}
		case "project_ids", "projects":
			values, ok := raw.([]any)
			if !ok {
				if strings, stringOK := raw.([]string); stringOK {
					values = make([]any, len(strings))
					for i := range strings {
						values[i] = strings[i]
					}
				} else {
					return &store.Error{Kind: store.ErrInvalid, Message: canonicalKey + " must be an array of project identifiers"}
				}
			}
			if len(values) > 200 {
				return &store.Error{Kind: store.ErrInvalid, Message: canonicalKey + " has too many projects"}
			}
			for _, rawValue := range values {
				value, ok := rawValue.(string)
				if !ok || strings.TrimSpace(value) == "" {
					return &store.Error{Kind: store.ErrInvalid, Message: canonicalKey + " must contain project identifiers"}
				}
				project, err := s.Store.GetProject(r.Context(), value)
				if err != nil {
					return err
				}
				if !identity.CanProject(project.ID) {
					return &store.Error{Kind: store.ErrForbidden, Message: "token is not scoped to this project"}
				}
			}
		}
	}
	return nil
}

func (s *Server) validateSavedViewFilters(r *http.Request, identity auth.Identity, filters map[string]any, sortTerms []store.SearchSort) error {
	view := store.SavedView{Filters: filters, Sort: sortTerms}
	_, err := s.searchFilterFromView(r, identity, view)
	return err
}

func (s *Server) writeSavedViewValidationError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrForbidden) || errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrInvalid) {
		s.writeStoreError(w, err)
		return
	}
	s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
}
