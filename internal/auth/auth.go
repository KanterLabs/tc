// Package auth implements local sessions, verified Cloudflare Access JWT
// identity, and scoped bearer token authentication for the HTTP layer.
package auth

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/KanterLabs/helm/internal/config"
	"github.com/KanterLabs/helm/internal/store"
)

const sessionCookie = "roadmap_session"

type Identity struct {
	Actor   store.Actor
	IsToken bool
	Token   store.AuthToken
}

func (i Identity) HasScope(scope string) bool {
	if !i.IsToken {
		return true
	}
	return i.Token.Scopes[scope]
}

func (i Identity) CanProject(projectID string) bool {
	if !i.IsToken || !i.Token.ProjectsScoped {
		return true
	}
	return i.Token.Projects[projectID]
}

type Manager struct {
	Store              *store.Store
	Mode               string
	AdminEmail         string
	SecureCookie       bool
	CloudflareVerifier CloudflareJWTVerifier
	cloudflareInitErr  error
}

func NewManager(s *store.Store, cfg config.Config) *Manager {
	m := &Manager{Store: s, Mode: cfg.AuthMode, AdminEmail: cfg.AdminEmail, SecureCookie: cfg.SecureCookies}
	if cfg.AuthMode == "cloudflare" {
		audiences := cfg.CloudflareAudiences
		if len(audiences) == 0 && cfg.CloudflareAudience != "" {
			audiences = []string{cfg.CloudflareAudience}
		}
		verifier, err := NewCloudflareJWTVerifierForAudiences(cfg.CloudflareIssuer, audiences, cfg.CloudflareJWKSURL)
		if err != nil {
			m.cloudflareInitErr = err
		} else {
			m.CloudflareVerifier = verifier
		}
	}
	return m
}

// NewManagerWithVerifier constructs a manager with an injected Cloudflare
// assertion verifier. It is useful for deterministic tests and for callers
// which keep verification keys in another trusted implementation.
func NewManagerWithVerifier(s *store.Store, cfg config.Config, verifier CloudflareJWTVerifier) *Manager {
	return &Manager{
		Store:              s,
		Mode:               cfg.AuthMode,
		AdminEmail:         cfg.AdminEmail,
		SecureCookie:       cfg.SecureCookies,
		CloudflareVerifier: verifier,
	}
}

func (m *Manager) Status(ctx context.Context) (map[string]any, error) {
	count, err := m.Store.CountHumanActors(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"mode": m.Mode, "configured": count > 0 || m.Mode == "disabled", "setup_required": count == 0 && m.Mode == "local"}, nil
}

func (m *Manager) Authenticate(ctx context.Context, r *http.Request) (Identity, error) {
	if header := strings.TrimSpace(r.Header.Get("Authorization")); header != "" {
		parts := strings.SplitN(header, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") || strings.TrimSpace(parts[1]) == "" {
			return Identity{}, errors.New("invalid bearer token")
		}
		token, err := m.Store.LookupToken(ctx, strings.TrimSpace(parts[1]))
		if err != nil {
			return Identity{}, err
		}
		return Identity{Actor: token.Actor, IsToken: true, Token: token}, nil
	}
	switch m.Mode {
	case "disabled":
		actor, err := m.Store.EnsureDisabledActor(ctx)
		if err != nil {
			return Identity{}, err
		}
		return Identity{Actor: actor}, nil
	case "cloudflare":
		if m.CloudflareVerifier == nil {
			if m.cloudflareInitErr != nil {
				return Identity{}, m.cloudflareInitErr
			}
			return Identity{}, errors.New("Cloudflare identity verifier is unavailable")
		}
		assertion, err := cloudflareAssertion(r)
		if err != nil {
			return Identity{}, err
		}
		claims, err := m.CloudflareVerifier.Verify(ctx, assertion)
		if err != nil {
			return Identity{}, errors.New("invalid Cloudflare identity")
		}
		email := claims.Email
		if !validEmail(email) {
			return Identity{}, errors.New("invalid Cloudflare identity email")
		}
		name := strings.TrimSpace(claims.Name)
		if name != "" && (utf8.RuneCountInString(name) > 200 || strings.IndexFunc(name, unicode.IsControl) >= 0) {
			return Identity{}, errors.New("invalid Cloudflare identity name")
		}
		actor, err := m.Store.EnsureCloudflareActor(ctx, email, name, m.AdminEmail)
		if err != nil {
			return Identity{}, err
		}
		return Identity{Actor: actor}, nil
	case "local":
		cookie, err := r.Cookie(sessionCookie)
		if err != nil {
			return Identity{}, errors.New("authentication required")
		}
		actor, err := m.Store.ActorForSession(ctx, cookie.Value)
		if err != nil {
			return Identity{}, errors.New("authentication required")
		}
		if actor.DisabledAt != nil {
			return Identity{}, errors.New("actor is disabled")
		}
		return Identity{Actor: actor}, nil
	default:
		return Identity{}, errors.New("authentication mode is unavailable")
	}
}

func (m *Manager) Setup(ctx context.Context, email, name, password string) (store.Actor, error) {
	if m.Mode != "local" {
		return store.Actor{}, &store.Error{Kind: store.ErrForbidden, Message: "setup is only available in local authentication mode"}
	}
	email = strings.TrimSpace(email)
	if !validEmail(email) {
		return store.Actor{}, &store.Error{Kind: store.ErrInvalid, Message: "a valid email is required"}
	}
	if utf8.RuneCountInString(password) < 12 {
		return store.Actor{}, &store.Error{Kind: store.ErrInvalid, Message: "password must be at least 12 characters"}
	}
	count, err := m.Store.CountHumanActors(ctx)
	if err != nil {
		return store.Actor{}, err
	}
	if count > 0 {
		return store.Actor{}, &store.Error{Kind: store.ErrConflict, Message: "authentication is already configured"}
	}
	hash, err := HashPassword(password)
	if err != nil {
		return store.Actor{}, &store.Error{Kind: store.ErrInvalid, Message: err.Error()}
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = strings.Split(email, "@")[0]
	}
	if utf8.RuneCountInString(name) == 0 || utf8.RuneCountInString(name) > 200 || strings.ContainsAny(name, "\r\n") {
		return store.Actor{}, &store.Error{Kind: store.ErrInvalid, Message: "name must be between 1 and 200 characters"}
	}
	return m.Store.CreateFirstAdmin(ctx, store.Actor{Kind: "human", Name: name, Email: &email, Admin: true}, hash)
}

func (m *Manager) Login(ctx context.Context, email, password string, w http.ResponseWriter, secureOverride ...bool) (store.Actor, error) {
	if m.Mode != "local" {
		return store.Actor{}, &store.Error{Kind: store.ErrForbidden, Message: "password login is only available in local authentication mode"}
	}
	email = strings.TrimSpace(email)
	if !validEmail(email) {
		return store.Actor{}, &store.Error{Kind: store.ErrInvalid, Message: "a valid email is required"}
	}
	if password == "" {
		return store.Actor{}, &store.Error{Kind: store.ErrInvalid, Message: "password is required"}
	}
	actor, hash, err := m.Store.GetPasswordHash(ctx, email)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.Actor{}, &store.Error{Kind: store.ErrForbidden, Message: "invalid email or password"}
		}
		return store.Actor{}, err
	}
	if hash == "" || !CheckPassword(password, hash) {
		return store.Actor{}, &store.Error{Kind: store.ErrForbidden, Message: "invalid email or password"}
	}
	if actor.DisabledAt != nil {
		return store.Actor{}, &store.Error{Kind: store.ErrForbidden, Message: "actor is disabled"}
	}
	raw, err := randomSession()
	if err != nil {
		return store.Actor{}, err
	}
	if err := m.Store.CreateSession(ctx, actor.ID, raw, time.Now().UTC().Add(30*24*time.Hour)); err != nil {
		return store.Actor{}, err
	}
	secure := m.SecureCookie
	if len(secureOverride) > 0 {
		secure = secureOverride[0]
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: raw, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: 30 * 24 * 60 * 60, Expires: time.Now().UTC().Add(30 * 24 * time.Hour)})
	return actor, nil
}

func cloudflareAssertion(r *http.Request) (string, error) {
	values := r.Header.Values("Cf-Access-Jwt-Assertion")
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		return "", errors.New("Cloudflare identity is required")
	}
	return strings.TrimSpace(values[0]), nil
}

func validEmail(value string) bool {
	if value == "" || len(value) > 320 || value != strings.TrimSpace(value) || strings.ContainsAny(value, "\r\n") {
		return false
	}
	parsed, err := mail.ParseAddress(value)
	return err == nil && parsed.Address == value
}

// ValidEmail exposes the same strict address validation used by local setup,
// login, and Cloudflare identity reconciliation to HTTP request validators.
func ValidEmail(value string) bool { return validEmail(value) }

func (m *Manager) Logout(ctx context.Context, r *http.Request, w http.ResponseWriter) error {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		_ = m.Store.DeleteSession(ctx, cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: m.SecureCookie, SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(0, 0)})
	return nil
}

func randomSession() (string, error) {
	buf := make([]byte, 32)
	if _, err := cryptorand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
