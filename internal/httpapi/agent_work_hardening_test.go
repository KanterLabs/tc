package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KanterLabs/helm/internal/store"
)

type agentWorkHTTPFixture struct {
	server  *Server
	data    *store.Store
	actor   store.Actor
	project store.Project
}

func newAgentWorkHTTPFixture(t *testing.T, key string) agentWorkHTTPFixture {
	t.Helper()
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	actor, err := data.EnsureDisabledActor(ctx)
	if err != nil {
		t.Fatalf("ensure disabled actor: %v", err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr(key), Name: stringPtr("Agent work " + key)}, actor.ID)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	return agentWorkHTTPFixture{server: server, data: data, actor: actor, project: project}
}

func (f agentWorkHTTPFixture) createTask(t *testing.T, title string, assignee string) store.Task {
	t.Helper()
	input := store.TaskInput{Title: stringPtr(title)}
	if assignee != "" {
		input.Assignee = stringPtr(assignee)
		input.AssigneeSet = true
	}
	task, err := f.data.CreateTask(context.Background(), f.project.ID, input, f.actor.ID)
	if err != nil {
		t.Fatalf("create task %q: %v", title, err)
	}
	return task
}

func agentWorkHTTPPayload(operationID, state, summary string) map[string]any {
	return map[string]any{
		"operation_id": operationID,
		"state":        state,
		"phase":        "phase-" + state,
		"summary":      summary,
		"next_action":  "next-" + state,
	}
}

func agentWorkHTTPHeaders(version int64, extra map[string]string) map[string]string {
	headers := map[string]string{
		"Content-Type": "application/json",
		"If-Match":     fmt.Sprintf(`"v%d"`, version),
	}
	for name, value := range extra {
		headers[name] = value
	}
	return headers
}

func publishAgentWorkHTTP(t *testing.T, fixture agentWorkHTTPFixture, task store.Task, state, summary string, extraHeaders map[string]string) store.Task {
	t.Helper()
	response := request(t, fixture.server, http.MethodPost, "/api/v1/tasks/"+task.ID+"/progress", agentWorkHTTPPayload("http-hardening/"+task.ID, state, summary), agentWorkHTTPHeaders(task.Version, extraHeaders))
	if response.Code != http.StatusOK {
		t.Fatalf("publish %s for %s = %d %s", state, task.ID, response.Code, response.Body.String())
	}
	var updated store.Task
	if err := json.Unmarshal(response.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode publish %s for %s: %v", state, task.ID, err)
	}
	return updated
}

func decodeAgentWorkTaskCollection(t *testing.T, response interface{ Result() *http.Response }) []store.Task {
	t.Helper()
	result := response.Result()
	defer result.Body.Close()
	var collection struct {
		Data []store.Task `json:"data"`
	}
	if err := json.NewDecoder(result.Body).Decode(&collection); err != nil {
		t.Fatalf("decode task collection: %v", err)
	}
	return collection.Data
}

func assertAgentWorkTaskSet(t *testing.T, tasks []store.Task, want ...string) {
	t.Helper()
	wantSet := make(map[string]bool, len(want))
	for _, id := range want {
		wantSet[id] = true
	}
	gotSet := make(map[string]bool, len(tasks))
	for _, task := range tasks {
		gotSet[task.ID] = true
	}
	if len(gotSet) != len(wantSet) {
		t.Fatalf("task IDs = %v, want %v", gotSet, wantSet)
	}
	for id := range wantSet {
		if !gotSet[id] {
			t.Fatalf("task IDs = %v, want %v", gotSet, wantSet)
		}
	}
}

func TestAgentWorkHTTPPublishesAllFourStates(t *testing.T) {
	fixture := newAgentWorkHTTPFixture(t, "STATES")
	task := fixture.createTask(t, "all publish states", "")
	claimed, err := fixture.data.ClaimTask(context.Background(), task.ID, fixture.actor.ID, time.Hour, task.Version)
	if err != nil {
		t.Fatalf("claim task: %v", err)
	}
	task = claimed

	for _, state := range []string{"working", "waiting", "verifying", "handoff"} {
		wantVersion := task.Version + 1
		response := request(t, fixture.server, http.MethodPost, "/api/v1/tasks/"+task.ID+"/progress", agentWorkHTTPPayload("http-hardening/states", state, "published "+state), agentWorkHTTPHeaders(task.Version, nil))
		if response.Code != http.StatusOK || response.Header().Get("ETag") != fmt.Sprintf(`"v%d"`, wantVersion) {
			t.Fatalf("publish state %s = %d etag=%q body=%s", state, response.Code, response.Header().Get("ETag"), response.Body.String())
		}
		var updated store.Task
		if err := json.Unmarshal(response.Body.Bytes(), &updated); err != nil {
			t.Fatalf("decode state %s response: %v", state, err)
		}
		if updated.Version != wantVersion || updated.AgentWork == nil || updated.AgentWork.State != state {
			t.Fatalf("state %s response task = version %d work=%+v, want version %d", state, updated.Version, updated.AgentWork, wantVersion)
		}
		task = updated
	}
}

func TestAgentWorkHTTPRejectsMissingExpiredAndForeignClaims(t *testing.T) {
	fixture := newAgentWorkHTTPFixture(t, "CLAIMS")
	ctx := context.Background()

	missing := fixture.createTask(t, "missing claim", "")
	expired := fixture.createTask(t, "expired claim", "")
	claimedExpired, err := fixture.data.ClaimTask(ctx, expired.ID, fixture.actor.ID, time.Hour, expired.Version)
	if err != nil {
		t.Fatalf("claim expired task: %v", err)
	}
	expired = claimedExpired
	if _, err := fixture.data.DB.ExecContext(ctx, `UPDATE tasks SET claim_expires_at=? WHERE id=?`, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), expired.ID); err != nil {
		t.Fatalf("expire claim: %v", err)
	}

	foreign, err := fixture.data.CreateActor(ctx, store.Actor{Kind: "agent", Name: "foreign claimant"}, "")
	if err != nil {
		t.Fatalf("create foreign actor: %v", err)
	}
	foreignTask := fixture.createTask(t, "foreign claim", "")
	claimedForeign, err := fixture.data.ClaimTask(ctx, foreignTask.ID, foreign.ID, time.Hour, foreignTask.Version)
	if err != nil {
		t.Fatalf("claim foreign task: %v", err)
	}
	foreignTask = claimedForeign

	for _, test := range []struct {
		name string
		task store.Task
	}{
		{name: "missing", task: missing},
		{name: "expired", task: expired},
		{name: "foreign", task: foreignTask},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := request(t, fixture.server, http.MethodPost, "/api/v1/tasks/"+test.task.ID+"/progress", agentWorkHTTPPayload("http-hardening/claim-rejection", "working", "must not publish"), agentWorkHTTPHeaders(test.task.Version, nil))
			if response.Code != http.StatusForbidden || errorCode(t, response) != "forbidden" {
				t.Fatalf("%s claim rejection = %d %s, want 403 forbidden", test.name, response.Code, response.Body.String())
			}
			current, err := fixture.data.GetTask(ctx, test.task.ID)
			if err != nil {
				t.Fatalf("read %s task: %v", test.name, err)
			}
			if current.Version != test.task.Version || current.AgentWork != nil {
				t.Fatalf("%s rejection mutated task: version=%d work=%+v", test.name, current.Version, current.AgentWork)
			}
		})
	}
}

func TestAgentWorkHTTPRejectsCompletedTaskAsConflictNotStaleETag(t *testing.T) {
	fixture := newAgentWorkHTTPFixture(t, "COMPLETE")
	ctx := context.Background()
	task := fixture.createTask(t, "completed task", "")
	claimed, err := fixture.data.ClaimTask(ctx, task.ID, fixture.actor.ID, time.Hour, task.Version)
	if err != nil {
		t.Fatalf("claim task: %v", err)
	}
	completed, err := fixture.data.CompleteTaskWithClaim(ctx, claimed.ID, fixture.actor.ID, claimed.Version)
	if err != nil {
		t.Fatalf("complete task: %v", err)
	}
	response := request(t, fixture.server, http.MethodPost, "/api/v1/tasks/"+completed.ID+"/progress", agentWorkHTTPPayload("http-hardening/completed", "working", "must not publish"), agentWorkHTTPHeaders(completed.Version, nil))
	if response.Code != http.StatusConflict || errorCode(t, response) != "conflict" {
		t.Fatalf("completed progress = %d %s, want 409 conflict", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), `"code":"stale_task"`) {
		t.Fatalf("completed task was misreported as stale ETag: %s", response.Body.String())
	}
}

func TestAgentWorkHTTPStaleBoundaryAndCoordinationFilters(t *testing.T) {
	fixture := newAgentWorkHTTPFixture(t, "FILTERS")
	ctx := context.Background()
	boundary := fixture.createTask(t, "exact stale boundary", "")
	waiting := fixture.createTask(t, "waiting action", "")
	handoff := fixture.createTask(t, "handoff action", "")
	fresh := fixture.createTask(t, "fresh verifying", "")
	missing := fixture.createTask(t, "missing snapshot", "")

	for _, test := range []struct {
		task  store.Task
		state string
	}{
		{task: boundary, state: "working"},
		{task: waiting, state: "waiting"},
		{task: handoff, state: "handoff"},
		{task: fresh, state: "verifying"},
	} {
		claimed, err := fixture.data.ClaimTask(ctx, test.task.ID, fixture.actor.ID, time.Hour, test.task.Version)
		if err != nil {
			t.Fatalf("claim %s: %v", test.task.Title, err)
		}
		published := publishAgentWorkHTTP(t, fixture, claimed, test.state, test.task.Title, nil)
		if published.AgentWork == nil || published.AgentWork.State != test.state {
			t.Fatalf("published %s work = %+v", test.task.Title, published.AgentWork)
		}
		if test.task.ID == boundary.ID {
			boundary = published
		} else if test.task.ID == waiting.ID {
			waiting = published
		} else if test.task.ID == handoff.ID {
			handoff = published
		} else {
			fresh = published
		}
	}

	// The timestamp is deliberately written at the inclusive 15-minute
	// boundary. The subsequent HTTP read occurs later, so this remains a
	// deterministic stale snapshot rather than relying on a sleep.
	boundaryAt := time.Now().UTC().Add(-store.AgentWorkStaleAfter)
	if _, err := fixture.data.DB.ExecContext(ctx, `UPDATE task_agent_work SET updated_at=? WHERE task_id=?`, boundaryAt.Format(time.RFC3339Nano), boundary.ID); err != nil {
		t.Fatalf("set exact stale boundary: %v", err)
	}

	detail := request(t, fixture.server, http.MethodGet, "/api/v1/tasks/"+boundary.ID, nil, nil)
	if detail.Code != http.StatusOK {
		t.Fatalf("boundary task detail = %d %s", detail.Code, detail.Body.String())
	}
	var boundaryTask store.Task
	if err := json.Unmarshal(detail.Body.Bytes(), &boundaryTask); err != nil {
		t.Fatalf("decode boundary detail: %v", err)
	}
	if boundaryTask.AgentWork == nil || !boundaryTask.AgentWork.Stale || !boundaryTask.AgentWork.ActionNeeded {
		t.Fatalf("boundary liveness = %+v, want stale and action_needed", boundaryTask.AgentWork)
	}

	stale := request(t, fixture.server, http.MethodGet, "/api/v1/projects/"+fixture.project.ID+"/tasks?agent_state=stale", nil, nil)
	if stale.Code != http.StatusOK {
		t.Fatalf("stale filter = %d %s", stale.Code, stale.Body.String())
	}
	assertAgentWorkTaskSet(t, decodeAgentWorkTaskCollection(t, stale), boundary.ID)

	missingResponse := request(t, fixture.server, http.MethodGet, "/api/v1/projects/"+fixture.project.ID+"/tasks?agent_state=missing", nil, nil)
	if missingResponse.Code != http.StatusOK {
		t.Fatalf("missing filter = %d %s", missingResponse.Code, missingResponse.Body.String())
	}
	assertAgentWorkTaskSet(t, decodeAgentWorkTaskCollection(t, missingResponse), missing.ID)

	actionNeeded := request(t, fixture.server, http.MethodGet, "/api/v1/projects/"+fixture.project.ID+"/tasks?action_needed=true", nil, nil)
	if actionNeeded.Code != http.StatusOK {
		t.Fatalf("action-needed filter = %d %s", actionNeeded.Code, actionNeeded.Body.String())
	}
	assertAgentWorkTaskSet(t, decodeAgentWorkTaskCollection(t, actionNeeded), boundary.ID, waiting.ID, handoff.ID)

	actionNotNeeded := request(t, fixture.server, http.MethodGet, "/api/v1/projects/"+fixture.project.ID+"/tasks?action_needed=false", nil, nil)
	if actionNotNeeded.Code != http.StatusOK {
		t.Fatalf("action-needed=false filter = %d %s", actionNotNeeded.Code, actionNotNeeded.Body.String())
	}
	assertAgentWorkTaskSet(t, decodeAgentWorkTaskCollection(t, actionNotNeeded), boundary.ID, waiting.ID, handoff.ID, fresh.ID, missing.ID)
}

func TestAgentWorkHTTPMyWorkLiveAndAssignedViews(t *testing.T) {
	fixture := newAgentWorkHTTPFixture(t, "MYWORK")
	ctx := context.Background()
	assignedMissing := fixture.createTask(t, "assigned without pulse", fixture.actor.ID)
	assignedPulse := fixture.createTask(t, "assigned pulse", "")
	liveOnly := fixture.createTask(t, "foreign live pulse", "")

	claimedAssigned, err := fixture.data.ClaimTask(ctx, assignedPulse.ID, fixture.actor.ID, time.Hour, assignedPulse.Version)
	if err != nil {
		t.Fatalf("claim assigned pulse: %v", err)
	}
	assignedPulse = publishAgentWorkHTTP(t, fixture, claimedAssigned, "working", assignedPulse.Title, nil)

	foreign, err := fixture.data.CreateAgent(ctx, store.Actor{Kind: "agent", Name: "live-only agent", ProjectIDs: []string{fixture.project.ID}}, fixture.actor.ID, "")
	if err != nil {
		t.Fatalf("create live-only agent: %v", err)
	}
	_, token, err := fixture.data.CreateTokenBy(ctx, foreign.ID, fixture.actor.ID, "live-only progress", []string{"tasks:claim"}, []string{fixture.project.ID}, nil)
	if err != nil {
		t.Fatalf("create live-only token: %v", err)
	}
	claimedLiveOnly, err := fixture.data.ClaimTask(ctx, liveOnly.ID, foreign.ID, time.Hour, liveOnly.Version)
	if err != nil {
		t.Fatalf("claim live-only task: %v", err)
	}
	liveOnly = publishAgentWorkHTTP(t, fixture, claimedLiveOnly, "working", liveOnly.Title, map[string]string{"Authorization": "Bearer " + token})

	assigned := request(t, fixture.server, http.MethodGet, "/api/v1/my-work?view=assigned&project="+fixture.project.Key, nil, nil)
	if assigned.Code != http.StatusOK {
		t.Fatalf("assigned my-work = %d %s", assigned.Code, assigned.Body.String())
	}
	assertAgentWorkTaskSet(t, decodeAgentWorkTaskCollection(t, assigned), assignedMissing.ID, assignedPulse.ID)

	live := request(t, fixture.server, http.MethodGet, "/api/v1/my-work?view=live&project="+fixture.project.Key, nil, nil)
	if live.Code != http.StatusOK {
		t.Fatalf("live my-work = %d %s", live.Code, live.Body.String())
	}
	assertAgentWorkTaskSet(t, decodeAgentWorkTaskCollection(t, live), assignedPulse.ID, liveOnly.ID)
}

func TestAgentWorkHTTPConcurrentSameETagHasOneWinner(t *testing.T) {
	fixture := newAgentWorkHTTPFixture(t, "RACE")
	task := fixture.createTask(t, "one pulse winner", "")
	claimed, err := fixture.data.ClaimTask(context.Background(), task.ID, fixture.actor.ID, time.Hour, task.Version)
	if err != nil {
		t.Fatalf("claim race task: %v", err)
	}
	task = claimed

	type result struct {
		code int
		etag string
		body string
	}
	results := make(chan result, 2)
	var group sync.WaitGroup
	for _, state := range []string{"working", "verifying"} {
		state := state
		group.Add(1)
		go func() {
			defer group.Done()
			response := request(t, fixture.server, http.MethodPost, "/api/v1/tasks/"+task.ID+"/progress", agentWorkHTTPPayload("http-hardening/race/"+state, state, "race "+state), agentWorkHTTPHeaders(task.Version, nil))
			results <- result{code: response.Code, etag: response.Header().Get("ETag"), body: response.Body.String()}
		}()
	}
	group.Wait()
	close(results)

	successes, conflicts := 0, 0
	for response := range results {
		switch response.code {
		case http.StatusOK:
			successes++
			if response.etag != `"v3"` {
				t.Fatalf("winning ETag = %q, want v3", response.etag)
			}
		case http.StatusConflict:
			conflicts++
			var envelope struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(response.body), &envelope); err != nil {
				t.Fatalf("decode stale race response: %v", err)
			}
			if envelope.Error.Code != "stale_task" {
				t.Fatalf("race conflict code = %q, want stale_task; body=%s", envelope.Error.Code, response.body)
			}
		default:
			t.Fatalf("concurrent publish status = %d body=%s", response.code, response.body)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent publish outcomes = successes %d conflicts %d, want one each", successes, conflicts)
	}

	current, err := fixture.data.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("read race task: %v", err)
	}
	if current.Version != 3 || current.AgentWork == nil || current.CommentCount != 1 {
		t.Fatalf("race task final state = version %d work=%+v comments=%d, want v3/one comment", current.Version, current.AgentWork, current.CommentCount)
	}
	events, _, err := fixture.data.ListEvents(context.Background(), store.EventFilter{ProjectID: fixture.project.ID, Limit: 100})
	if err != nil {
		t.Fatalf("list race events: %v", err)
	}
	progressEvents, progressComments := 0, 0
	for _, event := range events {
		if event.TaskID == nil || *event.TaskID != task.ID {
			continue
		}
		switch event.Type {
		case "task.progressed":
			progressEvents++
		case "comment.created":
			progressComments++
		}
	}
	if progressEvents != 1 || progressComments != 1 {
		t.Fatalf("race side effects = progressed %d comments %d, want one each", progressEvents, progressComments)
	}
}

func TestAgentWorkHTTPRedactsNarrativeFromEventsReadOnlyToken(t *testing.T) {
	fixture := newAgentWorkHTTPFixture(t, "REDACT")
	ctx := context.Background()
	task := fixture.createTask(t, "redaction task", "")
	claimed, err := fixture.data.ClaimTask(ctx, task.ID, fixture.actor.ID, time.Hour, task.Version)
	if err != nil {
		t.Fatalf("claim redaction task: %v", err)
	}
	publishAgentWorkHTTP(t, fixture, claimed, "working", "public summary", nil)

	unsafePayload := `{"version":3,"state":"working","summary":"event-secret-summary","phase":"event-secret-phase","next_action":"event-secret-next","checkpoint_refs":["event-secret-ref"],"agent_work":{"summary":"nested-secret"}}`
	if _, err := fixture.data.DB.ExecContext(ctx, `UPDATE events SET payload=? WHERE task_id=? AND type=?`, unsafePayload, task.ID, "task.progressed"); err != nil {
		t.Fatalf("seed unsafe progress event payload: %v", err)
	}
	readOnlyAgent, err := fixture.data.CreateAgent(ctx, store.Actor{Kind: "agent", Name: "event reader", ProjectIDs: []string{fixture.project.ID}}, fixture.actor.ID, "")
	if err != nil {
		t.Fatalf("create event reader: %v", err)
	}
	_, token, err := fixture.data.CreateTokenBy(ctx, readOnlyAgent.ID, fixture.actor.ID, "event reader", []string{"events:read"}, []string{fixture.project.ID}, nil)
	if err != nil {
		t.Fatalf("create event reader token: %v", err)
	}

	response := request(t, fixture.server, http.MethodGet, "/api/v1/events?project="+fixture.project.Key, nil, map[string]string{"Authorization": "Bearer " + token})
	if response.Code != http.StatusOK {
		t.Fatalf("read-only events = %d %s", response.Code, response.Body.String())
	}
	var collection struct {
		Data []store.Event `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &collection); err != nil {
		t.Fatalf("decode redacted events: %v", err)
	}
	found := false
	for _, event := range collection.Data {
		if event.Type != "task.progressed" || event.TaskID == nil || *event.TaskID != task.ID {
			continue
		}
		found = true
		payload := string(event.Payload)
		for _, secret := range []string{"event-secret-summary", "event-secret-phase", "event-secret-next", "event-secret-ref", "nested-secret"} {
			if strings.Contains(payload, secret) {
				t.Fatalf("redacted event leaked %q: %s", secret, payload)
			}
		}
		if !strings.Contains(payload, `"state":"working"`) {
			t.Fatalf("redacted event lost machine-readable state: %s", payload)
		}
	}
	if !found {
		t.Fatalf("redacted events did not include task.progressed for %s: %s", task.ID, response.Body.String())
	}

	// The roadmap embeds the same event records and must apply the identical
	// narrative redaction for an events:read token without tasks:read.
	roadmapReader, err := fixture.data.CreateAgent(ctx, store.Actor{Kind: "agent", Name: "roadmap event reader", ProjectIDs: []string{fixture.project.ID}}, fixture.actor.ID, "")
	if err != nil {
		t.Fatalf("create roadmap reader: %v", err)
	}
	_, roadmapToken, err := fixture.data.CreateTokenBy(ctx, roadmapReader.ID, fixture.actor.ID, "roadmap event reader", []string{"projects:read", "events:read"}, []string{fixture.project.ID}, nil)
	if err != nil {
		t.Fatalf("create roadmap reader token: %v", err)
	}
	roadmapResponse := request(t, fixture.server, http.MethodGet, "/api/v1/projects/"+fixture.project.Key+"/roadmap", nil, map[string]string{"Authorization": "Bearer " + roadmapToken})
	if roadmapResponse.Code != http.StatusOK {
		t.Fatalf("read-only roadmap = %d %s", roadmapResponse.Code, roadmapResponse.Body.String())
	}
	for _, secret := range []string{"event-secret-summary", "event-secret-phase", "event-secret-next", "event-secret-ref", "nested-secret"} {
		if strings.Contains(roadmapResponse.Body.String(), secret) {
			t.Fatalf("redacted roadmap leaked %q: %s", secret, roadmapResponse.Body.String())
		}
	}
}
