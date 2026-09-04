package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/KanterLabs/helm/internal/auth"
	"github.com/KanterLabs/helm/internal/store"
)

// taskHierarchy exposes the bounded hierarchy reads and the parent-edge
// mutations. The task in the URL is always resolved and authorized before a
// relation is returned or changed; the store repeats same-project and graph
// validation inside its serialized mutation transaction.
//
//	GET    /api/v1/tasks/{task}/hierarchy
//	GET    /api/v1/tasks/{task}/children
//	POST   /api/v1/tasks/{parent}/children
//	DELETE /api/v1/tasks/{parent}/children/{child}
//	GET    /api/v1/tasks/{task}/ancestors
//	GET    /api/v1/tasks/{task}/descendants
//	GET    /api/v1/tasks/{task}/parent
//	POST   /api/v1/tasks/{child}/parent
//	DELETE /api/v1/tasks/{child}/parent
func (s *Server) taskHierarchy(w http.ResponseWriter, r *http.Request, identity auth.Identity, reference, relation, childReference string) {
	switch relation {
	case "hierarchy":
		if r.Method != http.MethodGet {
			s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
			return
		}
		s.getTaskHierarchy(w, r, identity, reference)
	case "children":
		if childReference != "" {
			if r.Method != http.MethodDelete {
				s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
				return
			}
			s.removeTaskChild(w, r, identity, reference, childReference)
			return
		}
		switch r.Method {
		case http.MethodGet:
			s.getTaskChildren(w, r, identity, reference)
		case http.MethodPost:
			s.addTaskChild(w, r, identity, reference)
		default:
			s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
		}
	case "ancestors":
		if r.Method != http.MethodGet {
			s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
			return
		}
		s.getTaskAncestors(w, r, identity, reference)
	case "descendants":
		if r.Method != http.MethodGet {
			s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
			return
		}
		s.getTaskDescendants(w, r, identity, reference)
	case "parent":
		s.taskParent(w, r, identity, reference)
	default:
		s.writeError(w, http.StatusNotFound, "not_found", "route not found", nil)
	}
}

func (s *Server) getTaskHierarchy(w http.ResponseWriter, r *http.Request, identity auth.Identity, reference string) {
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
	hierarchy, err := s.Store.GetTaskHierarchy(r.Context(), task.ID)
	if err != nil {
		s.writeStoreErrorForIdentity(w, identity, err)
		return
	}
	body, err := json.Marshal(hierarchy)
	if err != nil {
		s.writeInternal(w, err)
		return
	}
	s.writeRaw(w, http.StatusOK, body, taskETag(task))
}

func (s *Server) getTaskChildren(w http.ResponseWriter, r *http.Request, identity auth.Identity, reference string) {
	if !requireScope(w, identity, "tasks:read") {
		return
	}
	task, err := s.authorizedHierarchyTask(r, identity, reference)
	if err != nil {
		s.writeStoreErrorForIdentity(w, identity, err)
		return
	}
	children, err := s.Store.ListTaskChildren(r.Context(), task.ID)
	if err != nil {
		s.writeStoreErrorForIdentity(w, identity, err)
		return
	}
	s.writeCollection(w, children, "")
}

func (s *Server) getTaskAncestors(w http.ResponseWriter, r *http.Request, identity auth.Identity, reference string) {
	if !requireScope(w, identity, "tasks:read") {
		return
	}
	task, err := s.authorizedHierarchyTask(r, identity, reference)
	if err != nil {
		s.writeStoreErrorForIdentity(w, identity, err)
		return
	}
	ancestors, err := s.Store.ListTaskAncestors(r.Context(), task.ID)
	if err != nil {
		s.writeStoreErrorForIdentity(w, identity, err)
		return
	}
	s.writeCollection(w, ancestors, "")
}

func (s *Server) getTaskDescendants(w http.ResponseWriter, r *http.Request, identity auth.Identity, reference string) {
	if !requireScope(w, identity, "tasks:read") {
		return
	}
	task, err := s.authorizedHierarchyTask(r, identity, reference)
	if err != nil {
		s.writeStoreErrorForIdentity(w, identity, err)
		return
	}
	descendants, err := s.Store.ListTaskDescendants(r.Context(), task.ID)
	if err != nil {
		s.writeStoreErrorForIdentity(w, identity, err)
		return
	}
	s.writeCollection(w, descendants, "")
}

func (s *Server) taskParent(w http.ResponseWriter, r *http.Request, identity auth.Identity, reference string) {
	switch r.Method {
	case http.MethodGet:
		if !requireScope(w, identity, "tasks:read") {
			return
		}
		task, err := s.authorizedHierarchyTask(r, identity, reference)
		if err != nil {
			s.writeStoreErrorForIdentity(w, identity, err)
			return
		}
		hierarchy, err := s.Store.GetTaskHierarchy(r.Context(), task.ID)
		if err != nil {
			s.writeStoreErrorForIdentity(w, identity, err)
			return
		}
		body, err := json.Marshal(map[string]any{"parent": hierarchy.Parent})
		if err != nil {
			s.writeInternal(w, err)
			return
		}
		s.writeRaw(w, http.StatusOK, body, taskETag(task))
	case http.MethodPost:
		s.setTaskParentHTTP(w, r, identity, reference)
	case http.MethodDelete:
		s.clearTaskParentHTTP(w, r, identity, reference)
	default:
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
	}
}

func (s *Server) addTaskChild(w http.ResponseWriter, r *http.Request, identity auth.Identity, parentReference string) {
	if !requireScope(w, identity, "tasks:write") {
		return
	}
	version, err := parseVersion(r)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if !requireHierarchyIdempotencyKey(w, r) || s.idempotencyReplay(w, r, identity) {
		return
	}
	parent, err := s.authorizedHierarchyTask(r, identity, parentReference)
	if err != nil {
		s.writeStoreErrorForIdentity(w, identity, err)
		return
	}
	childReference, err := decodeHierarchyReference(r, []string{"child", "child_id", "child_task_id"}, "child")
	if err != nil {
		writeHierarchyInputError(w, err)
		return
	}
	child, err := s.Store.ResolveTaskReference(r.Context(), childReference)
	if err != nil {
		s.writeStoreErrorForIdentity(w, identity, err)
		return
	}
	if !identity.CanProject(child.ProjectID) {
		s.writeError(w, http.StatusNotFound, "not_found", "task not found", nil)
		return
	}
	s.mutation(w, r, identity, func() (int, []byte, string, error) {
		updated, mutationErr := s.Store.SetTaskParentWithClaimOverride(r.Context(), child.ID, parent.ID, version, identity.Actor.ID, !identity.IsToken && identity.Actor.Admin)
		if mutationErr != nil {
			return 0, nil, "", mutationErr
		}
		body, marshalErr := marshalTaskForIdentity(identity, updated)
		if marshalErr != nil {
			return 0, nil, "", marshalErr
		}
		return http.StatusOK, body, taskETag(updated), nil
	})
}

func (s *Server) removeTaskChild(w http.ResponseWriter, r *http.Request, identity auth.Identity, parentReference, childReference string) {
	if !requireScope(w, identity, "tasks:write") {
		return
	}
	version, err := parseVersion(r)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if !requireHierarchyIdempotencyKey(w, r) || s.idempotencyReplay(w, r, identity) {
		return
	}
	parent, err := s.authorizedHierarchyTask(r, identity, parentReference)
	if err != nil {
		s.writeStoreErrorForIdentity(w, identity, err)
		return
	}
	child, err := s.Store.ResolveTaskReference(r.Context(), childReference)
	if err != nil {
		s.writeStoreErrorForIdentity(w, identity, err)
		return
	}
	if !identity.CanProject(child.ProjectID) {
		s.writeError(w, http.StatusNotFound, "not_found", "task not found", nil)
		return
	}
	s.mutation(w, r, identity, func() (int, []byte, string, error) {
		updated, mutationErr := s.Store.RemoveTaskChildWithClaimOverride(r.Context(), parent.ID, child.ID, version, identity.Actor.ID, !identity.IsToken && identity.Actor.Admin)
		if mutationErr != nil {
			return 0, nil, "", mutationErr
		}
		body, marshalErr := marshalTaskForIdentity(identity, updated)
		if marshalErr != nil {
			return 0, nil, "", marshalErr
		}
		return http.StatusOK, body, taskETag(updated), nil
	})
}

func (s *Server) setTaskParentHTTP(w http.ResponseWriter, r *http.Request, identity auth.Identity, childReference string) {
	if !requireScope(w, identity, "tasks:write") {
		return
	}
	version, err := parseVersion(r)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if !requireHierarchyIdempotencyKey(w, r) || s.idempotencyReplay(w, r, identity) {
		return
	}
	child, err := s.authorizedHierarchyTask(r, identity, childReference)
	if err != nil {
		s.writeStoreErrorForIdentity(w, identity, err)
		return
	}
	parentReference, err := decodeHierarchyReference(r, []string{"parent", "parent_id", "parent_task_id"}, "parent")
	if err != nil {
		writeHierarchyInputError(w, err)
		return
	}
	parent, err := s.Store.ResolveTaskReference(r.Context(), parentReference)
	if err != nil {
		s.writeStoreErrorForIdentity(w, identity, err)
		return
	}
	if !identity.CanProject(parent.ProjectID) {
		s.writeError(w, http.StatusNotFound, "not_found", "task not found", nil)
		return
	}
	s.mutation(w, r, identity, func() (int, []byte, string, error) {
		updated, mutationErr := s.Store.SetTaskParentWithClaimOverride(r.Context(), child.ID, parent.ID, version, identity.Actor.ID, !identity.IsToken && identity.Actor.Admin)
		if mutationErr != nil {
			return 0, nil, "", mutationErr
		}
		body, marshalErr := marshalTaskForIdentity(identity, updated)
		if marshalErr != nil {
			return 0, nil, "", marshalErr
		}
		return http.StatusOK, body, taskETag(updated), nil
	})
}

func (s *Server) clearTaskParentHTTP(w http.ResponseWriter, r *http.Request, identity auth.Identity, childReference string) {
	if !requireScope(w, identity, "tasks:write") {
		return
	}
	version, err := parseVersion(r)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if !requireHierarchyIdempotencyKey(w, r) || s.idempotencyReplay(w, r, identity) {
		return
	}
	child, err := s.authorizedHierarchyTask(r, identity, childReference)
	if err != nil {
		s.writeStoreErrorForIdentity(w, identity, err)
		return
	}
	s.mutation(w, r, identity, func() (int, []byte, string, error) {
		updated, mutationErr := s.Store.ClearTaskParentWithClaimOverride(r.Context(), child.ID, version, identity.Actor.ID, !identity.IsToken && identity.Actor.Admin)
		if mutationErr != nil {
			return 0, nil, "", mutationErr
		}
		body, marshalErr := marshalTaskForIdentity(identity, updated)
		if marshalErr != nil {
			return 0, nil, "", marshalErr
		}
		return http.StatusOK, body, taskETag(updated), nil
	})
}

func (s *Server) authorizedHierarchyTask(r *http.Request, identity auth.Identity, reference string) (store.Task, error) {
	task, err := s.Store.ResolveTaskReference(r.Context(), reference)
	if err != nil {
		return store.Task{}, err
	}
	if !identity.CanProject(task.ProjectID) {
		return store.Task{}, &store.Error{Kind: store.ErrForbidden, Message: "token is not scoped to this project"}
	}
	return task, nil
}

func requireHierarchyIdempotencyKey(w http.ResponseWriter, r *http.Request) bool {
	if strings.TrimSpace(r.Header.Get("Idempotency-Key")) != "" {
		return true
	}
	sDummyWriteError(w, http.StatusBadRequest, "idempotency_required", "Idempotency-Key is required for hierarchy mutations", nil)
	return false
}

func decodeHierarchyReference(r *http.Request, aliases []string, field string) (string, error) {
	var payload map[string]json.RawMessage
	fields, err := decodeJSONObject(r, &payload)
	if err != nil {
		return "", err
	}
	allowed := make(map[string]struct{}, len(aliases))
	for _, alias := range aliases {
		allowed[alias] = struct{}{}
	}
	for name := range fields {
		if _, ok := allowed[name]; !ok {
			return "", taskInputError("unknown hierarchy field: " + name)
		}
	}
	var raw json.RawMessage
	for _, alias := range aliases {
		if value, ok := fields[alias]; ok {
			raw = value
			break
		}
	}
	if raw == nil {
		return "", taskInputError(field + " is required")
	}
	value, err := parseTaskString(raw, field, false)
	if err != nil {
		return "", err
	}
	if value == nil {
		return "", taskInputError(field + " cannot be null")
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return "", taskInputError(field + " must not be empty")
	}
	if utf8.RuneCountInString(trimmed) > maxDependencyReferenceLength {
		return "", taskInputError(field + " is too long")
	}
	return trimmed, nil
}

func writeHierarchyInputError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrInvalid) {
		sDummyWriteError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	sDummyWriteError(w, http.StatusBadRequest, "invalid_json", "request body is invalid", nil)
}
