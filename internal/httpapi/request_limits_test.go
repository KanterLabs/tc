package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"roadmap/internal/store"
)

type trackingRequestBody struct {
	reader *strings.Reader
	reads  int
}

func (b *trackingRequestBody) Read(p []byte) (int, error) {
	b.reads++
	return b.reader.Read(p)
}

func (b *trackingRequestBody) Close() error { return nil }

type blockingRequestBody struct {
	started chan<- struct{}
	release <-chan struct{}
	reader  *strings.Reader
}

func (b *blockingRequestBody) Read(p []byte) (int, error) {
	select {
	case b.started <- struct{}{}:
	default:
	}
	<-b.release
	return b.reader.Read(p)
}

func (b *blockingRequestBody) Close() error { return nil }

func TestServeHTTPDoesNotBufferReadOnlyRequestBodies(t *testing.T) {
	server, _ := testServer(t, "disabled")
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		body := &trackingRequestBody{reader: strings.NewReader("read-only body")}
		req := httptest.NewRequest(method, "/api/v1/projects", body)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, req)
		if body.reads != 0 {
			t.Fatalf("%s body reads = %d, want 0", method, body.reads)
		}
		if remaining := body.reader.Len(); remaining != len("read-only body") {
			t.Fatalf("%s body remaining bytes = %d, want %d", method, remaining, len("read-only body"))
		}
	}
}

func TestAPIDiscoveryRemainsPublic(t *testing.T) {
	server, _ := testServer(t, "local")
	response := request(t, server, http.MethodGet, "/api/v1", nil, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("API discovery status = %d, body=%s", response.Code, response.Body.String())
	}
}

func TestServeHTTPRejectsOversizedDeclaredBodyBeforeRead(t *testing.T) {
	server, _ := testServer(t, "disabled")
	body := &trackingRequestBody{reader: strings.NewReader("must not be read")}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", body)
	req.ContentLength = maxRequestBodyBytes + 1
	response := httptest.NewRecorder()
	server.ServeHTTP(response, req)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized declared body status = %d, body=%s", response.Code, response.Body.String())
	}
	if body.reads != 0 {
		t.Fatalf("oversized declared body reads = %d, want 0", body.reads)
	}
}

func TestServeHTTPRejectsWhenBodyBufferSlotsAreSaturated(t *testing.T) {
	server, _ := testServer(t, "disabled")
	server.bodyBufferPool = newBodyBufferPool(1, 1, 1)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	firstRequest := httptest.NewRequest(http.MethodPost, "/api/v1/projects", &blockingRequestBody{
		started: started,
		release: release,
		reader:  strings.NewReader(`{"key":"FIRST","name":"First"}`),
	})
	firstResponse := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		server.ServeHTTP(firstResponse, firstRequest)
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first request did not start reading its body")
	}

	secondBody := &trackingRequestBody{reader: strings.NewReader(`{"key":"SECOND","name":"Second"}`)}
	secondRequest := httptest.NewRequest(http.MethodPost, "/api/v1/projects", secondBody)
	secondResponse := httptest.NewRecorder()
	server.ServeHTTP(secondResponse, secondRequest)
	if secondResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("saturated body buffer status = %d, body=%s", secondResponse.Code, secondResponse.Body.String())
	}
	if secondResponse.Header().Get("Retry-After") != "1" {
		t.Fatalf("saturated body buffer Retry-After = %q, want 1", secondResponse.Header().Get("Retry-After"))
	}
	if secondBody.reads != 0 {
		t.Fatalf("saturated body buffer reads = %d, want 0", secondBody.reads)
	}

	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("first request did not finish after body release")
	}
}

func TestAgentBodyBufferSaturationLeavesHumanMutationCapacity(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatal(err)
	}
	agent, err := data.CreateAgent(ctx, store.Actor{Kind: "agent", Name: "body-flood agent"}, "actor-disabled-mode", "")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := data.CreateTokenBy(ctx, agent.ID, "actor-disabled-mode", "body-flood", []string{"projects:write"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	server.bodyBufferPool = newBodyBufferPool(2, 1, 1)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	agentRequest := httptest.NewRequest(http.MethodPost, "/api/v1/projects", &blockingRequestBody{
		started: started,
		release: release,
		reader:  strings.NewReader(`{"key":"AGENTHELD","name":"Agent held"}`),
	})
	agentRequest.Header.Set("Authorization", "Bearer "+token)
	agentResponse := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		server.ServeHTTP(agentResponse, agentRequest)
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("agent request did not start reading its body")
	}

	humanResponse := request(t, server, http.MethodPost, "/api/v1/projects", map[string]any{
		"key": "HUMANWHILEAGENT", "name": "Human while agent body held",
	}, nil)
	if humanResponse.Code != http.StatusCreated {
		t.Fatalf("human mutation during agent body saturation = %d, body=%s", humanResponse.Code, humanResponse.Body.String())
	}

	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("agent request did not finish after body release")
	}
}

func TestInvalidProtectedRequestReturnsUnauthorizedWithoutReadingBody(t *testing.T) {
	server, _ := testServer(t, "local")
	body := &trackingRequestBody{reader: strings.NewReader(`{"key":"UNAUTHORIZED","name":"Must not read"}`)}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", body)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, req)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("invalid protected request status = %d, body=%s", response.Code, response.Body.String())
	}
	if body.reads != 0 {
		t.Fatalf("invalid protected request body reads = %d, want 0", body.reads)
	}
}

func TestBodyBufferPoolCapsTotalAndClassReservations(t *testing.T) {
	pool := newBodyBufferPool(16, 8, 4)
	if got := cap(pool.total); got != 16 {
		t.Fatalf("body pool total capacity = %d, want 16", got)
	}
	if got := cap(pool.agent); got != 8 {
		t.Fatalf("body pool agent capacity = %d, want 8", got)
	}
	if got := cap(pool.public); got != 4 {
		t.Fatalf("body pool public capacity = %d, want 4", got)
	}
	for i := 0; i < 8; i++ {
		if !pool.tryAcquire(bodyBufferAgent) {
			t.Fatalf("agent reservation %d rejected before class cap", i)
		}
	}
	if pool.tryAcquire(bodyBufferAgent) {
		t.Fatal("agent reservation exceeded class cap")
	}
	for i := 0; i < 8; i++ {
		if !pool.tryAcquire(bodyBufferHuman) {
			t.Fatalf("human reservation %d rejected before total cap", i)
		}
	}
	if pool.tryAcquire(bodyBufferHuman) {
		t.Fatal("body reservation exceeded total cap")
	}
	for i := 0; i < 8; i++ {
		pool.release(bodyBufferAgent)
	}
	for i := 0; i < 8; i++ {
		pool.release(bodyBufferHuman)
	}
}

func TestBearerCredentialLimiterRejectsRepeatedCredentialBeforeAuthentication(t *testing.T) {
	server, _ := testServer(t, "disabled")
	server.bearerCredentialLimiter = newMutationRateLimiter(1, 2, 2, time.Hour)
	for i := 0; i < 2; i++ {
		response := request(t, server, http.MethodGet, "/api/v1/projects", nil, map[string]string{"Authorization": "Bearer invalid-credential"})
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("invalid bearer attempt %d status = %d, body=%s", i, response.Code, response.Body.String())
		}
	}
	rejected := request(t, server, http.MethodGet, "/api/v1/projects", nil, map[string]string{"Authorization": "Bearer invalid-credential"})
	if rejected.Code != http.StatusTooManyRequests || rejected.Header().Get("Retry-After") == "" {
		t.Fatalf("credential-limited status = %d Retry-After=%q body=%s", rejected.Code, rejected.Header().Get("Retry-After"), rejected.Body.String())
	}
	for key := range server.bearerCredentialLimiter.entries {
		if key == "invalid-credential" {
			t.Fatal("raw bearer credential was retained in limiter state")
		}
	}
}

func TestBearerAuthenticationSaturationLeavesHumanMutationCapacity(t *testing.T) {
	server, _ := testServer(t, "disabled")
	server.bearerAuthSlots = make(chan struct{}, 1)
	server.bearerAuthSlots <- struct{}{}
	defer func() { <-server.bearerAuthSlots }()

	blocked := request(t, server, http.MethodGet, "/api/v1/projects", nil, map[string]string{"Authorization": "Bearer invalid-credential"})
	if blocked.Code != http.StatusServiceUnavailable || blocked.Header().Get("Retry-After") != "1" {
		t.Fatalf("saturated bearer authentication = %d Retry-After=%q body=%s", blocked.Code, blocked.Header().Get("Retry-After"), blocked.Body.String())
	}
	human := request(t, server, http.MethodPost, "/api/v1/projects", map[string]any{
		"key": "HUMANAUTHPOOL", "name": "Human while auth pool full",
	}, nil)
	if human.Code != http.StatusCreated {
		t.Fatalf("human mutation while bearer auth pool full = %d, body=%s", human.Code, human.Body.String())
	}
}

var _ io.ReadCloser = (*trackingRequestBody)(nil)
var _ io.ReadCloser = (*blockingRequestBody)(nil)

func TestBearerReadLimiterIsActorKeyedAndLeavesHumanReadsUnrestricted(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatal(err)
	}
	agent, err := data.CreateAgent(ctx, store.Actor{Kind: "agent", Name: "read-limited"}, "actor-disabled-mode", "")
	if err != nil {
		t.Fatal(err)
	}
	_, tokenOne, err := data.CreateTokenBy(ctx, agent.ID, "actor-disabled-mode", "read-one", []string{"projects:read"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, tokenTwo, err := data.CreateTokenBy(ctx, agent.ID, "actor-disabled-mode", "read-two", []string{"projects:read"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	// A small deterministic bucket makes this regression independent of wall
	// clock timing. Both bearer tokens share the actor's allowance.
	server.agentRequestLimiter = newMutationRateLimiter(1, 2, 2, time.Hour)
	for _, token := range []string{tokenOne, tokenTwo} {
		response := request(t, server, http.MethodGet, "/api/v1/projects", nil, map[string]string{"Authorization": "Bearer " + token})
		if response.Code != http.StatusOK {
			t.Fatalf("bearer read status = %d, body=%s", response.Code, response.Body.String())
		}
	}
	rejected := request(t, server, http.MethodGet, "/api/v1/projects", nil, map[string]string{"Authorization": "Bearer " + tokenOne})
	if rejected.Code != http.StatusTooManyRequests || rejected.Header().Get("Retry-After") == "" {
		t.Fatalf("bearer read rejection = %d Retry-After=%q body=%s", rejected.Code, rejected.Header().Get("Retry-After"), rejected.Body.String())
	}

	human := request(t, server, http.MethodGet, "/api/v1/projects", nil, nil)
	if human.Code != http.StatusOK {
		t.Fatalf("human read after agent limit = %d, body=%s", human.Code, human.Body.String())
	}
}
