package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/KanterLabs/helm/internal/auth"
	"github.com/KanterLabs/helm/internal/codexruntime"
	"github.com/KanterLabs/helm/internal/store"
)

type fakeCodexAccounts struct {
	mu            sync.Mutex
	actors        []string
	draftActors   []string
	draftRequests []codexruntime.RunRequest
	accountStatus *codexruntime.AccountStatus
	draftOutput   string
	draftStatus   string
	draftErr      error
}

func (f *fakeCodexAccounts) record(actor string) {
	f.mu.Lock()
	f.actors = append(f.actors, actor)
	f.mu.Unlock()
}

func (f *fakeCodexAccounts) Account(_ context.Context, actor string, _ bool) (codexruntime.AccountStatus, error) {
	f.record(actor)
	if f.accountStatus != nil {
		return *f.accountStatus, nil
	}
	return codexruntime.AccountStatus{Connected: true, AccountType: "chatgpt", Email: actor + "@example.com", PlanType: "plus", RequiresOpenAIAuth: true}, nil
}
func (f *fakeCodexAccounts) StartDeviceLogin(_ context.Context, actor string) (codexruntime.DeviceLogin, error) {
	f.record(actor)
	return codexruntime.DeviceLogin{LoginID: "login-" + actor, VerificationURL: "https://auth.openai.test/device", UserCode: "CODE-" + actor}, nil
}
func (f *fakeCodexAccounts) CancelDeviceLogin(_ context.Context, actor, _ string) (codexruntime.CancelLoginResult, error) {
	f.record(actor)
	return codexruntime.CancelLoginResult{Status: "canceled"}, nil
}
func (f *fakeCodexAccounts) LogoutAccount(_ context.Context, actor string) error {
	f.record(actor)
	return nil
}

func (f *fakeCodexAccounts) Draft(ctx context.Context, actor string, request codexruntime.RunRequest) (codexruntime.RunResult, error) {
	if f.draftErr != nil {
		return codexruntime.RunResult{}, f.draftErr
	}
	select {
	case <-ctx.Done():
		return codexruntime.RunResult{}, ctx.Err()
	default:
	}
	f.mu.Lock()
	f.draftActors = append(f.draftActors, actor)
	f.draftRequests = append(f.draftRequests, request)
	f.mu.Unlock()
	output := f.draftOutput
	if output == "" {
		output = `{"title":"Ship Luna drafting","description":"Generate a reviewed task suggestion.","acceptance_criteria":["Returns a validated preview"],"priority":"high","rationale":"It unblocks assisted planning.","supporting_task_keys":[]}`
	}
	status := f.draftStatus
	if status == "" {
		status = "completed"
	}
	return codexruntime.RunResult{Status: status, Output: output}, nil
}

func TestCodexAccountLifecycleAndActorIsolation(t *testing.T) {
	server, _ := testServer(t, "disabled")
	fake := &fakeCodexAccounts{}
	server.Codex = fake
	humans := []auth.Identity{
		{Actor: store.Actor{ID: "human-a", Kind: "human", Name: "A"}},
		{Actor: store.Actor{ID: "human-b", Kind: "human", Name: "B"}},
	}
	for _, identity := range humans {
		for _, test := range []struct {
			method string
			parts  []string
			body   string
		}{
			{http.MethodGet, []string{"account"}, ""},
			{http.MethodPost, []string{"login"}, ""},
			{http.MethodPost, []string{"login", "cancel"}, `{"login_id":"pending"}`},
			{http.MethodPost, []string{"logout"}, ""},
		} {
			req := httptest.NewRequest(test.method, "/api/v1/codex/"+strings.Join(test.parts, "/"), strings.NewReader(test.body))
			response := httptest.NewRecorder()
			server.codexAccount(response, req, identity, test.parts)
			if response.Code != http.StatusOK {
				t.Fatalf("%s %v status=%d body=%s", test.method, test.parts, response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "token") || strings.Contains(response.Body.String(), "refresh") {
				t.Fatalf("response exposed credential field: %s", response.Body.String())
			}
		}
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.actors) != 8 {
		t.Fatalf("calls=%v", fake.actors)
	}
	for index, actor := range fake.actors {
		want := humans[index/4].Actor.ID
		if actor != want {
			t.Fatalf("call %d used actor %q, want %q", index, actor, want)
		}
	}
}

func TestCodexAccountRejectsBearerAndUnavailableRuntime(t *testing.T) {
	server, _ := testServer(t, "disabled")
	request := httptest.NewRequest(http.MethodGet, "/api/v1/codex/account", nil)
	response := httptest.NewRecorder()
	server.codexAccount(response, request, auth.Identity{Actor: store.Actor{ID: "agent", Kind: "agent"}, IsToken: true}, []string{"account"})
	if response.Code != http.StatusForbidden {
		t.Fatalf("bearer status=%d", response.Code)
	}

	response = httptest.NewRecorder()
	server.codexAccount(response, request, auth.Identity{Actor: store.Actor{ID: "human", Kind: "human"}}, []string{"account"})
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable status=%d", response.Code)
	}
}

func TestCodexCancelRejectsInvalidLoginID(t *testing.T) {
	server, _ := testServer(t, "disabled")
	server.Codex = &fakeCodexAccounts{}
	identity := auth.Identity{Actor: store.Actor{ID: "human", Kind: "human"}}
	for _, body := range []string{`{}`, `{"login_id":null}`, `{"login_id":""}`, `{"login_id":"id","extra":true}`} {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/codex/login/cancel", strings.NewReader(body))
		response := httptest.NewRecorder()
		server.codexAccount(response, request, identity, []string{"login", "cancel"})
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body=%s status=%d response=%s", body, response.Code, response.Body.String())
		}
	}
}
