package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/KanterLabs/helm/internal/auth"
)

const codexAccountTimeout = 20 * time.Second

func (s *Server) codexAccount(w http.ResponseWriter, r *http.Request, identity auth.Identity, parts []string) {
	w.Header().Set("Cache-Control", "no-store")
	if identity.IsToken || identity.Actor.Kind != "human" {
		s.writeError(w, http.StatusForbidden, "human_account_required", "Codex subscriptions can only be managed by a signed-in human", nil)
		return
	}
	if s.Codex == nil {
		s.writeError(w, http.StatusServiceUnavailable, "codex_unavailable", "Codex is not available on this Helm instance", nil)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), codexAccountTimeout)
	defer cancel()
	switch {
	case len(parts) == 1 && parts[0] == "account" && r.Method == http.MethodGet:
		refresh := r.URL.Query().Get("refresh") == "true"
		if value := r.URL.Query().Get("refresh"); value != "" && value != "true" && value != "false" {
			s.writeError(w, http.StatusBadRequest, "invalid_request", "refresh must be true or false", nil)
			return
		}
		status, err := s.Codex.Account(ctx, identity.Actor.ID, refresh)
		if err != nil {
			s.writeCodexError(w)
			return
		}
		s.writeJSON(w, http.StatusOK, status)
	case len(parts) == 1 && parts[0] == "login" && r.Method == http.MethodPost:
		login, err := s.Codex.StartDeviceLogin(ctx, identity.Actor.ID)
		if err != nil {
			s.writeCodexError(w)
			return
		}
		s.writeJSON(w, http.StatusOK, login)
	case len(parts) == 2 && parts[0] == "login" && parts[1] == "cancel" && r.Method == http.MethodPost:
		var request struct {
			LoginID *string `json:"login_id"`
		}
		fields, err := decodeJSONObject(r, &request)
		if err != nil || len(fields) != 1 || request.LoginID == nil || strings.TrimSpace(*request.LoginID) == "" || len(*request.LoginID) > 200 {
			s.writeError(w, http.StatusBadRequest, "invalid_request", "login_id is required", nil)
			return
		}
		result, err := s.Codex.CancelDeviceLogin(ctx, identity.Actor.ID, strings.TrimSpace(*request.LoginID))
		if err != nil {
			s.writeCodexError(w)
			return
		}
		s.writeJSON(w, http.StatusOK, result)
	case len(parts) == 1 && parts[0] == "logout" && r.Method == http.MethodPost:
		if err := s.Codex.LogoutAccount(ctx, identity.Actor.ID); err != nil {
			s.writeCodexError(w)
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		s.writeError(w, http.StatusNotFound, "not_found", "route not found", nil)
	}
}

// Codex errors can contain upstream account details. Keep them out of HTTP
// responses and request logs while still giving the UI a retryable status.
func (s *Server) writeCodexError(w http.ResponseWriter) {
	s.writeError(w, http.StatusServiceUnavailable, "codex_account_error", "Codex could not complete the account request; retry or reconnect", nil)
}
