package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"roadmap/internal/auth"
	"roadmap/internal/config"
	"roadmap/internal/db"
	"roadmap/internal/store"
)

func testServer(t *testing.T, mode string) (*Server, *store.Store) {
	t.Helper()
	database, err := db.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	data := store.New(database)
	cfg := config.Config{AuthMode: mode, PublicOrigin: "http://roadmap.test", SecureCookies: false}
	return New(data, auth.NewManager(data, cfg), cfg), data
}

func request(t *testing.T, handler http.Handler, method, target string, payload any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var body []byte
	if payload != nil {
		body, _ = json.Marshal(payload)
	}
	req := httptest.NewRequest(method, target, bytesReader(body))
	if method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions {
		req.Header.Set("Origin", "http://roadmap.test")
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, req)
	return result
}

func bytesReader(value []byte) *bytes.Reader { return bytes.NewReader(value) }

func TestContractMutationETagAndIdempotency(t *testing.T) {
	server, _ := testServer(t, "disabled")
	projectResponse := request(t, server, http.MethodPost, "/api/v1/projects", map[string]any{"key": "OPS", "name": "Operations"}, map[string]string{"Content-Type": "application/json", "Idempotency-Key": "project-1"})
	if projectResponse.Code != http.StatusCreated {
		t.Fatalf("create project status = %d, body=%s", projectResponse.Code, projectResponse.Body.String())
	}
	replay := request(t, server, http.MethodPost, "/api/v1/projects", map[string]any{"key": "OPS", "name": "Operations"}, map[string]string{"Content-Type": "application/json", "Idempotency-Key": "project-1"})
	if replay.Code != http.StatusCreated || replay.Body.String() != projectResponse.Body.String() {
		t.Fatalf("idempotent replay changed response: %d %s", replay.Code, replay.Body.String())
	}
	trailingSlashOrigin := request(t, server, http.MethodPost, "/api/v1/projects", map[string]any{"key": "WEB", "name": "Wrong origin"}, map[string]string{"Content-Type": "application/json", "Origin": "http://roadmap.test/"})
	if trailingSlashOrigin.Code != http.StatusForbidden {
		t.Fatalf("trailing slash origin status = %d, body=%s", trailingSlashOrigin.Code, trailingSlashOrigin.Body.String())
	}
	var project store.Project
	if err := json.Unmarshal(projectResponse.Body.Bytes(), &project); err != nil {
		t.Fatal(err)
	}
	taskResponse := request(t, server, http.MethodPost, "/api/v1/projects/OPS/tasks", map[string]any{"title": "Ship API"}, map[string]string{"Content-Type": "application/json"})
	if taskResponse.Code != http.StatusCreated || taskResponse.Header().Get("ETag") != `"v1"` {
		t.Fatalf("create task: status=%d etag=%q body=%s", taskResponse.Code, taskResponse.Header().Get("ETag"), taskResponse.Body.String())
	}
	var task store.Task
	if err := json.Unmarshal(taskResponse.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	updated := request(t, server, http.MethodPatch, "/api/v1/tasks/"+task.ID, map[string]any{"title": "updated"}, map[string]string{"Content-Type": "application/json", "If-Match": `"v1"`})
	if updated.Code != http.StatusOK || updated.Header().Get("ETag") != `"v2"` {
		t.Fatalf("update task: status=%d etag=%q body=%s", updated.Code, updated.Header().Get("ETag"), updated.Body.String())
	}
	stale := request(t, server, http.MethodPatch, "/api/v1/tasks/"+task.ID, map[string]any{"title": "stale"}, map[string]string{"Content-Type": "application/json", "If-Match": `"v1"`})
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale update status = %d, body=%s", stale.Code, stale.Body.String())
	}
	events := request(t, server, http.MethodGet, "/api/v1/events?after=0", nil, nil)
	if events.Code != http.StatusOK || !strings.Contains(events.Body.String(), "task.created") {
		t.Fatalf("events response = %d %s", events.Code, events.Body.String())
	}
}

func TestClaimsAreAtomic(t *testing.T) {
	server, data := testServer(t, "disabled")
	if _, err := data.EnsureDisabledActor(context.Background()); err != nil {
		t.Fatal(err)
	}
	project, err := data.CreateProject(context.Background(), store.ProjectInput{Key: stringPtr("CLAIM"), Name: stringPtr("Claiming")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	task, err := data.CreateTask(context.Background(), project.ID, store.TaskInput{Title: stringPtr("One winner")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	// Use distinct actors so this exercises the conditional update rather than
	// the same-owner renewal path.
	actors := make([]store.Actor, 2)
	for i := range actors {
		actors[i], err = data.CreateActor(context.Background(), store.Actor{Kind: "agent", Name: "agent" + string(rune('A'+i))}, "")
		if err != nil {
			t.Fatal(err)
		}
	}
	results := make(chan error, len(actors))
	var group sync.WaitGroup
	for _, actor := range actors {
		group.Add(1)
		go func(actor store.Actor) {
			defer group.Done()
			_, err := data.ClaimTask(context.Background(), task.ID, actor.ID, 0, 1)
			results <- err
		}(actor)
	}
	group.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("claim winners = %d", winners)
	}
	_ = server
}

func TestFirstAdminSetupIsSingleton(t *testing.T) {
	_, data := testServer(t, "local")
	manager := auth.NewManager(data, config.Config{AuthMode: "local", SecureCookies: false})
	var group sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			_, err := manager.Setup(context.Background(), "admin"+string(rune('a'+i))+"@example.com", "Admin", "password1234")
			results <- err
		}(i)
	}
	group.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("setup successes = %d", successes)
	}
}

func TestTokenPlaintextIsNeverStored(t *testing.T) {
	_, data := testServer(t, "disabled")
	if _, err := data.EnsureDisabledActor(context.Background()); err != nil {
		t.Fatal(err)
	}
	project, err := data.CreateProject(context.Background(), store.ProjectInput{Key: stringPtr("TOK"), Name: stringPtr("Tokens")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := data.CreateAgent(context.Background(), store.Actor{Kind: "agent", Name: "Token agent", ProjectIDs: []string{project.ID}}, "actor-disabled-mode", "")
	if err != nil {
		t.Fatal(err)
	}
	_, plaintext, err := data.CreateTokenBy(context.Background(), agent.ID, "actor-disabled-mode", "test", []string{"tasks:read"}, []string{project.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := data.DB.QueryRowContext(context.Background(), `SELECT COUNT(1) FROM idempotency_keys WHERE response_body LIKE ?`, "%"+plaintext+"%").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("token plaintext was persisted in an idempotency response")
	}
	var hash string
	if err := data.DB.QueryRowContext(context.Background(), `SELECT token_hash FROM tokens WHERE actor_id=?`, agent.ID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash == plaintext {
		t.Fatal("token plaintext was stored instead of a hash")
	}
}

func TestListAgentsReturnsOnlyAgentsAndTokenMetadata(t *testing.T) {
	server, data := testServer(t, "disabled")
	if _, err := data.EnsureDisabledActor(context.Background()); err != nil {
		t.Fatal(err)
	}
	human, err := data.CreateActor(context.Background(), store.Actor{Kind: "human", Name: "Owner", Email: stringPtr("owner@example.com")}, "")
	if err != nil {
		t.Fatalf("create human: %v", err)
	}
	agent, err := data.CreateAgent(context.Background(), store.Actor{Kind: "agent", Name: "Build agent"}, human.ID, "")
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	token, _, err := data.CreateTokenBy(context.Background(), agent.ID, human.ID, "CI token", []string{"tasks:read"}, nil, nil)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}

	response := request(t, server, http.MethodGet, "/api/v1/agents", nil, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("list agents status = %d, body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Data []store.Actor `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode agents: %v", err)
	}
	if len(body.Data) != 1 || body.Data[0].ID != agent.ID {
		t.Fatalf("agents = %#v, want only %q", body.Data, agent.ID)
	}
	if len(body.Data[0].Tokens) != 1 || body.Data[0].Tokens[0].ID != token.ID {
		t.Fatalf("agent tokens = %#v, want token %q", body.Data[0].Tokens, token.ID)
	}
	if strings.Contains(response.Body.String(), "owner@example.com") {
		t.Fatal("human actor leaked from agents collection")
	}

	badKind := request(t, server, http.MethodGet, "/api/v1/agents?kind=human", nil, nil)
	if badKind.Code != http.StatusBadRequest {
		t.Fatalf("human kind status = %d, body=%s", badKind.Code, badKind.Body.String())
	}
}

func TestCloudflareServiceCallerCannotForgeHumanIdentity(t *testing.T) {
	server, _ := testServer(t, "cloudflare")
	server.Cfg.AdminEmail = "owner@example.com"
	server.Cfg.PublicOrigin = "https://tc.shanekanterman.dev"
	server.Auth.AdminEmail = "owner@example.com"

	forged := request(t, server, http.MethodPost, "/api/v1/agents", map[string]any{"name": "forged"}, map[string]string{
		"Content-Type":         "application/json",
		"Origin":               "https://tc.shanekanterman.dev",
		"X-Auth-Request-Email": "owner@example.com",
	})
	if forged.Code != http.StatusUnauthorized {
		t.Fatalf("forged proxy identity status = %d, body=%s", forged.Code, forged.Body.String())
	}

	canonicalOnly := request(t, server, http.MethodPost, "/api/v1/agents", map[string]any{"name": "canonical-only"}, map[string]string{
		"Content-Type":                       "application/json",
		"Origin":                             "https://tc.shanekanterman.dev",
		"Cf-Access-Authenticated-User-Email": "owner@example.com",
	})
	if canonicalOnly.Code != http.StatusUnauthorized {
		t.Fatalf("canonical header without JWT status = %d, body=%s", canonicalOnly.Code, canonicalOnly.Body.String())
	}
}

func TestProjectScopedTokenCannotCreateOutsideItsCeiling(t *testing.T) {
	server, data := testServer(t, "disabled")
	if _, err := data.EnsureDisabledActor(context.Background()); err != nil {
		t.Fatal(err)
	}
	project, err := data.CreateProject(context.Background(), store.ProjectInput{Key: stringPtr("BOUND"), Name: stringPtr("Boundary")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := data.CreateAgent(context.Background(), store.Actor{Kind: "agent", Name: "Bound agent", ProjectIDs: []string{project.ID}}, "actor-disabled-mode", "")
	if err != nil {
		t.Fatal(err)
	}
	_, plaintext, err := data.CreateTokenBy(context.Background(), agent.ID, "actor-disabled-mode", "bound", []string{"projects:write"}, []string{project.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}

	response := request(t, server, http.MethodPost, "/api/v1/projects", map[string]any{"key": "OUTSIDE", "name": "Outside"}, map[string]string{
		"Authorization": "Bearer " + plaintext,
		"Content-Type":  "application/json",
	})
	if response.Code != http.StatusForbidden {
		t.Fatalf("scoped project creation status = %d, body=%s", response.Code, response.Body.String())
	}
	if _, err := data.GetProject(context.Background(), "OUTSIDE"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("outside project should not exist, got %v", err)
	}
}

func TestEmbeddedFrontendServesAssetsAndSPAPaths(t *testing.T) {
	server, _ := testServer(t, "disabled")

	page := request(t, server, http.MethodGet, "/", nil, nil)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Roadmap") {
		t.Fatalf("embedded index: status=%d content-type=%q body=%s", page.Code, page.Header().Get("Content-Type"), page.Body.String())
	}
	if got := page.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Fatalf("embedded index content type = %q", got)
	}

	route := request(t, server, http.MethodGet, "/p/example", nil, nil)
	if route.Code != http.StatusOK || !strings.Contains(route.Body.String(), "Roadmap") {
		t.Fatalf("SPA route: status=%d body=%s", route.Code, route.Body.String())
	}

	missing := request(t, server, http.MethodGet, "/assets/does-not-exist.js", nil, nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing asset status = %d, body=%s", missing.Code, missing.Body.String())
	}
}

func stringPtr(value string) *string { return &value }
