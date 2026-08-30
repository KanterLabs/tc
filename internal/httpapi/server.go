// Package httpapi exposes the documented Roadmap HTTP API and the embedded
// frontend fallback used when a separate web/dist bundle is unavailable.
package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"roadmap/internal/auth"
	"roadmap/internal/config"
	"roadmap/internal/store"
	"roadmap/internal/webassets"
)

type Server struct {
	Store  *store.Store
	Auth   *auth.Manager
	Cfg    config.Config
	idemMu sync.Mutex
	// mutationLimiter is initialized by New and is intentionally process-local.
	// Persistent agent accounting lives in store so a restart cannot reset the
	// actor's resource budget.
	mutationLimiter             *mutationRateLimiter
	mutationLimiterOnce         sync.Once
	agentRequestLimiter         *mutationRateLimiter
	agentRequestLimiterOnce     sync.Once
	bearerCredentialLimiter     *mutationRateLimiter
	bearerCredentialLimiterOnce sync.Once
	bodyBufferPool              *bodyBufferPool
	bearerAuthSlots             chan struct{}
	static                      http.Handler
}

type contextKey string

const (
	requestBodyKey      contextKey = "roadmap-request-body"
	requestIdentityKey  contextKey = "roadmap-request-identity"
	maxRequestBodyBytes            = 2 * 1024 * 1024
)

const (
	defaultBodyBufferTotalSlots  = 16
	defaultBodyBufferAgentSlots  = 8
	defaultBodyBufferPublicSlots = 4
	defaultBearerAuthSlots       = 4
)

type bodyBufferClass uint8

const (
	bodyBufferHuman bodyBufferClass = iota
	bodyBufferAgent
	bodyBufferPublic
)

type bodyBufferPool struct {
	total  chan struct{}
	agent  chan struct{}
	public chan struct{}
}

func newBodyBufferPool(total, agent, public int) *bodyBufferPool {
	if total < 1 {
		total = defaultBodyBufferTotalSlots
	}
	if agent < 1 || agent > total {
		agent = minBodyBufferClassSlots(defaultBodyBufferAgentSlots, total)
	}
	if public < 1 || public > total {
		public = minBodyBufferClassSlots(defaultBodyBufferPublicSlots, total)
	}
	return &bodyBufferPool{total: make(chan struct{}, total), agent: make(chan struct{}, agent), public: make(chan struct{}, public)}
}

func minBodyBufferClassSlots(want, total int) int {
	if want < 1 {
		return 1
	}
	if want > total {
		return total
	}
	return want
}

func (p *bodyBufferPool) classSlots(class bodyBufferClass) chan struct{} {
	switch class {
	case bodyBufferAgent:
		return p.agent
	case bodyBufferPublic:
		return p.public
	default:
		return nil
	}
}

func (p *bodyBufferPool) tryAcquire(class bodyBufferClass) bool {
	if p == nil || p.total == nil {
		return true
	}
	classSlots := p.classSlots(class)
	if classSlots != nil {
		select {
		case classSlots <- struct{}{}:
		default:
			return false
		}
	}
	select {
	case p.total <- struct{}{}:
		return true
	default:
		if classSlots != nil {
			<-classSlots
		}
		return false
	}
}

func (p *bodyBufferPool) release(class bodyBufferClass) {
	if p == nil || p.total == nil {
		return
	}
	<-p.total
	if classSlots := p.classSlots(class); classSlots != nil {
		<-classSlots
	}
}

// All Server instances in one process share the body-buffer cap. Tests can
// replace the per-server pool with a smaller one to exercise saturation.
var processBodyBufferPool = newBodyBufferPool(defaultBodyBufferTotalSlots, defaultBodyBufferAgentSlots, defaultBodyBufferPublicSlots)
var processBearerAuthSlots = make(chan struct{}, defaultBearerAuthSlots)

func New(s *store.Store, manager *auth.Manager, cfg config.Config) *Server {
	return &Server{Store: s, Auth: manager, Cfg: cfg, mutationLimiter: newDefaultMutationRateLimiter(), agentRequestLimiter: newDefaultAgentRequestLimiter(), bearerCredentialLimiter: newDefaultBearerCredentialLimiter(), bodyBufferPool: processBodyBufferPool, bearerAuthSlots: processBearerAuthSlots}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sw := &statusWriter{ResponseWriter: w}
	w = sw
	started := time.Now()
	requestID := r.Header.Get("X-Request-ID")
	if requestID == "" || len(requestID) > 128 {
		requestID = newRequestID()
	}
	w.Header().Set("X-Request-ID", requestID)
	if s.Cfg.ReleaseSHA != "" {
		w.Header().Set("X-Roadmap-Revision", s.Cfg.ReleaseSHA)
	}
	w.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; connect-src 'self'; font-src 'self'; frame-ancestors 'none'; form-action 'self'; img-src 'self' data:; object-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'")
	w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("Permissions-Policy", "camera=(), geolocation=(), microphone=()")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	if strings.HasPrefix(s.Cfg.PublicOrigin, "https://") {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
	}
	// The OpenAPI document is part of the API surface and must not be cached
	// separately from the JSON API responses.  In particular, deployments can
	// serve a revision-specific document, so a stale intermediary response is
	// misleading even though the document itself is public.
	if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/openapi.json" {
		w.Header().Set("Cache-Control", "no-store")
	}
	if s.Cfg.PublicOrigin != "" && r.Header.Get("Origin") == s.Cfg.PublicOrigin {
		w.Header().Set("Access-Control-Allow-Origin", s.Cfg.PublicOrigin)
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
	}
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PATCH,DELETE,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization,Content-Type,If-Match,Idempotency-Key,X-Request-ID")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	identity, hasIdentity, proceed := s.prepareRequest(w, r)
	if !proceed {
		return
	}
	if isBufferableRequest(r) && r.ContentLength > maxRequestBodyBytes {
		s.writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds 2 MiB", nil)
		return
	}

	var body []byte
	if shouldBufferRequestBody(r) {
		pool := s.bodyBufferPoolValue()
		class := requestBodyClass(r, identity, hasIdentity)
		if !pool.tryAcquire(class) {
			w.Header().Set("Retry-After", "1")
			s.writeError(w, http.StatusServiceUnavailable, "server_busy", "server is busy buffering request bodies; retry later", nil)
			return
		}
		defer pool.release(class)
		var err error
		body, err = readRequestBody(r.Body, r.ContentLength)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_body", "could not read request body", nil)
			return
		}
		if len(body) > maxRequestBodyBytes {
			s.writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds 2 MiB", nil)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
	}
	requestContext := context.WithValue(r.Context(), requestBodyKey, body)
	if hasIdentity {
		requestContext = context.WithValue(requestContext, requestIdentityKey, identity)
	}
	r = r.WithContext(requestContext)
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf(`{"level":"error","msg":"panic recovered","request_id":%q}`, requestID)
			s.writeError(w, http.StatusInternalServerError, "internal_error", "internal server error", nil)
		}
		log.Printf(`{"method":%q,"path":%q,"status":%d,"duration_ms":%d,"request_id":%q}`, r.Method, r.URL.Path, responseStatus(w), time.Since(started).Milliseconds(), requestID)
	}()
	s.route(w, r)
}

func (s *Server) prepareRequest(w http.ResponseWriter, r *http.Request) (auth.Identity, bool, bool) {
	protected := isProtectedAPIRequest(r)
	publicAuth := isPublicAuthRequest(r)
	if !protected && !publicAuth {
		return auth.Identity{}, false, true
	}
	if !protected && strings.TrimSpace(r.Header.Get("Authorization")) == "" {
		return auth.Identity{}, false, true
	}
	identity, err, admitted := s.authenticateRequest(w, r)
	if !admitted {
		return auth.Identity{}, false, false
	}
	if err != nil {
		if protected {
			s.writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required", nil)
			return auth.Identity{}, false, false
		}
		// Setup, login, logout, and status remain public even when a stale or
		// malformed bearer value accompanies the request.
		return auth.Identity{}, false, true
	}
	// A valid bearer is admitted before body buffering. This protects the
	// request body pool and also ensures idempotency replays pay only the
	// general request cost, not a mutation reservation.
	if !s.admitBearerRequest(w, r, identity) {
		return auth.Identity{}, false, false
	}
	return identity, true, true
}

func isProtectedAPIRequest(r *http.Request) bool {
	if !isAPIPath(r.URL.Path) {
		return false
	}
	// Keep the unauthenticated API discovery endpoint public.
	if (r.URL.Path == "/api/v1" || r.URL.Path == "/api/v1/") && r.Method == http.MethodGet {
		return false
	}
	return !isPublicAuthRequest(r)
}

func isAPIPath(value string) bool {
	return value == "/api/v1" || strings.HasPrefix(value, "/api/v1/")
}

func isPublicAuthRequest(r *http.Request) bool {
	parts := splitPath(strings.TrimPrefix(r.URL.Path, "/api/v1"))
	if len(parts) < 2 || parts[0] != "auth" {
		return false
	}
	switch parts[1] {
	case "status":
		return r.Method == http.MethodGet
	case "setup", "login", "logout":
		return r.Method == http.MethodPost
	default:
		return false
	}
}

func isBufferableRequest(r *http.Request) bool {
	if !isBodyBearingMethod(r.Method) {
		return false
	}
	return isProtectedAPIRequest(r) || isPublicAuthRequest(r)
}

func shouldBufferRequestBody(r *http.Request) bool {
	return isBufferableRequest(r) && r.Body != nil && r.Body != http.NoBody
}

func requestBodyClass(r *http.Request, identity auth.Identity, hasIdentity bool) bodyBufferClass {
	if isPublicAuthRequest(r) || !hasIdentity {
		return bodyBufferPublic
	}
	if identity.IsToken || identity.Actor.Kind != "human" {
		return bodyBufferAgent
	}
	return bodyBufferHuman
}

func requestIdentity(r *http.Request) (auth.Identity, bool) {
	identity, ok := r.Context().Value(requestIdentityKey).(auth.Identity)
	return identity, ok
}

func bearerCredentialKey(r *http.Request) (string, bool) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return "", false
	}
	credential := strings.TrimSpace(parts[1])
	if credential == "" {
		return "", false
	}
	sum := sha256.Sum256([]byte(credential))
	return hex.EncodeToString(sum[:]), true
}

func (s *Server) bearerAuthSemaphore() chan struct{} {
	if s.bearerAuthSlots != nil {
		return s.bearerAuthSlots
	}
	return processBearerAuthSlots
}

func (s *Server) authenticateRequest(w http.ResponseWriter, r *http.Request) (auth.Identity, error, bool) {
	if _, isBearer := bearerCredentialKey(r); isBearer {
		if !s.admitBearerCredential(w, r) {
			return auth.Identity{}, nil, false
		}
		slots := s.bearerAuthSemaphore()
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			w.Header().Set("Retry-After", "1")
			s.writeError(w, http.StatusServiceUnavailable, "server_busy", "server is busy authenticating bearer requests; retry later", nil)
			return auth.Identity{}, nil, false
		}
	}
	identity, err := s.Auth.Authenticate(r.Context(), r)
	return identity, err, true
}

func readRequestBody(reader io.Reader, contentLength int64) ([]byte, error) {
	limited := io.LimitReader(reader, maxRequestBodyBytes+1)
	if contentLength >= 0 && contentLength <= maxRequestBodyBytes {
		buffer := bytes.NewBuffer(make([]byte, 0, int(contentLength)))
		if _, err := buffer.ReadFrom(limited); err != nil {
			return nil, err
		}
		return buffer.Bytes(), nil
	}
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func (s *Server) bodyBufferPoolValue() *bodyBufferPool {
	if s.bodyBufferPool != nil {
		return s.bodyBufferPool
	}
	return processBodyBufferPool
}

// statusWriter lets the request logger report the status without changing
// handlers' response behavior.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *statusWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(body)
}

func responseStatus(w http.ResponseWriter) int {
	if sw, ok := w.(*statusWriter); ok && sw.status != 0 {
		return sw.status
	}
	return http.StatusOK
}

func newRequestID() string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
	return hex.EncodeToString(sum[:8])
}

func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" || r.URL.Path == "/health" {
		s.health(w, r, false)
		return
	}
	if r.URL.Path == "/readyz" || r.URL.Path == "/ready" {
		s.health(w, r, true)
		return
	}
	if r.URL.Path == "/openapi.json" {
		s.openAPI(w, r)
		return
	}
	if !isAPIPath(r.URL.Path) {
		s.staticFile(w, r)
		return
	}
	parts := splitPath(strings.TrimPrefix(r.URL.Path, "/api/v1"))
	if len(parts) == 0 {
		if r.Method != http.MethodGet {
			s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
			return
		}
		response := map[string]any{"name": "roadmap", "version": "v1"}
		if s.Cfg.ReleaseSHA != "" {
			response["revision"] = s.Cfg.ReleaseSHA
		}
		s.writeJSON(w, http.StatusOK, response)
		return
	}
	// Auth endpoints status/setup/login are intentionally public. Logout can
	// also be called after a session has expired.
	if len(parts) >= 2 && parts[0] == "auth" {
		switch parts[1] {
		case "status":
			if r.Method == http.MethodGet {
				s.authStatus(w, r)
				return
			}
		case "setup":
			if r.Method == http.MethodPost {
				s.authSetup(w, r)
				return
			}
		case "login":
			if r.Method == http.MethodPost {
				s.authLogin(w, r)
				return
			}
		case "logout":
			if r.Method == http.MethodPost {
				s.authLogout(w, r)
				return
			}
		}
	}
	identity, ok := requestIdentity(r)
	if !ok {
		var err error
		var admitted bool
		identity, err, admitted = s.authenticateRequest(w, r)
		if !admitted {
			return
		}
		if err != nil {
			s.writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required", nil)
			return
		}
		// ServeHTTP authenticates and admits protected requests before buffering;
		// retain this fallback for direct route callers.
		if !s.admitBearerRequest(w, r, identity) {
			return
		}
	}
	if len(parts) >= 2 && parts[0] == "auth" && parts[1] == "me" && r.Method == http.MethodGet {
		s.writeJSON(w, http.StatusOK, identity.Actor)
		return
	}
	if !identity.IsToken && !s.validMutationOrigin(r) {
		s.writeError(w, http.StatusForbidden, "csrf_origin", "request origin is not allowed", nil)
		return
	}
	s.dispatchAuthed(w, r, identity, parts)
}

func (s *Server) validMutationOrigin(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return true
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		// Browser mutations always carry Origin. Requiring it prevents a
		// cross-site form or legacy client from riding an ambient session cookie.
		// App bearer tokens are checked before this guard and remain suitable for
		// non-browser automation.
		return false
	}
	return s.Cfg.PublicOrigin != "" && origin == s.Cfg.PublicOrigin
}

func splitPath(value string) []string {
	value = strings.Trim(value, "/")
	if value == "" {
		return nil
	}
	raw := strings.Split(value, "/")
	result := make([]string, 0, len(raw))
	for _, part := range raw {
		if decoded, err := strconv.Unquote(`"` + part + `"`); err == nil {
			part = decoded
		}
		result = append(result, part)
	}
	return result
}

func (s *Server) dispatchAuthed(w http.ResponseWriter, r *http.Request, identity auth.Identity, parts []string) {
	if len(parts) == 1 {
		switch parts[0] {
		case "projects":
			s.projects(w, r, identity)
		case "events":
			s.events(w, r, identity)
		case "roadmap":
			s.roadmap(w, r, identity, "", false)
		case "my-work":
			s.myWork(w, r, identity)
		case "issues":
			s.issues(w, r, identity)
		case "agents":
			s.agents(w, r, identity)
		default:
			s.writeError(w, http.StatusNotFound, "not_found", "route not found", nil)
		}
		return
	}
	if parts[0] == "projects" {
		if len(parts) == 2 {
			s.project(w, r, identity, parts[1])
			return
		}
		if len(parts) == 3 {
			switch parts[2] {
			case "tasks":
				s.tasks(w, r, identity, parts[1])
			case "columns":
				s.columns(w, r, identity, parts[1])
			case "labels":
				s.labels(w, r, identity, parts[1])
			case "roadmap":
				s.roadmap(w, r, identity, parts[1], true)
			case "audits":
				s.audits(w, r, identity, parts[1], true)
			default:
				s.writeError(w, http.StatusNotFound, "not_found", "route not found", nil)
			}
			return
		}
	}
	if parts[0] == "columns" && len(parts) == 2 {
		s.column(w, r, identity, parts[1])
		return
	}
	if parts[0] == "tasks" && len(parts) >= 2 {
		if len(parts) == 2 {
			s.task(w, r, identity, parts[1])
			return
		}
		if len(parts) == 3 {
			switch parts[2] {
			case "comments":
				s.comments(w, r, identity, parts[1])
			case "timeline":
				s.taskTimeline(w, r, identity, parts[1])
			case "progress":
				s.taskProgress(w, r, identity, parts[1])
			case "heartbeat":
				s.taskHeartbeat(w, r, identity, parts[1])
			case "move":
				s.taskMove(w, r, identity, parts[1])
			case "claim":
				s.taskAction(w, r, identity, parts[1], "claim")
			case "renew":
				s.taskAction(w, r, identity, parts[1], "renew")
			case "release":
				s.taskAction(w, r, identity, parts[1], "release")
			case "complete":
				s.taskAction(w, r, identity, parts[1], "complete")
			case "block":
				s.taskAction(w, r, identity, parts[1], "block")
			case "triage", "resolve", "reopen":
				s.issueAction(w, r, identity, parts[1], parts[2])
			default:
				s.writeError(w, http.StatusNotFound, "not_found", "route not found", nil)
			}
			return
		}
	}
	if parts[0] == "audits" && len(parts) >= 2 {
		if len(parts) == 2 {
			s.audit(w, r, identity, parts[1])
			return
		}
		if len(parts) == 3 {
			switch parts[2] {
			case "findings":
				s.auditFindings(w, r, identity, parts[1])
			case "finalize":
				s.auditFinalize(w, r, identity, parts[1])
			default:
				s.writeError(w, http.StatusNotFound, "not_found", "route not found", nil)
			}
			return
		}
	}
	if parts[0] == "audit-findings" && len(parts) == 2 {
		s.auditFinding(w, r, identity, parts[1])
		return
	}
	if parts[0] == "labels" && len(parts) == 2 && r.Method == http.MethodDelete {
		s.deleteLabel(w, r, identity, parts[1])
		return
	}
	if parts[0] == "agents" && len(parts) == 3 && parts[2] == "tokens" && r.Method == http.MethodPost {
		s.createAgentToken(w, r, identity, parts[1])
		return
	}
	if parts[0] == "tokens" && len(parts) == 2 && r.Method == http.MethodDelete {
		s.deleteToken(w, r, identity, parts[1])
		return
	}
	s.writeError(w, http.StatusNotFound, "not_found", "route not found", nil)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request, ready bool) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
		return
	}
	if ready {
		if err := s.Store.DB.PingContext(r.Context()); err != nil {
			s.writeError(w, http.StatusServiceUnavailable, "not_ready", "database unavailable", nil)
			return
		}
	}
	response := map[string]any{"status": "ok", "service": "roadmap"}
	if s.Cfg.ReleaseSHA != "" {
		response["revision"] = s.Cfg.ReleaseSHA
	}
	s.writeJSON(w, http.StatusOK, response)
}

func (s *Server) staticFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
		return
	}
	// The frontend is embedded so a standalone binary does not depend on a
	// source checkout or a runtime web/dist directory. Keep the /web/dist alias
	// for deployments that previously used that path in asset URLs.
	clean := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if strings.HasPrefix(clean, "web/dist/") {
		clean = strings.TrimPrefix(clean, "web/dist/")
	}
	if clean == "." || clean == "" {
		clean = "index.html"
	}
	if data, err := fs.ReadFile(webassets.Dist, clean); err == nil {
		if contentType := mime.TypeByExtension(path.Ext(clean)); contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		if strings.HasPrefix(clean, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(data)
		}
		return
	}
	// Client-side routes are extensionless and should resolve to the SPA shell.
	// A missing asset must remain a real 404 rather than returning HTML with a
	// successful status, which otherwise hides broken production bundles.
	if path.Ext(clean) == "" {
		if data, err := fs.ReadFile(webassets.Dist, "index.html"); err == nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			if r.Method != http.MethodHead {
				_, _ = w.Write(data)
			}
			return
		}
	}
	s.writeError(w, http.StatusNotFound, "not_found", "asset not found", nil)
}

func (s *Server) openAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(openAPIDocument)
}

func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	status, err := s.Auth.Status(r.Context())
	if err != nil {
		s.writeInternal(w, err)
		return
	}
	status["authenticated"] = false
	status["actor"] = nil
	status["user"] = nil
	identity, authenticated := requestIdentity(r)
	if !authenticated && strings.TrimSpace(r.Header.Get("Authorization")) == "" {
		if authenticatedIdentity, authErr, admitted := s.authenticateRequest(w, r); !admitted {
			return
		} else if authErr == nil {
			identity, authenticated = authenticatedIdentity, true
		}
	}
	if authenticated {
		status["authenticated"] = true
		status["actor"] = identity.Actor
		status["user"] = identity.Actor
		// The UI and API use separate Cloudflare Access applications. A normal
		// browser navigation to this endpoint lets Access issue its API-path
		// authorization cookie, then returns the user to the SPA. API clients and
		// fetch requests explicitly ask for JSON and retain the status response.
		if s.Cfg.AuthMode == "cloudflare" && !identity.IsToken && strings.Contains(r.Header.Get("Accept"), "text/html") {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
	}
	s.writeJSON(w, http.StatusOK, status)
}

func (s *Server) authSetup(w http.ResponseWriter, r *http.Request) {
	if !s.validMutationOrigin(r) {
		s.writeError(w, http.StatusForbidden, "csrf_origin", "request origin is not allowed", nil)
		return
	}
	if !s.rejectAuthIdempotencyKey(w, r) {
		return
	}
	var input struct {
		Email    *string `json:"email"`
		Name     *string `json:"name"`
		Password *string `json:"password"`
	}
	fields, err := decodeJSONObject(r, &input)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_json", "request body is invalid", nil)
		return
	}
	if err := requireJSONFields(fields, "email", "password"); err != nil {
		s.writeStoreError(w, err)
		return
	}
	if input.Email == nil || input.Password == nil {
		// requireJSONFields handles the explicit-null case; keep this guard
		// defensive if a future decoder changes pointer behavior.
		s.writeError(w, http.StatusBadRequest, "invalid_request", "email and password are required", nil)
		return
	}
	if !auth.ValidEmail(*input.Email) {
		s.writeError(w, http.StatusBadRequest, "invalid_request", "a valid email is required", nil)
		return
	}
	if err := validateStringLength(json.RawMessage(strconv.Quote(*input.Password)), "password", 12, 0, false); err != nil {
		s.writeStoreError(w, err)
		return
	}
	if raw, present := fields["name"]; present {
		if err := validateStringLength(raw, "name", 1, 200, true); err != nil {
			s.writeStoreError(w, err)
			return
		}
	}
	name := ""
	if input.Name != nil {
		name = *input.Name
	}
	actor, err := s.Auth.Setup(r.Context(), *input.Email, name, *input.Password)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, actor)
}

func (s *Server) authLogin(w http.ResponseWriter, r *http.Request) {
	if !s.validMutationOrigin(r) {
		s.writeError(w, http.StatusForbidden, "csrf_origin", "request origin is not allowed", nil)
		return
	}
	if !s.rejectAuthIdempotencyKey(w, r) {
		return
	}
	var input struct {
		Email    *string `json:"email"`
		Password *string `json:"password"`
	}
	fields, err := decodeJSONObject(r, &input)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_json", "request body is invalid", nil)
		return
	}
	if err := requireJSONFields(fields, "email", "password"); err != nil {
		s.writeStoreError(w, err)
		return
	}
	if input.Email == nil || input.Password == nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", "email and password are required", nil)
		return
	}
	if !auth.ValidEmail(*input.Email) {
		s.writeError(w, http.StatusBadRequest, "invalid_request", "a valid email is required", nil)
		return
	}
	if err := validateStringLength(json.RawMessage(strconv.Quote(*input.Password)), "password", 1, 0, false); err != nil {
		s.writeStoreError(w, err)
		return
	}
	actor, err := s.Auth.Login(r.Context(), *input.Email, *input.Password, w)
	if err != nil {
		if errors.Is(err, store.ErrForbidden) {
			s.writeError(w, http.StatusUnauthorized, "invalid_credentials", "invalid email or password", nil)
			return
		}
		s.writeStoreError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, actor)
}

func (s *Server) authLogout(w http.ResponseWriter, r *http.Request) {
	if !s.validMutationOrigin(r) {
		s.writeError(w, http.StatusForbidden, "csrf_origin", "request origin is not allowed", nil)
		return
	}
	if !s.rejectAuthIdempotencyKey(w, r) {
		return
	}
	if err := s.Auth.Logout(r.Context(), r, w); err != nil {
		s.writeInternal(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func decodeJSON(r *http.Request, value any) error {
	_, err := decodeJSONObject(r, value)
	return err
}

// decodeJSONObject decodes one JSON object and returns its raw fields as well
// as the typed value. The standard json decoder intentionally treats a JSON
// null assigned to a pointer or scalar as the zero value, which makes an
// omitted field indistinguishable from an explicit null. Request handlers use
// the returned map to enforce the OpenAPI nullability and required-field
// rules before passing values to the store.
func decodeJSONObject(r *http.Request, value any) (map[string]json.RawMessage, error) {
	body, err := requestBodyData(r)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, errors.New("request body must be a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return nil, err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, errors.New("multiple JSON values")
	}
	return fields, nil
}

func requestBodyData(r *http.Request) ([]byte, error) {
	if body, ok := r.Context().Value(requestBodyKey).([]byte); ok {
		if len(body) == 0 {
			return nil, io.EOF
		}
		return body, nil
	}
	if r.Body == nil {
		return nil, io.EOF
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, io.EOF
	}
	// Preserve the body for direct route callers that may inspect it after a
	// validation pass. ServeHTTP already provides an immutable context copy.
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

func jsonFieldPresent(fields map[string]json.RawMessage, name string) bool {
	_, ok := fields[name]
	return ok
}

func jsonFieldNull(fields map[string]json.RawMessage, name string) bool {
	value, ok := fields[name]
	return ok && isJSONNull(value)
}

func rejectJSONNull(fields map[string]json.RawMessage, names ...string) error {
	for _, name := range names {
		if jsonFieldNull(fields, name) {
			return taskInputError(name + " cannot be null")
		}
	}
	return nil
}

func requireJSONFields(fields map[string]json.RawMessage, names ...string) error {
	for _, name := range names {
		value, ok := fields[name]
		if !ok {
			return taskInputError(name + " is required")
		}
		if isJSONNull(value) {
			return taskInputError(name + " cannot be null")
		}
	}
	return nil
}

// validateIdentifierArray enforces the shared Identifier item schema. In
// particular, unmarshalling []string alone would silently turn a null item
// into an empty string, and store.uniqueStrings would then discard it.
func validateIdentifierArray(raw json.RawMessage, field string, nullable bool) error {
	if isJSONNull(raw) {
		if nullable {
			return nil
		}
		return taskInputError(field + " cannot be null")
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if isJSONNull(item) {
			return taskInputError(field + " items cannot be null")
		}
		var value string
		if err := json.Unmarshal(item, &value); err != nil {
			return err
		}
		if utf8.RuneCountInString(value) == 0 || utf8.RuneCountInString(value) > 200 || strings.TrimSpace(value) == "" {
			return taskInputError(field + " items must be non-empty identifiers")
		}
		if _, exists := seen[value]; exists {
			return taskInputError(field + " must not contain duplicate items")
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateStringLength(raw json.RawMessage, field string, min, max int, trimEmpty bool) error {
	if isJSONNull(raw) {
		return taskInputError(field + " cannot be null")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	length := utf8.RuneCountInString(value)
	if trimEmpty && strings.TrimSpace(value) == "" {
		return taskInputError(field + " must not be empty")
	}
	if length < min || (max > 0 && length > max) {
		if min > 0 && max > 0 {
			return taskInputError(fmt.Sprintf("%s must be between %d and %d characters", field, min, max))
		}
		if min > 0 {
			return taskInputError(fmt.Sprintf("%s must contain at least %d characters", field, min))
		}
		return taskInputError(fmt.Sprintf("%s is too long", field))
	}
	return nil
}

func (s *Server) rejectAuthIdempotencyKey(w http.ResponseWriter, r *http.Request) bool {
	if strings.TrimSpace(r.Header.Get("Idempotency-Key")) == "" {
		return true
	}
	s.writeError(w, http.StatusBadRequest, "idempotency_not_supported", "Idempotency-Key is not supported on authentication routes", nil)
	return false
}

func bodyBytes(r *http.Request) []byte {
	if body, ok := r.Context().Value(requestBodyKey).([]byte); ok {
		return body
	}
	return nil
}

func isBodyBearingMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func (s *Server) mutation(w http.ResponseWriter, r *http.Request, identity auth.Identity, fn func() (int, []byte, string, error)) {
	s.mutationWithAdmission(w, r, identity, s.admitMutation, fn)
}

// mutationRateOnly retains ordinary idempotent replay and short-lived rate
// limiting without charging the persistent agent storage allowance. Use it
// only for bounded lifecycle metadata changes that do not add user-sized
// content, events, comments, or task state.
func (s *Server) mutationRateOnly(w http.ResponseWriter, r *http.Request, identity auth.Identity, fn func() (int, []byte, string, error)) {
	s.mutationWithAdmission(w, r, identity, s.admitMutationRate, fn)
}

func (s *Server) mutationWithAdmission(w http.ResponseWriter, r *http.Request, identity auth.Identity, admit func(http.ResponseWriter, *http.Request, auth.Identity) bool, fn func() (int, []byte, string, error)) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		if !admit(w, r, identity) {
			return
		}
		status, body, etag, err := fn()
		if err != nil {
			s.writeStoreErrorForIdentity(w, identity, err)
			return
		}
		s.writeRaw(w, status, body, etag)
		return
	}
	if len(key) > 255 {
		s.writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key is too long", nil)
		return
	}
	hash := sha256.Sum256(bodyBytes(r))
	requestHash := hex.EncodeToString(hash[:])
	storeKey := idempotencyStoreKey(identity, key)
	s.idemMu.Lock()
	defer s.idemMu.Unlock()
	record, found, err := s.Store.GetIdempotency(r.Context(), identity.Actor.ID, storeKey, r.Method, r.URL.Path, requestHash)
	if err != nil {
		s.writeStoreErrorForIdentity(w, identity, err)
		return
	}
	if found {
		if record.ResponseLocation != "" {
			w.Header().Set("Location", record.ResponseLocation)
		}
		s.writeRaw(w, record.Status, record.ResponseBody, record.ETag)
		return
	}
	if !admit(w, r, identity) {
		return
	}
	status, body, etag, err := fn()
	if err != nil {
		s.writeStoreErrorForIdentity(w, identity, err)
		return
	}
	if err := s.Store.SaveIdempotency(r.Context(), identity.Actor.ID, storeKey, r.Method, r.URL.Path, requestHash, store.IdempotencyRecord{Status: status, ResponseBody: body, ETag: etag, ResponseLocation: w.Header().Get("Location")}); err != nil {
		s.writeStoreErrorForIdentity(w, identity, err)
		return
	}
	s.writeRaw(w, status, body, etag)
}

// idempotencyStoreKey keeps retries for one bearer credential isolated from a
// different credential owned by the same agent actor. The database row still
// uses actor_id as its foreign key, while the token ID becomes part of the
// caller-provided key namespace. Human sessions retain actor-wide replay
// semantics because their authenticated principal is the actor itself.
func idempotencyStoreKey(identity auth.Identity, key string) string {
	if identity.IsToken {
		return "token:" + identity.Token.Token.ID + ":" + key
	}
	return key
}

// idempotencyReplay checks an existing record before handlers resolve a
// mutable resource. A missing record is deliberately not an admission: the
// caller continues through normal validation and mutation handling. Scope and
// administrator checks must run before calling this helper.
func (s *Server) idempotencyReplay(w http.ResponseWriter, r *http.Request, identity auth.Identity) bool {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		return false
	}
	if len(key) > 255 {
		s.writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key is too long", nil)
		return true
	}
	hash := sha256.Sum256(bodyBytes(r))
	requestHash := hex.EncodeToString(hash[:])
	storeKey := idempotencyStoreKey(identity, key)
	s.idemMu.Lock()
	record, found, err := s.Store.GetIdempotency(r.Context(), identity.Actor.ID, storeKey, r.Method, r.URL.Path, requestHash)
	s.idemMu.Unlock()
	if err != nil {
		s.writeStoreError(w, err)
		return true
	}
	if !found {
		return false
	}
	if record.ResponseLocation != "" {
		w.Header().Set("Location", record.ResponseLocation)
	}
	s.writeRaw(w, record.Status, record.ResponseBody, record.ETag)
	return true
}

func (s *Server) writeRaw(w http.ResponseWriter, status int, body []byte, etag string) {
	if etag != "" {
		w.Header().Set("ETag", etag)
	}
	if len(body) > 0 {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(status)
	if len(body) > 0 {
		_, _ = w.Write(body)
	}
}
func (s *Server) writeJSON(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		s.writeInternal(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
func (s *Server) writeCollection(w http.ResponseWriter, data any, next string) {
	s.writeJSON(w, http.StatusOK, map[string]any{"data": data, "next_cursor": next})
}

func (s *Server) writeStoreError(w http.ResponseWriter, err error) {
	s.writeStoreErrorForIdentity(w, auth.Identity{}, err)
}

// writeStoreErrorForIdentity preserves the detailed task snapshot for human
// callers and bearer credentials with tasks:read. Mutation and action
// conflicts are also returned to write-only/claim-only credentials, but their
// current task snapshot is reduced to retry metadata so read-protected task
// fields cannot be used as a side channel.
func (s *Server) writeStoreErrorForIdentity(w http.ResponseWriter, identity auth.Identity, err error) {
	if err == nil {
		return
	}
	status, code, message, details := http.StatusInternalServerError, "internal_error", "internal server error", any(map[string]any{})
	switch {
	case errors.Is(err, store.ErrInvalid):
		status, code, message = http.StatusBadRequest, "invalid_request", err.Error()
	case errors.Is(err, store.ErrNotFound):
		status, code, message = http.StatusNotFound, "not_found", err.Error()
	case errors.Is(err, store.ErrConflict):
		status, code, message = http.StatusConflict, "conflict", err.Error()
	case errors.Is(err, store.ErrAlreadyExists):
		status, code, message = http.StatusConflict, "already_exists", err.Error()
	case errors.Is(err, store.ErrPrecondition):
		status, code, message = http.StatusPreconditionRequired, "if_match_required", "If-Match is required"
	case errors.Is(err, store.ErrForbidden):
		status, code, message = http.StatusForbidden, "forbidden", err.Error()
	case errors.Is(err, store.ErrClaimUnavailable):
		status, code, message = http.StatusConflict, "task_already_claimed", err.Error()
	}
	var typed *store.Error
	if errors.As(err, &typed) && typed.Details != nil {
		details = typed.Details
	}
	if errors.Is(err, store.ErrConflict) {
		if strings.Contains(strings.ToLower(err.Error()), "idempotency") {
			code = "idempotency_key_reused"
		}
		if typed != nil {
			if detailMap, ok := typed.Details.(map[string]any); ok {
				if _, hasCurrent := detailMap["current"]; hasCurrent {
					code = "stale_task"
				}
			}
		}
		if strings.Contains(strings.ToLower(err.Error()), "already configured") {
			code = "setup_already_complete"
		}
	}
	details = redactTaskConflictDetails(identity, details)
	s.writeError(w, status, code, message, details)
}

func redactTaskConflictDetails(identity auth.Identity, details any) any {
	if !identity.IsToken || identity.HasScope("tasks:read") {
		return details
	}
	detailMap, ok := details.(map[string]any)
	if !ok {
		return details
	}
	current, ok := detailMap["current"]
	if !ok {
		// Task and column mutations can fail because an active claim exists.
		// Store diagnostics intentionally carry the blocking task ID, claimant,
		// and lease expiry for human diagnostics, but none of those fields are
		// readable by a bearer lacking tasks:read. Return an empty details object
		// (or the explicitly safe column ID) while preserving status and code.
		if hasSensitiveClaimDetail(detailMap) {
			redacted := make(map[string]any)
			if columnID, hasColumnID := detailMap["column_id"]; hasColumnID {
				redacted["column_id"] = columnID
			}
			return redacted
		}
		return details
	}
	var redacted map[string]any
	switch task := current.(type) {
	case store.Task:
		redacted = redactedTaskConflictCurrent(task)
	case *store.Task:
		if task == nil {
			return details
		}
		redacted = redactedTaskConflictCurrent(*task)
	case store.AuditFinding:
		redacted = map[string]any{"id": task.ID, "version": task.Version}
	case *store.AuditFinding:
		if task == nil {
			return details
		}
		redacted = map[string]any{"id": task.ID, "version": task.Version}
	default:
		return details
	}
	copyDetails := make(map[string]any, len(detailMap))
	for key, value := range detailMap {
		if isSensitiveClaimDetail(key) {
			continue
		}
		copyDetails[key] = value
	}
	copyDetails["current"] = redacted
	return copyDetails
}

func hasSensitiveClaimDetail(details map[string]any) bool {
	for key := range details {
		if isSensitiveClaimDetail(key) {
			return true
		}
	}
	return false
}

func isSensitiveClaimDetail(key string) bool {
	switch key {
	case "task_id", "claimed_by", "claim_expires_at":
		return true
	default:
		return false
	}
}

func redactedTaskConflictCurrent(task store.Task) map[string]any {
	// Keep only values needed to identify the stale version and retry against
	// it. In particular, do not include title, description, labels, assignee,
	// priority, column, timestamps, or claim metadata.
	return map[string]any{
		"id":      task.ID,
		"version": task.Version,
	}
}

func (s *Server) writeInternal(w http.ResponseWriter, err error) {
	log.Printf("internal error: %v", err)
	s.writeError(w, http.StatusInternalServerError, "internal_error", "internal server error", nil)
}
func (s *Server) writeError(w http.ResponseWriter, status int, code, message string, details any) {
	if details == nil {
		details = map[string]any{}
	}
	s.writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": message, "details": details}})
}

func requireScope(w http.ResponseWriter, identity auth.Identity, scope string) bool {
	if identity.HasScope(scope) {
		return true
	}
	sDummyWriteError(w, http.StatusForbidden, "insufficient_scope", "token lacks required scope", map[string]any{"required_scope": scope})
	return false
}
func sDummyWriteError(w http.ResponseWriter, status int, code, message string, details any) {
	if details == nil {
		details = map[string]any{}
	}
	body, _ := json.Marshal(map[string]any{"error": map[string]any{"code": code, "message": message, "details": details}})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
func parseLimit(r *http.Request, fallback int) (int, error) {
	values, present := r.URL.Query()["limit"]
	if !present {
		return fallback, nil
	}
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		return 0, errors.New("limit must be a positive integer")
	}
	value, err := strconv.Atoi(values[0])
	if err != nil || value <= 0 {
		return 0, errors.New("limit must be a positive integer")
	}
	if value > 200 {
		return 200, nil
	}
	return value, nil
}

func queryValue(r *http.Request, name string) (string, bool, error) {
	values, present := r.URL.Query()[name]
	if !present {
		return "", false, nil
	}
	if len(values) != 1 {
		return "", true, fmt.Errorf("%s must be supplied at most once", name)
	}
	return values[0], true, nil
}

func parseOptionalBool(r *http.Request, name string) (bool, error) {
	value, present, err := queryValue(r, name)
	if err != nil {
		return false, err
	}
	if !present {
		return false, nil
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be a boolean", name)
	}
}

// parseOptionalStrictBool is used by newer coordination filters whose wire
// contract permits only the lowercase JSON-style query spellings. Unlike the
// legacy helper above it does not trim or case-fold values.
func parseOptionalStrictBool(r *http.Request, name string) (bool, error) {
	value, present, err := queryValue(r, name)
	if err != nil {
		return false, err
	}
	if !present {
		return false, nil
	}
	switch value {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be true or false", name)
	}
}

func parseOptionalEnum(r *http.Request, name string, allowed map[string]struct{}) (string, error) {
	value, present, err := queryValue(r, name)
	if err != nil {
		return "", err
	}
	if !present {
		return "", nil
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

var semanticStates = map[string]struct{}{
	"backlog": {}, "ready": {}, "active": {}, "blocked": {}, "completed": {},
}

var taskPriorities = map[string]struct{}{
	"low": {}, "normal": {}, "high": {}, "urgent": {},
}

var taskKinds = map[string]struct{}{
	"task": {}, "bug": {},
}

var bugSeverities = map[string]struct{}{
	"s1": {}, "s2": {}, "s3": {}, "s4": {},
}

var bugSeverityFilters = map[string]struct{}{
	"s1": {}, "s2": {}, "s3": {}, "s4": {}, "untriaged": {},
}

var bugResolutions = map[string]struct{}{
	"fixed": {}, "duplicate": {}, "not_planned": {}, "cannot_reproduce": {}, "works_as_designed": {},
}

var bugResolutionFilters = map[string]struct{}{
	"fixed": {}, "duplicate": {}, "not_planned": {}, "cannot_reproduce": {}, "works_as_designed": {}, "unresolved": {},
}

func parseOptionalIdentifier(r *http.Request, name string) (string, error) {
	value, present, err := queryValue(r, name)
	if err != nil {
		return "", err
	}
	if !present {
		return "", nil
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s must not be empty", name)
	}
	if utf8.RuneCountInString(value) > 200 {
		return "", fmt.Errorf("%s is too long", name)
	}
	return value, nil
}

func parseOptionalSearch(r *http.Request) (string, error) {
	value, present, err := queryValue(r, "q")
	if err != nil {
		return "", err
	}
	if !present {
		return "", nil
	}
	if utf8.RuneCountInString(value) > 200 {
		return "", errors.New("q is too long")
	}
	return value, nil
}

func parseOptionalTimestamp(r *http.Request, name string) (*time.Time, error) {
	value, present, err := queryValue(r, name)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, fmt.Errorf("%s must be RFC3339", name)
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, fmt.Errorf("%s must be RFC3339", name)
	}
	return &parsed, nil
}

func parseAfterCursor(r *http.Request) (int64, error) {
	value, present, err := queryValue(r, "after")
	if err != nil {
		return 0, err
	}
	if !present {
		return 0, nil
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, errors.New("after must be a cursor integer")
	}
	after, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, errors.New("after must be a cursor integer")
	}
	if after < 0 {
		return 0, errors.New("after must be zero or greater")
	}
	return after, nil
}

func parsePagination(r *http.Request, fallback int) (int, int, error) {
	limit, err := parseLimit(r, fallback)
	if err != nil {
		return 0, 0, err
	}
	values, present := r.URL.Query()["cursor"]
	if !present {
		return limit, 0, nil
	}
	if len(values) != 1 {
		return 0, 0, errors.New("cursor must be supplied at most once")
	}
	offset, err := parseCursor(values[0])
	if err != nil {
		return 0, 0, err
	}
	return limit, offset, nil
}

func parseCursor(value string) (int, error) {
	if value == "" {
		return 0, errors.New("cursor must not be empty")
	}
	original := value
	if decoded, err := base64.RawURLEncoding.DecodeString(value); err == nil {
		if number, parseErr := strconv.Atoi(string(decoded)); parseErr == nil && number >= 0 {
			return number, nil
		}
	}
	number, err := strconv.Atoi(original)
	if err != nil || number < 0 {
		return 0, errors.New("cursor must be a non-negative integer or URL-safe base64 offset")
	}
	return number, nil
}
func encodeCursor(value int) string {
	if value <= 0 {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(value)))
}
func parseIfMatch(r *http.Request) (int64, error) {
	value := r.Header.Get("If-Match")
	if strings.TrimSpace(value) == "" {
		return 0, store.ErrPrecondition
	}
	if len(value) < 4 || value[0] != '"' || value[len(value)-1] != '"' || value[1] != 'v' {
		return 0, invalidIfMatch()
	}
	digits := value[2 : len(value)-1]
	if digits == "" || digits[0] == '0' {
		return 0, invalidIfMatch()
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return 0, invalidIfMatch()
		}
	}
	parsed, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, invalidIfMatch()
	}
	return parsed, nil
}

func parseVersion(r *http.Request) (int64, error) {
	return parseIfMatch(r)
}

func invalidIfMatch() error {
	return &store.Error{Kind: store.ErrInvalid, Message: "If-Match must be a task version such as \"v3\""}
}
