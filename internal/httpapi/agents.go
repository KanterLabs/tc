package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/KanterLabs/helm/internal/auth"
	"github.com/KanterLabs/helm/internal/store"
)

var tokenScopes = map[string]struct{}{
	"projects:read": {}, "projects:write": {}, "tasks:read": {},
	"tasks:write": {}, "tasks:claim": {}, "events:read": {},
}

func validateTokenScopes(raw json.RawMessage) error {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return err
	}
	if len(items) == 0 {
		return taskInputError("at least one token scope is required")
	}
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if isJSONNull(item) {
			return taskInputError("token scopes cannot contain null items")
		}
		var scope string
		if err := json.Unmarshal(item, &scope); err != nil {
			return err
		}
		if _, ok := tokenScopes[scope]; !ok {
			return taskInputError("token contains an unsupported scope")
		}
		if _, duplicate := seen[scope]; duplicate {
			return taskInputError("token scopes must not contain duplicate items")
		}
		seen[scope] = struct{}{}
	}
	return nil
}

func requireAdmin(w http.ResponseWriter, identity auth.Identity) bool {
	// Administrative operations are deliberately human-only. Agent bearer
	// tokens remain least-privilege credentials even if a malformed or legacy
	// actor record ever carries an administrator bit.
	if !identity.IsToken && identity.Actor.Admin {
		return true
	}
	sDummyWriteError(w, http.StatusForbidden, "forbidden", "administrator access is required", nil)
	return false
}

func (s *Server) agents(w http.ResponseWriter, r *http.Request, identity auth.Identity) {
	if !requireAdmin(w, identity) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		kind := strings.TrimSpace(r.URL.Query().Get("kind"))
		if values, present := r.URL.Query()["kind"]; present {
			if len(values) != 1 || strings.TrimSpace(values[0]) != "agent" {
				s.writeError(w, http.StatusBadRequest, "invalid_request", "kind must be agent", nil)
				return
			}
			kind = "agent"
		}
		if kind != "" && kind != "agent" {
			s.writeError(w, http.StatusBadRequest, "invalid_request", "kind must be agent", nil)
			return
		}
		includeDisabled := false
		if value, ok := r.URL.Query()["disabled"]; ok {
			if len(value) != 1 {
				s.writeError(w, http.StatusBadRequest, "invalid_request", "disabled must be a boolean", nil)
				return
			}
			switch strings.ToLower(strings.TrimSpace(value[0])) {
			case "true":
				includeDisabled = true
			case "false":
			default:
				s.writeError(w, http.StatusBadRequest, "invalid_request", "disabled must be a boolean", nil)
				return
			}
		}
		limit, offset, paginationErr := parsePagination(r, 50)
		if paginationErr != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_request", paginationErr.Error(), nil)
			return
		}
		agents, more, err := s.Store.ListActorsPage(r.Context(), "agent", limit, offset, includeDisabled)
		if err != nil {
			s.writeInternal(w, err)
			return
		}
		next := ""
		if more && len(agents) > 0 {
			next = encodeCursor(offset + len(agents))
		}
		s.writeCollection(w, agents, next)
	case http.MethodPost:
		var payload struct {
			Name        *string  `json:"name"`
			Description *string  `json:"description"`
			ProjectIDs  []string `json:"project_ids"`
		}
		if s.idempotencyReplay(w, r, identity) {
			return
		}
		fields, err := decodeJSONObject(r, &payload)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_json", "request body is invalid", nil)
			return
		}
		if err := requireJSONFields(fields, "name"); err != nil {
			s.writeStoreError(w, err)
			return
		}
		if err := rejectJSONNull(fields, "name", "description", "project_ids"); err != nil {
			s.writeStoreError(w, err)
			return
		}
		if payload.Name == nil || strings.TrimSpace(*payload.Name) == "" {
			s.writeStoreError(w, taskInputError("name must not be empty"))
			return
		}
		if err := validateStringLength(json.RawMessage(strconv.Quote(*payload.Name)), "name", 1, 200, true); err != nil {
			s.writeStoreError(w, err)
			return
		}
		if raw, present := fields["description"]; present {
			if err := validateStringLength(raw, "description", 0, 10000, false); err != nil {
				s.writeStoreError(w, err)
				return
			}
		}
		if raw, present := fields["project_ids"]; present {
			if err := validateIdentifierArray(raw, "project_ids", false); err != nil {
				s.writeStoreError(w, err)
				return
			}
		}
		description := ""
		if payload.Description != nil {
			description = *payload.Description
		}
		s.mutation(w, r, identity, func() (int, []byte, string, error) {
			actor, err := s.Store.CreateAgent(r.Context(), store.Actor{Kind: "agent", Name: strings.TrimSpace(*payload.Name), Description: description, ProjectIDs: payload.ProjectIDs}, identity.Actor.ID, "")
			if err != nil {
				return 0, nil, "", err
			}
			body, _ := json.Marshal(actor)
			return http.StatusCreated, body, "", nil
		})
	default:
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
	}
}

func (s *Server) createAgentToken(w http.ResponseWriter, r *http.Request, identity auth.Identity, agentID string) {
	if !requireAdmin(w, identity) {
		return
	}
	// The plaintext bearer value exists only in this response. Prevent browsers
	// and intermediaries from retaining it after the one-time reveal closes.
	w.Header().Set("Cache-Control", "no-store")
	if strings.TrimSpace(r.Header.Get("Idempotency-Key")) != "" {
		s.writeError(w, http.StatusBadRequest, "idempotency_not_supported_for_secret", "Idempotency-Key cannot be used when issuing a plaintext token", nil)
		return
	}
	agent, err := s.Store.GetActor(r.Context(), agentID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if agent.Kind != "agent" {
		s.writeError(w, http.StatusBadRequest, "invalid_request", "tokens can only be issued to agent actors", nil)
		return
	}
	var payload struct {
		Name       *string  `json:"name"`
		Scopes     []string `json:"scopes"`
		ProjectIDs []string `json:"project_ids"`
		ExpiresAt  *string  `json:"expires_at"`
	}
	fields, err := decodeJSONObject(r, &payload)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_json", "request body is invalid", nil)
		return
	}
	if err := requireJSONFields(fields, "name", "scopes"); err != nil {
		s.writeStoreError(w, err)
		return
	}
	if err := rejectJSONNull(fields, "name", "scopes", "project_ids"); err != nil {
		s.writeStoreError(w, err)
		return
	}
	if payload.Name == nil {
		s.writeStoreError(w, taskInputError("name is required"))
		return
	}
	if err := validateStringLength(json.RawMessage(strconv.Quote(*payload.Name)), "name", 1, 200, true); err != nil {
		s.writeStoreError(w, err)
		return
	}
	if raw := fields["scopes"]; raw != nil {
		if err := validateTokenScopes(raw); err != nil {
			s.writeStoreError(w, err)
			return
		}
	}
	if raw, present := fields["project_ids"]; present {
		if err := validateIdentifierArray(raw, "project_ids", false); err != nil {
			s.writeStoreError(w, err)
			return
		}
	}
	var expires *time.Time
	if raw, present := fields["expires_at"]; present && !isJSONNull(raw) {
		if payload.ExpiresAt == nil || strings.TrimSpace(*payload.ExpiresAt) == "" {
			s.writeError(w, http.StatusBadRequest, "invalid_request", "expires_at must be RFC3339", nil)
			return
		}
		parsed, err := time.Parse(time.RFC3339, *payload.ExpiresAt)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_request", "expires_at must be RFC3339", nil)
			return
		}
		if !parsed.After(time.Now().UTC()) {
			s.writeError(w, http.StatusBadRequest, "invalid_request", "expires_at must be in the future", nil)
			return
		}
		expires = &parsed
	}
	s.mutation(w, r, identity, func() (int, []byte, string, error) {
		token, plain, err := s.Store.CreateTokenBy(r.Context(), agentID, identity.Actor.ID, *payload.Name, payload.Scopes, payload.ProjectIDs, expires)
		if err != nil {
			return 0, nil, "", err
		}
		w.Header().Set("Location", "/api/v1/tokens/"+token.ID)
		response := map[string]any{"token": plain, "id": token.ID, "name": token.Name, "actor_id": token.ActorID, "scopes": token.Scopes, "project_ids": token.ProjectIDs, "expires_at": token.ExpiresAt, "created_at": token.CreatedAt}
		response["agent_id"] = token.ActorID
		body, _ := json.Marshal(response)
		return http.StatusCreated, body, "", nil
	})
}

func (s *Server) deleteToken(w http.ResponseWriter, r *http.Request, identity auth.Identity, id string) {
	if !requireAdmin(w, identity) {
		return
	}
	s.mutation(w, r, identity, func() (int, []byte, string, error) {
		if err := s.Store.DeleteTokenBy(r.Context(), id, identity.Actor.ID); err != nil {
			return 0, nil, "", err
		}
		return http.StatusNoContent, nil, "", nil
	})
}
