package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/KanterLabs/helm/internal/auth"
	"github.com/KanterLabs/helm/internal/store"
)

// taskChecklist serves the public checklist collection and item resources.
// Checklist mutations advance the owning task version, so all writes use the
// same If-Match and idempotency contract as task/dependency mutations.
//
//	GET    /api/v1/tasks/{task}/checklist
//	POST   /api/v1/tasks/{task}/checklist
//	PATCH  /api/v1/tasks/{task}/checklist/{item}
//	DELETE /api/v1/tasks/{task}/checklist/{item}
func (s *Server) taskChecklist(w http.ResponseWriter, r *http.Request, identity auth.Identity, taskReference, itemReference string) {
	if itemReference == "" {
		switch r.Method {
		case http.MethodGet:
			s.getTaskChecklist(w, r, identity, taskReference)
		case http.MethodPost:
			s.addTaskChecklistItem(w, r, identity, taskReference)
		case http.MethodPatch:
			s.taskChecklistReorder(w, r, identity, taskReference)
		default:
			s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
		}
		return
	}
	switch r.Method {
	case http.MethodPatch:
		s.updateTaskChecklistItem(w, r, identity, taskReference, itemReference)
	case http.MethodDelete:
		s.deleteTaskChecklistItem(w, r, identity, taskReference, itemReference)
	default:
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
	}
}

func (s *Server) getTaskChecklist(w http.ResponseWriter, r *http.Request, identity auth.Identity, reference string) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
		return
	}
	if !requireScope(w, identity, "tasks:read") {
		return
	}
	task, err := s.Store.ResolveTaskReference(r.Context(), reference)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if !identity.CanProject(task.ProjectID) {
		s.writeError(w, http.StatusForbidden, "forbidden", "token is not scoped to this project", nil)
		return
	}
	// ResolveTaskReference already returns the authoritative checklist and task
	// version. Build the collection from that one snapshot so the body and ETag
	// cannot describe different versions when another writer races the read.
	checklist := store.ChecklistCollection{
		TaskID:  task.ID,
		Version: task.Version,
		Items:   task.Checklist,
		Summary: task.ChecklistSummary,
	}
	body, err := json.Marshal(checklist)
	if err != nil {
		s.writeInternal(w, err)
		return
	}
	s.writeRaw(w, http.StatusOK, body, taskETag(task))
}

func (s *Server) addTaskChecklistItem(w http.ResponseWriter, r *http.Request, identity auth.Identity, reference string) {
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
	if s.idempotencyReplay(w, r, identity) {
		return
	}
	task, err := s.resolveChecklistTask(r, identity, reference)
	if err != nil {
		s.writeStoreErrorForIdentity(w, identity, err)
		return
	}
	input, err := decodeChecklistItemInput(r, true)
	if err != nil {
		writeChecklistInputError(w, err)
		return
	}
	s.mutation(w, r, identity, func() (int, []byte, string, error) {
		updated, err := s.Store.AddTaskChecklistItemWithClaimOverride(r.Context(), task.ID, input, version, identity.Actor.ID, !identity.IsToken && identity.Actor.Admin)
		if err != nil {
			return 0, nil, "", err
		}
		body, err := marshalTaskForIdentity(identity, updated)
		if err != nil {
			return 0, nil, "", err
		}
		w.Header().Set("Location", "/api/v1/tasks/"+updated.ID+"/checklist")
		return http.StatusOK, body, taskETag(updated), nil
	})
}

func (s *Server) updateTaskChecklistItem(w http.ResponseWriter, r *http.Request, identity auth.Identity, taskReference, itemReference string) {
	if r.Method != http.MethodPatch {
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
	if s.idempotencyReplay(w, r, identity) {
		return
	}
	task, err := s.resolveChecklistTask(r, identity, taskReference)
	if err != nil {
		s.writeStoreErrorForIdentity(w, identity, err)
		return
	}
	input, err := decodeChecklistItemInput(r, false)
	if err != nil {
		writeChecklistInputError(w, err)
		return
	}
	s.mutation(w, r, identity, func() (int, []byte, string, error) {
		updated, err := s.Store.UpdateTaskChecklistItemWithClaimOverride(r.Context(), task.ID, itemReference, input, version, identity.Actor.ID, !identity.IsToken && identity.Actor.Admin)
		if err != nil {
			return 0, nil, "", err
		}
		body, err := marshalTaskForIdentity(identity, updated)
		if err != nil {
			return 0, nil, "", err
		}
		return http.StatusOK, body, taskETag(updated), nil
	})
}

func (s *Server) deleteTaskChecklistItem(w http.ResponseWriter, r *http.Request, identity auth.Identity, taskReference, itemReference string) {
	if r.Method != http.MethodDelete {
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
	if s.idempotencyReplay(w, r, identity) {
		return
	}
	task, err := s.resolveChecklistTask(r, identity, taskReference)
	if err != nil {
		s.writeStoreErrorForIdentity(w, identity, err)
		return
	}
	s.mutation(w, r, identity, func() (int, []byte, string, error) {
		updated, err := s.Store.DeleteTaskChecklistItemWithClaimOverride(r.Context(), task.ID, itemReference, version, identity.Actor.ID, !identity.IsToken && identity.Actor.Admin)
		if err != nil {
			return 0, nil, "", err
		}
		body, err := marshalTaskForIdentity(identity, updated)
		if err != nil {
			return 0, nil, "", err
		}
		return http.StatusOK, body, taskETag(updated), nil
	})
}

func (s *Server) taskChecklistReorder(w http.ResponseWriter, r *http.Request, identity auth.Identity, reference string) {
	if r.Method != http.MethodPatch && r.Method != http.MethodPost {
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
	if s.idempotencyReplay(w, r, identity) {
		return
	}
	task, err := s.resolveChecklistTask(r, identity, reference)
	if err != nil {
		s.writeStoreErrorForIdentity(w, identity, err)
		return
	}
	input, err := decodeChecklistReorderInput(r)
	if err != nil {
		writeChecklistInputError(w, err)
		return
	}
	s.mutation(w, r, identity, func() (int, []byte, string, error) {
		updated, err := s.Store.ReorderTaskChecklistWithClaimOverride(r.Context(), task.ID, input, version, identity.Actor.ID, !identity.IsToken && identity.Actor.Admin)
		if err != nil {
			return 0, nil, "", err
		}
		body, err := marshalTaskForIdentity(identity, updated)
		if err != nil {
			return 0, nil, "", err
		}
		return http.StatusOK, body, taskETag(updated), nil
	})
}

func (s *Server) resolveChecklistTask(r *http.Request, identity auth.Identity, reference string) (store.Task, error) {
	task, err := s.Store.ResolveTaskReference(r.Context(), reference)
	if err != nil {
		return store.Task{}, err
	}
	if !identity.CanProject(task.ProjectID) {
		return store.Task{}, &store.Error{Kind: store.ErrForbidden, Message: "token is not scoped to this project"}
	}
	return task, nil
}

type checklistItemRequest struct {
	Text      *string `json:"text"`
	Title     *string `json:"title"`
	Completed *bool   `json:"completed"`
	Position  *int    `json:"position"`
}

func decodeChecklistItemInput(r *http.Request, creating bool) (store.ChecklistItemInput, error) {
	var payload checklistItemRequest
	fields, err := decodeJSONObject(r, &payload)
	if err != nil {
		return store.ChecklistItemInput{}, err
	}
	if err := rejectJSONNull(fields, "text", "title", "completed", "position"); err != nil {
		return store.ChecklistItemInput{}, err
	}
	text := payload.Text
	if text == nil {
		text = payload.Title
	}
	input := store.ChecklistItemInput{Text: text, Completed: payload.Completed, Position: payload.Position}
	if creating && text == nil {
		return store.ChecklistItemInput{}, taskInputError("checklist item text is required")
	}
	if !creating && text == nil && payload.Completed == nil && payload.Position == nil {
		return store.ChecklistItemInput{}, taskInputError("checklist item patch must include at least one field")
	}
	return input, nil
}

type checklistReorderRequest struct {
	ItemIDs []string `json:"item_ids"`
	Items   []string `json:"items"`
	Order   []string `json:"order"`
}

func decodeChecklistReorderInput(r *http.Request) (store.ChecklistReorderInput, error) {
	var payload checklistReorderRequest
	fields, err := decodeJSONObject(r, &payload)
	if err != nil {
		return store.ChecklistReorderInput{}, err
	}
	if err := rejectJSONNull(fields, "item_ids", "items", "order"); err != nil {
		return store.ChecklistReorderInput{}, err
	}
	var ids []string
	supplied := 0
	for _, candidate := range []struct {
		name string
		ids  []string
	}{
		{"item_ids", payload.ItemIDs},
		{"items", payload.Items},
		{"order", payload.Order},
	} {
		if _, ok := fields[candidate.name]; !ok {
			continue
		}
		supplied++
		ids = candidate.ids
	}
	if supplied != 1 {
		return store.ChecklistReorderInput{}, taskInputError("exactly one of item_ids, items, or order is required")
	}
	return store.ChecklistReorderInput{ItemIDs: ids}, nil
}

func writeChecklistInputError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrInvalid) || errors.Is(err, store.ErrChecklistLimitExceeded) {
		// Keep all checklist validation errors in the public request envelope;
		// store limits are client-correctable and do not indicate server failure.
		if errors.Is(err, store.ErrChecklistLimitExceeded) {
			sDummyWriteError(w, http.StatusBadRequest, "checklist_limit_exceeded", err.Error(), nil)
		} else {
			sDummyWriteError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		}
		return
	}
	sDummyWriteError(w, http.StatusBadRequest, "invalid_json", "request body is invalid", nil)
}
