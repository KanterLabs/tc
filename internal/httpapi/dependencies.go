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

// dependencyFilters is shared by project task collections and My Work.  The
// empty value intentionally means no graph-state filter; ready requires at
// least one prerequisite in the store query.
var dependencyFilters = map[string]struct{}{
	"blocked": {},
	"ready":   {},
}

const maxDependencyReferenceLength = 200

// taskDependencies serves the direct relation collection and its mutations:
//
//	GET    /api/v1/tasks/{task}/dependencies
//	POST   /api/v1/tasks/{task}/dependencies
//	DELETE /api/v1/tasks/{task}/dependencies/{prerequisite}
//
// Task references accept the same opaque ID or case-insensitive project-key
// form as the task detail route.  The store performs the relation validation
// and emits the dependency activity event; this layer owns authorization,
// optimistic concurrency, replay, and response shaping.
func (s *Server) taskDependencies(w http.ResponseWriter, r *http.Request, identity auth.Identity, taskReference, prerequisiteReference string) {
	if prerequisiteReference == "" && r.Method == http.MethodGet {
		s.getTaskDependencies(w, r, identity, taskReference)
		return
	}
	if prerequisiteReference == "" && r.Method == http.MethodPost {
		s.addTaskDependency(w, r, identity, taskReference)
		return
	}
	if prerequisiteReference != "" && r.Method == http.MethodDelete {
		s.removeTaskDependency(w, r, identity, taskReference, prerequisiteReference)
		return
	}
	s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
}

func (s *Server) getTaskDependencies(w http.ResponseWriter, r *http.Request, identity auth.Identity, reference string) {
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
	dependencies, err := s.Store.GetTaskDependencies(r.Context(), task.ID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	body, err := json.Marshal(dependencies)
	if err != nil {
		s.writeInternal(w, err)
		return
	}
	s.writeRaw(w, http.StatusOK, body, taskETag(task))
}

func (s *Server) addTaskDependency(w http.ResponseWriter, r *http.Request, identity auth.Identity, reference string) {
	if !requireScope(w, identity, "tasks:write") {
		return
	}
	version, err := parseVersion(r)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if !requireDependencyIdempotencyKey(w, r) {
		return
	}
	// Keep replay handling before resource lookup and body validation so a
	// successful retry remains exact even if the task has since changed.
	if s.idempotencyReplay(w, r, identity) {
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
	prerequisite, err := decodeDependencyPrerequisite(r)
	if err != nil {
		writeDependencyInputError(w, err)
		return
	}
	// Resolve the referenced task before entering the mutation so a scoped
	// bearer cannot use a cross-project error to probe a project outside its
	// ceiling. The store repeats this resolution and validates same-project
	// semantics inside its immediate transaction.
	prerequisiteTask, err := s.Store.ResolveTaskReference(r.Context(), prerequisite)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.writeError(w, http.StatusNotFound, "not_found", "task not found", nil)
			return
		}
		s.writeStoreErrorForIdentity(w, identity, err)
		return
	}
	if !identity.CanProject(prerequisiteTask.ProjectID) {
		s.writeError(w, http.StatusNotFound, "not_found", "task not found", nil)
		return
	}
	s.mutation(w, r, identity, func() (int, []byte, string, error) {
		updated, err := s.Store.AddTaskDependencyWithClaimOverride(r.Context(), task.ID, prerequisiteTask.ID, version, identity.Actor.ID, !identity.IsToken && identity.Actor.Admin)
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

func (s *Server) removeTaskDependency(w http.ResponseWriter, r *http.Request, identity auth.Identity, taskReference, prerequisiteReference string) {
	if !requireScope(w, identity, "tasks:write") {
		return
	}
	version, err := parseVersion(r)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if !requireDependencyIdempotencyKey(w, r) {
		return
	}
	if s.idempotencyReplay(w, r, identity) {
		return
	}
	task, err := s.Store.ResolveTaskReference(r.Context(), taskReference)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if !identity.CanProject(task.ProjectID) {
		s.writeError(w, http.StatusForbidden, "forbidden", "token is not scoped to this project", nil)
		return
	}
	prerequisiteTask, err := s.Store.ResolveTaskReference(r.Context(), prerequisiteReference)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.writeError(w, http.StatusNotFound, "not_found", "task not found", nil)
			return
		}
		s.writeStoreErrorForIdentity(w, identity, err)
		return
	}
	if !identity.CanProject(prerequisiteTask.ProjectID) {
		s.writeError(w, http.StatusNotFound, "not_found", "task not found", nil)
		return
	}
	s.mutation(w, r, identity, func() (int, []byte, string, error) {
		updated, err := s.Store.RemoveTaskDependencyWithClaimOverride(r.Context(), task.ID, prerequisiteTask.ID, version, identity.Actor.ID, !identity.IsToken && identity.Actor.Admin)
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

func requireDependencyIdempotencyKey(w http.ResponseWriter, r *http.Request) bool {
	if strings.TrimSpace(r.Header.Get("Idempotency-Key")) != "" {
		return true
	}
	sDummyWriteError(w, http.StatusBadRequest, "idempotency_required", "Idempotency-Key is required for dependency mutations", nil)
	return false
}

func decodeDependencyPrerequisite(r *http.Request) (string, error) {
	var payload struct {
		Prerequisite *string `json:"prerequisite"`
	}
	fields, err := decodeJSONObject(r, &payload)
	if err != nil {
		return "", err
	}
	if err := requireJSONFields(fields, "prerequisite"); err != nil {
		return "", err
	}
	if payload.Prerequisite == nil {
		return "", taskInputError("prerequisite cannot be null")
	}
	value := strings.TrimSpace(*payload.Prerequisite)
	if value == "" {
		return "", taskInputError("prerequisite must not be empty")
	}
	if utf8.RuneCountInString(value) > maxDependencyReferenceLength {
		return "", taskInputError("prerequisite is too long")
	}
	return value, nil
}

func writeDependencyInputError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrInvalid) {
		sDummyWriteError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	sDummyWriteError(w, http.StatusBadRequest, "invalid_json", "request body is invalid", nil)
}
