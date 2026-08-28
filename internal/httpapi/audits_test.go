package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"roadmap/internal/auth"
	"roadmap/internal/store"
)

func TestAuditHTTPHandlersLifecycleAndReview(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	actor, err := data.EnsureDisabledActor(ctx)
	if err != nil {
		t.Fatalf("ensure actor: %v", err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("HTTPAUDIT"), Name: stringPtr("HTTP audits")}, actor.ID)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	columns, err := data.ListColumns(ctx, project.ID)
	if err != nil || len(columns) == 0 {
		t.Fatalf("list columns: %v", err)
	}
	task, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr("HTTP target")}, actor.ID)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	identity := auth.Identity{Actor: actor}

	created := callAuditCollection(t, identity, server.audits, http.MethodPost, "/api/v1/projects/"+project.ID+"/audits", map[string]any{"scope": "board", "status": "queued"}, nil)
	if created.Code != http.StatusCreated {
		t.Fatalf("create audit status = %d, body=%s", created.Code, created.Body.String())
	}
	var run store.AuditRun
	if err := json.Unmarshal(created.Body.Bytes(), &run); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	if run.Status != "queued" || run.ProjectID != project.ID || run.ActorID != actor.ID {
		t.Fatalf("run = %+v", run)
	}

	listing := callAuditCollection(t, identity, server.audits, http.MethodGet, "/api/v1/projects/"+project.ID+"/audits?limit=1", nil, nil)
	if listing.Code != http.StatusOK || !bytes.Contains(listing.Body.Bytes(), []byte(run.ID)) {
		t.Fatalf("list audits = %d %s", listing.Code, listing.Body.String())
	}

	findingResponse := callAuditResource(t, identity, server.auditFindings, http.MethodPost, "/api/v1/audits/"+run.ID+"/findings", run.ID, map[string]any{
		"task_id":                       task.ID,
		"captured_version":              task.Version,
		"source_column":                 columns[0].ID,
		"verdict":                       "move_proposed",
		"proposed_semantic_destination": "ready",
		"confidence":                    0.9,
		"reason":                        "Move to ready after triage.",
		"evidence_refs":                 []string{"/api/v1/tasks/" + task.ID},
	}, nil)
	if findingResponse.Code != http.StatusCreated || findingResponse.Header().Get("ETag") != `"v1"` {
		t.Fatalf("append finding = %d etag=%q body=%s", findingResponse.Code, findingResponse.Header().Get("ETag"), findingResponse.Body.String())
	}
	var finding store.AuditFinding
	if err := json.Unmarshal(findingResponse.Body.Bytes(), &finding); err != nil {
		t.Fatalf("decode finding: %v", err)
	}

	runResponse := callAuditResource(t, identity, server.audit, http.MethodGet, "/api/v1/audits/"+run.ID, run.ID, nil, nil)
	if runResponse.Code != http.StatusOK || bytes.Contains(runResponse.Body.Bytes(), []byte(finding.ID)) || !bytes.Contains(runResponse.Body.Bytes(), []byte(`"finding_count":1`)) {
		t.Fatalf("get audit = %d %s", runResponse.Code, runResponse.Body.String())
	}
	findingsResponse := callAuditResource(t, identity, server.auditFindings, http.MethodGet, "/api/v1/audits/"+run.ID+"/findings?limit=1", run.ID, nil, nil)
	if findingsResponse.Code != http.StatusOK || !bytes.Contains(findingsResponse.Body.Bytes(), []byte(finding.ID)) {
		t.Fatalf("list findings = %d %s", findingsResponse.Code, findingsResponse.Body.String())
	}

	prematureReview := callAuditResource(t, identity, server.auditFinding, http.MethodPatch, "/api/v1/audit-findings/"+finding.ID, finding.ID, map[string]any{
		"review_state": "approved",
	}, map[string]string{"If-Match": `"v1"`})
	if prematureReview.Code != http.StatusConflict {
		t.Fatalf("premature review = %d body=%s, want 409", prematureReview.Code, prematureReview.Body.String())
	}

	finalized := callAuditResource(t, identity, server.auditFinalize, http.MethodPost, "/api/v1/audits/"+run.ID+"/finalize", run.ID, map[string]any{"status": "complete"}, nil)
	if finalized.Code != http.StatusOK || !bytes.Contains(finalized.Body.Bytes(), []byte(`"status":"complete"`)) {
		t.Fatalf("finalize = %d body=%s", finalized.Code, finalized.Body.String())
	}
	review := callAuditResource(t, identity, server.auditFinding, http.MethodPatch, "/api/v1/audit-findings/"+finding.ID, finding.ID, map[string]any{
		"review_state": "approved",
	}, map[string]string{"If-Match": `"v1"`})
	if review.Code != http.StatusOK || review.Header().Get("ETag") != `"v2"` || !bytes.Contains(review.Body.Bytes(), []byte(`"review_state":"approved"`)) {
		t.Fatalf("review = %d etag=%q body=%s", review.Code, review.Header().Get("ETag"), review.Body.String())
	}
	late := callAuditResource(t, identity, server.auditFindings, http.MethodPost, "/api/v1/audits/"+run.ID+"/findings", run.ID, map[string]any{
		"task_id": task.ID, "captured_version": task.Version, "source_column": columns[0].ID,
		"verdict": "correct", "confidence": 0.5, "reason": "late",
	}, nil)
	if late.Code != http.StatusConflict {
		t.Fatalf("late finding = %d body=%s, want 409", late.Code, late.Body.String())
	}
}

func TestAuditHTTPHandlersRejectBadScopeAndSource(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	actor, err := data.EnsureDisabledActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("HTTPBAD"), Name: stringPtr("HTTP bad audit")}, actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	task, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr("Target")}, actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	columns, err := data.ListColumns(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	identity := auth.Identity{Actor: actor}
	runResponse := callAuditCollection(t, identity, server.audits, http.MethodPost, "/api/v1/projects/"+project.ID+"/audits", map[string]any{"scope": "board"}, nil)
	if runResponse.Code != http.StatusCreated {
		t.Fatalf("create run = %d %s", runResponse.Code, runResponse.Body.String())
	}
	var run store.AuditRun
	if err := json.Unmarshal(runResponse.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"malformed json":       `{"task_id":`,
		"wrong evidence shape": `{"task_id":"` + task.ID + `","captured_version":1,"source_column":"` + columns[0].ID + `","verdict":"correct","confidence":0.5,"reason":"safe","evidence_refs":"not-an-array"}`,
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/audits/"+run.ID+"/findings", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", "http://roadmap.test")
			response := httptest.NewRecorder()
			server.auditFindings(response, req, identity, run.ID)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("invalid finding body = %d %s, want 400", response.Code, response.Body.String())
			}
		})
	}
	badSource := callAuditResource(t, identity, server.auditFindings, http.MethodPost, "/api/v1/audits/"+run.ID+"/findings", run.ID, map[string]any{
		"task_id": task.ID, "captured_version": task.Version, "source_column": columns[0].Name,
		"verdict": "correct", "confidence": 0.5, "reason": "invalid source",
	}, nil)
	if badSource.Code != http.StatusBadRequest {
		t.Fatalf("bad source = %d %s", badSource.Code, badSource.Body.String())
	}
	for _, status := range []string{"finalized", "done"} {
		bad := callAuditResource(t, identity, server.auditFinalize, http.MethodPost, "/api/v1/audits/"+run.ID+"/finalize", run.ID, map[string]any{"status": status}, nil)
		if bad.Code != http.StatusBadRequest {
			t.Fatalf("bad terminal status %q = %d %s", status, bad.Code, bad.Body.String())
		}
	}

	// A token with no project/task/audit read or write scope is denied before
	// the project lookup, preserving the least-privilege route boundary.
	noScope := auth.Identity{Actor: actor, IsToken: true, Token: store.AuthToken{Scopes: map[string]bool{}}}
	denied := callAuditCollection(t, noScope, server.audits, http.MethodGet, "/api/v1/projects/"+project.ID+"/audits", nil, nil)
	if denied.Code != http.StatusForbidden || errorCode(t, denied) != "insufficient_scope" {
		t.Fatalf("no-scope read = %d %s", denied.Code, denied.Body.String())
	}
}

func TestAuditLifecycleMetadataDoesNotReserveAgentMutationBudget(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	owner, err := data.EnsureDisabledActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("AUDITBUDGET"), Name: stringPtr("Audit budget")}, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := data.CreateAgent(ctx, store.Actor{Kind: "agent", Name: "audit agent", ProjectIDs: []string{project.ID}}, owner.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	_, plaintext, err := data.CreateTokenBy(ctx, agent.ID, owner.ID, "audit", []string{"tasks:read", "tasks:write"}, []string{project.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	token, err := data.LookupToken(ctx, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	identity := auth.Identity{Actor: token.Actor, IsToken: true, Token: token}

	before, err := data.AgentMutationUsage(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	created := callAuditCollection(t, identity, server.audits, http.MethodPost, "/api/v1/projects/"+project.ID+"/audits", map[string]any{"scope": "board"}, map[string]string{"Idempotency-Key": "audit-create-budget"})
	if created.Code != http.StatusCreated {
		t.Fatalf("create audit = %d %s", created.Code, created.Body.String())
	}
	var run store.AuditRun
	if err := json.Unmarshal(created.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	finalized := callAuditResource(t, identity, server.auditFinalize, http.MethodPost, "/api/v1/audits/"+run.ID+"/finalize", run.ID, map[string]any{"status": "complete"}, map[string]string{"Idempotency-Key": "audit-finalize-budget"})
	if finalized.Code != http.StatusOK {
		t.Fatalf("finalize audit = %d %s", finalized.Code, finalized.Body.String())
	}
	after, err := data.AgentMutationUsage(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("audit lifecycle reserved agent mutation budget: before=%d after=%d", before, after)
	}
}

func callAuditCollection(t *testing.T, identity auth.Identity, handler func(http.ResponseWriter, *http.Request, auth.Identity, string, bool), method, target string, payload any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var body []byte
	if payload != nil {
		body, _ = json.Marshal(payload)
	}
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions {
		req.Header.Set("Origin", "http://roadmap.test")
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	result := httptest.NewRecorder()
	parts := splitPath(strings.TrimPrefix(target, "/api/v1"))
	projectRoute := len(parts) >= 3 && parts[0] == "projects"
	projectReference := ""
	if projectRoute {
		projectReference = parts[1]
	}
	handler(result, req, identity, projectReference, projectRoute)
	return result
}

func callAuditResource(t *testing.T, identity auth.Identity, handler func(http.ResponseWriter, *http.Request, auth.Identity, string), method, target, reference string, payload any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var body []byte
	if payload != nil {
		body, _ = json.Marshal(payload)
	}
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions {
		req.Header.Set("Origin", "http://roadmap.test")
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	result := httptest.NewRecorder()
	handler(result, req, identity, reference)
	return result
}
