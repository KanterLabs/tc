package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"roadmap/internal/store"
)

type dependencyHTTPFixture struct {
	server       *Server
	data         *store.Store
	actor        store.Actor
	project      store.Project
	dependent    store.Task
	prerequisite store.Task
}

func newDependencyHTTPFixture(t *testing.T, key string) dependencyHTTPFixture {
	t.Helper()
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	actor, err := data.EnsureDisabledActor(ctx)
	if err != nil {
		t.Fatalf("ensure disabled actor: %v", err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr(key), Name: stringPtr("Dependency " + key)}, actor.ID)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	dependent, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr("Dependent private title")}, actor.ID)
	if err != nil {
		t.Fatalf("create dependent task: %v", err)
	}
	prerequisite, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr("Prerequisite private title")}, actor.ID)
	if err != nil {
		t.Fatalf("create prerequisite task: %v", err)
	}
	return dependencyHTTPFixture{server: server, data: data, actor: actor, project: project, dependent: dependent, prerequisite: prerequisite}
}

func dependencyHTTPToken(t *testing.T, fixture dependencyHTTPFixture, name string, scopes []string, projects []string, kind string) (store.Actor, string) {
	t.Helper()
	if kind == "" {
		kind = "agent"
	}
	agent, err := fixture.data.CreateAgent(context.Background(), store.Actor{Kind: kind, Name: name, ProjectIDs: projects}, fixture.actor.ID, "")
	if err != nil {
		t.Fatalf("create %s: %v", kind, err)
	}
	_, token, err := fixture.data.CreateTokenBy(context.Background(), agent.ID, fixture.actor.ID, name+" token", scopes, projects, nil)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	return agent, token
}

func responseErrorCode(t *testing.T, body []byte) string {
	t.Helper()
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return envelope.Error.Code
}

func TestTaskDependenciesHTTPContractAndTimeline(t *testing.T) {
	fixture := newDependencyHTTPFixture(t, "DEPAPI")
	server := fixture.server
	dependent := fixture.dependent
	prerequisite := fixture.prerequisite

	initial := request(t, server, http.MethodGet, "/api/v1/tasks/"+dependent.ID+"/dependencies", nil, nil)
	if initial.Code != http.StatusOK || initial.Header().Get("ETag") != `"v1"` {
		t.Fatalf("initial dependency read: status=%d etag=%q body=%s", initial.Code, initial.Header().Get("ETag"), initial.Body.String())
	}
	var initialRelations store.TaskDependencies
	if err := json.Unmarshal(initial.Body.Bytes(), &initialRelations); err != nil {
		t.Fatalf("decode initial dependencies: %v", err)
	}
	if initialRelations.Prerequisites == nil || initialRelations.Dependents == nil || len(initialRelations.Prerequisites) != 0 || len(initialRelations.Dependents) != 0 {
		t.Fatalf("initial relations = %#v, want two empty arrays", initialRelations)
	}

	addHeaders := map[string]string{
		"Content-Type":    "application/json",
		"If-Match":        `"v1"`,
		"Idempotency-Key": "dependency-add-1",
	}
	added := request(t, server, http.MethodPost, "/api/v1/tasks/"+dependent.ID+"/dependencies", map[string]any{"prerequisite": prerequisite.Key}, addHeaders)
	if added.Code != http.StatusOK || added.Header().Get("ETag") != `"v2"` {
		t.Fatalf("add dependency: status=%d etag=%q body=%s", added.Code, added.Header().Get("ETag"), added.Body.String())
	}
	var updated store.Task
	if err := json.Unmarshal(added.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode updated task: %v", err)
	}
	if updated.ID != dependent.ID || updated.Version != 2 || updated.DependencySummary.PrerequisiteCount != 1 || updated.DependencySummary.UnmetPrerequisiteCount != 1 || !updated.DependencySummary.Blocked {
		t.Fatalf("updated task dependency summary = %#v", updated.DependencySummary)
	}

	replay := request(t, server, http.MethodPost, "/api/v1/tasks/"+dependent.ID+"/dependencies", map[string]any{"prerequisite": prerequisite.Key}, addHeaders)
	if replay.Code != added.Code || replay.Header().Get("ETag") != added.Header().Get("ETag") || replay.Body.String() != added.Body.String() {
		t.Fatalf("dependency replay changed response: first=%d/%q/%s replay=%d/%q/%s", added.Code, added.Header().Get("ETag"), added.Body.String(), replay.Code, replay.Header().Get("ETag"), replay.Body.String())
	}
	conflict := request(t, server, http.MethodPost, "/api/v1/tasks/"+dependent.ID+"/dependencies", map[string]any{"prerequisite": dependent.Key}, addHeaders)
	if conflict.Code != http.StatusConflict || responseErrorCode(t, conflict.Body.Bytes()) != "idempotency_key_reused" {
		t.Fatalf("same key/different body: status=%d code=%s body=%s", conflict.Code, responseErrorCode(t, conflict.Body.Bytes()), conflict.Body.String())
	}

	read := request(t, server, http.MethodGet, "/api/v1/tasks/"+strings.ToLower(dependent.Key)+"/dependencies", nil, nil)
	if read.Code != http.StatusOK || read.Header().Get("ETag") != `"v2"` {
		t.Fatalf("dependency read by key: status=%d etag=%q body=%s", read.Code, read.Header().Get("ETag"), read.Body.String())
	}
	var relations store.TaskDependencies
	if err := json.Unmarshal(read.Body.Bytes(), &relations); err != nil {
		t.Fatalf("decode dependency relations: %v", err)
	}
	if len(relations.Prerequisites) != 1 || relations.Prerequisites[0].ID != prerequisite.ID || relations.Prerequisites[0].Key != prerequisite.Key || relations.Prerequisites[0].Title != prerequisite.Title || relations.Prerequisites[0].Satisfied {
		t.Fatalf("prerequisites = %#v", relations.Prerequisites)
	}
	if len(relations.Dependents) != 0 {
		t.Fatalf("dependents = %#v, want empty", relations.Dependents)
	}

	removeHeaders := map[string]string{
		"If-Match":        `"v2"`,
		"Idempotency-Key": "dependency-remove-1",
	}
	removed := request(t, server, http.MethodDelete, "/api/v1/tasks/"+dependent.ID+"/dependencies/"+prerequisite.Key, nil, removeHeaders)
	if removed.Code != http.StatusOK || removed.Header().Get("ETag") != `"v3"` {
		t.Fatalf("remove dependency: status=%d etag=%q body=%s", removed.Code, removed.Header().Get("ETag"), removed.Body.String())
	}
	var removedTask store.Task
	if err := json.Unmarshal(removed.Body.Bytes(), &removedTask); err != nil {
		t.Fatalf("decode removed task: %v", err)
	}
	if removedTask.Version != 3 || removedTask.DependencySummary != (store.DependencySummary{}) {
		t.Fatalf("removed task = %#v", removedTask)
	}

	timeline := request(t, server, http.MethodGet, "/api/v1/tasks/"+dependent.ID+"/timeline?kind=task_change", nil, nil)
	if timeline.Code != http.StatusOK {
		t.Fatalf("dependency timeline: status=%d body=%s", timeline.Code, timeline.Body.String())
	}
	var timelineEnvelope struct {
		Data []store.TaskTimelineItem `json:"data"`
	}
	if err := json.Unmarshal(timeline.Body.Bytes(), &timelineEnvelope); err != nil {
		t.Fatalf("decode timeline: %v", err)
	}
	addedEvents, removedEvents := 0, 0
	for _, item := range timelineEnvelope.Data {
		if item.Change == nil {
			continue
		}
		switch item.Change.EventType {
		case "task.dependency_added":
			addedEvents++
		case "task.dependency_removed":
			removedEvents++
		}
	}
	if addedEvents != 1 || removedEvents != 1 {
		t.Fatalf("dependency timeline events = added %d removed %d, want 1/1: %#v", addedEvents, removedEvents, timelineEnvelope.Data)
	}

	removeReplay := request(t, server, http.MethodDelete, "/api/v1/tasks/"+dependent.ID+"/dependencies/"+prerequisite.Key, nil, removeHeaders)
	if removeReplay.Code != removed.Code || removeReplay.Header().Get("ETag") != removed.Header().Get("ETag") || removeReplay.Body.String() != removed.Body.String() {
		t.Fatalf("remove replay changed response: first=%d/%q/%s replay=%d/%q/%s", removed.Code, removed.Header().Get("ETag"), removed.Body.String(), removeReplay.Code, removeReplay.Header().Get("ETag"), removeReplay.Body.String())
	}
}

func TestTaskDependenciesHTTPHeadersScopesAndProjectCeiling(t *testing.T) {
	fixture := newDependencyHTTPFixture(t, "DEPSCOPE")
	ctx := context.Background()
	otherProject, err := fixture.data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("DEPOUT"), Name: stringPtr("Outside project")}, fixture.actor.ID)
	if err != nil {
		t.Fatalf("create outside project: %v", err)
	}
	outside, err := fixture.data.CreateTask(ctx, otherProject.ID, store.TaskInput{Title: stringPtr("Outside private title")}, fixture.actor.ID)
	if err != nil {
		t.Fatalf("create outside task: %v", err)
	}

	missingVersion := request(t, fixture.server, http.MethodPost, "/api/v1/tasks/"+fixture.dependent.ID+"/dependencies", map[string]any{"prerequisite": fixture.prerequisite.Key}, map[string]string{"Idempotency-Key": "missing-version", "Content-Type": "application/json"})
	if missingVersion.Code != http.StatusPreconditionRequired || responseErrorCode(t, missingVersion.Body.Bytes()) != "if_match_required" {
		t.Fatalf("missing If-Match: status=%d body=%s", missingVersion.Code, missingVersion.Body.String())
	}
	missingKey := request(t, fixture.server, http.MethodPost, "/api/v1/tasks/"+fixture.dependent.ID+"/dependencies", map[string]any{"prerequisite": fixture.prerequisite.Key}, map[string]string{"If-Match": `"v1"`, "Content-Type": "application/json"})
	if missingKey.Code != http.StatusBadRequest || responseErrorCode(t, missingKey.Body.Bytes()) != "idempotency_required" {
		t.Fatalf("missing Idempotency-Key: status=%d body=%s", missingKey.Code, missingKey.Body.String())
	}
	unknownField := request(t, fixture.server, http.MethodPost, "/api/v1/tasks/"+fixture.dependent.ID+"/dependencies", map[string]any{"prerequisite": fixture.prerequisite.Key, "unexpected": true}, map[string]string{
		"If-Match":        `"v1"`,
		"Idempotency-Key": "unknown-field",
		"Content-Type":    "application/json",
	})
	if unknownField.Code != http.StatusBadRequest || responseErrorCode(t, unknownField.Body.Bytes()) != "invalid_json" {
		t.Fatalf("unknown dependency field: status=%d body=%s", unknownField.Code, unknownField.Body.String())
	}

	readTokenActor, readToken := dependencyHTTPToken(t, fixture, "read only", []string{"tasks:read"}, []string{fixture.project.ID}, "agent")
	_ = readTokenActor
	readOnlyPost := request(t, fixture.server, http.MethodPost, "/api/v1/tasks/"+fixture.dependent.ID+"/dependencies", map[string]any{"prerequisite": fixture.prerequisite.Key}, map[string]string{
		"Authorization":   "Bearer " + readToken,
		"Content-Type":    "application/json",
		"If-Match":        `"v1"`,
		"Idempotency-Key": "read-only-post",
	})
	if readOnlyPost.Code != http.StatusForbidden || responseErrorCode(t, readOnlyPost.Body.Bytes()) != "insufficient_scope" {
		t.Fatalf("read-only POST: status=%d body=%s", readOnlyPost.Code, readOnlyPost.Body.String())
	}

	_, writeToken := dependencyHTTPToken(t, fixture, "write only", []string{"tasks:write"}, []string{fixture.project.ID}, "agent")
	writeOnlyGet := request(t, fixture.server, http.MethodGet, "/api/v1/tasks/"+fixture.dependent.ID+"/dependencies", nil, map[string]string{"Authorization": "Bearer " + writeToken})
	if writeOnlyGet.Code != http.StatusForbidden || responseErrorCode(t, writeOnlyGet.Body.Bytes()) != "insufficient_scope" {
		t.Fatalf("write-only GET: status=%d body=%s", writeOnlyGet.Code, writeOnlyGet.Body.String())
	}
	writeOnlyPost := request(t, fixture.server, http.MethodPost, "/api/v1/tasks/"+fixture.dependent.ID+"/dependencies", map[string]any{"prerequisite": fixture.prerequisite.Key}, map[string]string{
		"Authorization":   "Bearer " + writeToken,
		"Content-Type":    "application/json",
		"If-Match":        `"v1"`,
		"Idempotency-Key": "write-only-post",
	})
	if writeOnlyPost.Code != http.StatusOK || writeOnlyPost.Header().Get("ETag") != `"v2"` {
		t.Fatalf("write-only POST: status=%d etag=%q body=%s", writeOnlyPost.Code, writeOnlyPost.Header().Get("ETag"), writeOnlyPost.Body.String())
	}
	var reduced map[string]any
	if err := json.Unmarshal(writeOnlyPost.Body.Bytes(), &reduced); err != nil {
		t.Fatalf("decode write-only response: %v", err)
	}
	if len(reduced) != 2 || reduced["id"] != fixture.dependent.ID || reduced["version"] != float64(2) || strings.Contains(writeOnlyPost.Body.String(), fixture.dependent.Title) {
		t.Fatalf("write-only response leaked or changed shape: %#v body=%s", reduced, writeOnlyPost.Body.String())
	}

	outsideCeiling := request(t, fixture.server, http.MethodPost, "/api/v1/tasks/"+fixture.dependent.ID+"/dependencies", map[string]any{"prerequisite": outside.Key}, map[string]string{
		"Authorization":   "Bearer " + writeToken,
		"Content-Type":    "application/json",
		"If-Match":        `"v2"`,
		"Idempotency-Key": "outside-ceiling",
	})
	if outsideCeiling.Code != http.StatusNotFound || responseErrorCode(t, outsideCeiling.Body.Bytes()) != "not_found" || strings.Contains(outsideCeiling.Body.String(), outside.Key) || strings.Contains(outsideCeiling.Body.String(), outside.Title) || strings.Contains(outsideCeiling.Body.String(), outside.ID) {
		t.Fatalf("outside-ceiling prerequisite leaked: status=%d body=%s", outsideCeiling.Code, outsideCeiling.Body.String())
	}

	_, bothToken := dependencyHTTPToken(t, fixture, "both projects", []string{"tasks:write"}, []string{fixture.project.ID, otherProject.ID}, "agent")
	crossProject := request(t, fixture.server, http.MethodPost, "/api/v1/tasks/"+fixture.dependent.ID+"/dependencies", map[string]any{"prerequisite": outside.ID}, map[string]string{
		"Authorization":   "Bearer " + bothToken,
		"Content-Type":    "application/json",
		"If-Match":        `"v2"`,
		"Idempotency-Key": "cross-project",
	})
	if crossProject.Code != http.StatusBadRequest || responseErrorCode(t, crossProject.Body.Bytes()) != "dependency_cross_project" || strings.Contains(crossProject.Body.String(), outside.Title) || strings.Contains(crossProject.Body.String(), outside.Key) || strings.Contains(crossProject.Body.String(), outside.ID) {
		t.Fatalf("cross-project dependency: status=%d body=%s", crossProject.Code, crossProject.Body.String())
	}

	badDependency := request(t, fixture.server, http.MethodPost, "/api/v1/tasks/"+fixture.dependent.ID+"/dependencies", map[string]any{"prerequisite": fixture.dependent.Key}, map[string]string{
		"Authorization":   "Bearer " + writeToken,
		"Content-Type":    "application/json",
		"If-Match":        `"v2"`,
		"Idempotency-Key": "self-reference",
	})
	if badDependency.Code != http.StatusBadRequest || responseErrorCode(t, badDependency.Body.Bytes()) != "dependency_self_reference" || strings.Contains(badDependency.Body.String(), fixture.dependent.Title) || strings.Contains(badDependency.Body.String(), fixture.dependent.Key) {
		t.Fatalf("redacted self-reference: status=%d body=%s", badDependency.Code, badDependency.Body.String())
	}
}

func TestTaskDependenciesHTTPClaimOwnershipAdminOverrideAndRejectedEvents(t *testing.T) {
	fixture := newDependencyHTTPFixture(t, "DEPCLAIM")
	ctx := context.Background()
	completedPrerequisite, err := fixture.data.CompleteTask(ctx, fixture.prerequisite.ID, fixture.actor.ID, fixture.prerequisite.Version)
	if err != nil {
		t.Fatalf("complete prerequisite: %v", err)
	}
	foreign, _ := dependencyHTTPToken(t, fixture, "foreign claimant", []string{"tasks:write"}, []string{fixture.project.ID}, "agent")
	claimed, err := fixture.data.ClaimTask(ctx, fixture.dependent.ID, foreign.ID, time.Hour, fixture.dependent.Version)
	if err != nil {
		t.Fatalf("claim dependent: %v", err)
	}

	beforeEvents := dependencyEventCount(t, fixture.data, fixture.dependent.ID)
	otherWriterActor, otherWriter := dependencyHTTPToken(t, fixture, "other writer", []string{"tasks:write"}, []string{fixture.project.ID}, "agent")
	if _, err := fixture.data.DB.ExecContext(ctx, `UPDATE actors SET admin=1 WHERE id=?`, otherWriterActor.ID); err != nil {
		t.Fatalf("mark bearer actor admin for override test: %v", err)
	}
	foreignWrite := request(t, fixture.server, http.MethodPost, "/api/v1/tasks/"+claimed.ID+"/dependencies", map[string]any{"prerequisite": completedPrerequisite.ID}, map[string]string{
		"Authorization":   "Bearer " + otherWriter,
		"Content-Type":    "application/json",
		"If-Match":        `"v2"`,
		"Idempotency-Key": "foreign-claim-write",
	})
	if foreignWrite.Code != http.StatusConflict || responseErrorCode(t, foreignWrite.Body.Bytes()) != "task_already_claimed" {
		t.Fatalf("foreign claim write: status=%d body=%s", foreignWrite.Code, foreignWrite.Body.String())
	}
	if got := dependencyEventCount(t, fixture.data, fixture.dependent.ID); got != beforeEvents {
		t.Fatalf("rejected claim write changed event count from %d to %d", beforeEvents, got)
	}

	ownerWrite := request(t, fixture.server, http.MethodPost, "/api/v1/tasks/"+claimed.ID+"/dependencies", map[string]any{"prerequisite": completedPrerequisite.ID}, map[string]string{
		"Authorization":   "Bearer " + tokenForActor(t, fixture, foreign.ID, "owner-token", []string{"tasks:write"}),
		"Content-Type":    "application/json",
		"If-Match":        `"v2"`,
		"Idempotency-Key": "owner-claim-write",
	})
	if ownerWrite.Code != http.StatusOK || ownerWrite.Header().Get("ETag") != `"v3"` {
		t.Fatalf("claim owner write: status=%d etag=%q body=%s", ownerWrite.Code, ownerWrite.Header().Get("ETag"), ownerWrite.Body.String())
	}
	ownerRemove := request(t, fixture.server, http.MethodDelete, "/api/v1/tasks/"+claimed.ID+"/dependencies/"+completedPrerequisite.ID, nil, map[string]string{
		"Authorization":   "Bearer " + tokenForActor(t, fixture, foreign.ID, "owner-remove-token", []string{"tasks:write"}),
		"If-Match":        `"v3"`,
		"Idempotency-Key": "owner-claim-remove",
	})
	if ownerRemove.Code != http.StatusOK || ownerRemove.Header().Get("ETag") != `"v4"` {
		t.Fatalf("claim owner remove: status=%d etag=%q body=%s", ownerRemove.Code, ownerRemove.Header().Get("ETag"), ownerRemove.Body.String())
	}

	adminTask, err := fixture.data.CreateTask(ctx, fixture.project.ID, store.TaskInput{Title: stringPtr("Admin override target")}, fixture.actor.ID)
	if err != nil {
		t.Fatalf("create admin target: %v", err)
	}
	adminClaimed, err := fixture.data.ClaimTask(ctx, adminTask.ID, foreign.ID, time.Hour, adminTask.Version)
	if err != nil {
		t.Fatalf("claim admin target: %v", err)
	}
	adminWrite := request(t, fixture.server, http.MethodPost, "/api/v1/tasks/"+adminClaimed.ID+"/dependencies", map[string]any{"prerequisite": completedPrerequisite.ID}, map[string]string{
		"Content-Type":    "application/json",
		"If-Match":        `"v2"`,
		"Idempotency-Key": "admin-claim-write",
	})
	if adminWrite.Code != http.StatusOK || adminWrite.Header().Get("ETag") != `"v3"` {
		t.Fatalf("human admin override: status=%d etag=%q body=%s", adminWrite.Code, adminWrite.Header().Get("ETag"), adminWrite.Body.String())
	}
}

func tokenForActor(t *testing.T, fixture dependencyHTTPFixture, actorID, name string, scopes []string) string {
	t.Helper()
	_, token, err := fixture.data.CreateTokenBy(context.Background(), actorID, fixture.actor.ID, name, scopes, []string{fixture.project.ID}, nil)
	if err != nil {
		t.Fatalf("create actor token: %v", err)
	}
	return token
}

func dependencyEventCount(t *testing.T, data *store.Store, taskID string) int {
	t.Helper()
	var count int
	if err := data.DB.QueryRowContext(context.Background(), `SELECT COUNT(1) FROM events WHERE task_id=? AND type IN ('task.dependency_added', 'task.dependency_removed')`, taskID).Scan(&count); err != nil {
		t.Fatalf("count dependency events: %v", err)
	}
	return count
}

func TestTaskDependencyFilterParsing(t *testing.T) {
	fixture := newDependencyHTTPFixture(t, "DEPFILTER")
	blocked := request(t, fixture.server, http.MethodGet, "/api/v1/projects/"+fixture.project.ID+"/tasks?dependency=blocked", nil, nil)
	if blocked.Code != http.StatusOK {
		t.Fatalf("blocked filter status=%d body=%s", blocked.Code, blocked.Body.String())
	}
	invalid := request(t, fixture.server, http.MethodGet, "/api/v1/projects/"+fixture.project.ID+"/tasks?dependency=unknown", nil, nil)
	if invalid.Code != http.StatusBadRequest || !strings.Contains(invalid.Body.String(), "dependency is invalid") {
		t.Fatalf("invalid task dependency filter: status=%d body=%s", invalid.Code, invalid.Body.String())
	}
	myWorkInvalid := request(t, fixture.server, http.MethodGet, "/api/v1/my-work?dependency=unknown", nil, nil)
	if myWorkInvalid.Code != http.StatusBadRequest || !strings.Contains(myWorkInvalid.Body.String(), "dependency is invalid") {
		t.Fatalf("invalid my-work dependency filter: status=%d body=%s", myWorkInvalid.Code, myWorkInvalid.Body.String())
	}
}

func TestTaskDependencyErrorMappingSentinels(t *testing.T) {
	server, _ := testServer(t, "disabled")
	for _, test := range []struct {
		name string
		err  error
		code string
	}{
		{name: "self", err: &store.Error{Kind: store.ErrDependencySelfReference, Message: "self"}, code: "dependency_self_reference"},
		{name: "cross", err: &store.Error{Kind: store.ErrDependencyCrossProject, Message: "cross"}, code: "dependency_cross_project"},
		{name: "duplicate", err: &store.Error{Kind: store.ErrDependencyAlreadyExists, Message: "duplicate"}, code: "dependency_already_exists"},
		{name: "limit", err: &store.Error{Kind: store.ErrDependencyLimitExceeded, Message: "limit"}, code: "dependency_limit_exceeded"},
		{name: "cycle", err: &store.Error{Kind: store.ErrDependencyCycle, Message: "cycle"}, code: "dependency_cycle"},
		{name: "missing", err: &store.Error{Kind: store.ErrDependencyNotFound, Message: "missing"}, code: "dependency_not_found"},
		{name: "unmet", err: &store.Error{Kind: store.ErrUnmetDependencies, Message: "unmet"}, code: "unmet_dependencies"},
		{name: "in use", err: &store.Error{Kind: store.ErrDependencyInUse, Message: "in use"}, code: "dependency_in_use"},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			server.writeStoreError(recorder, test.err)
			if recorder.Code != expectedDependencyHTTPStatus(test.code) || responseErrorCode(t, recorder.Body.Bytes()) != test.code {
				t.Fatalf("error mapping = status %d body %s", recorder.Code, recorder.Body.String())
			}
			if errors.Is(test.err, store.ErrDependencySelfReference) && responseErrorCode(t, recorder.Body.Bytes()) != "dependency_self_reference" {
				t.Fatal("self sentinel was not preserved")
			}
		})
	}
}

func expectedDependencyHTTPStatus(code string) int {
	if code == "dependency_already_exists" || code == "dependency_cycle" || code == "unmet_dependencies" || code == "dependency_in_use" {
		return http.StatusConflict
	}
	if code == "dependency_not_found" {
		return http.StatusNotFound
	}
	return http.StatusBadRequest
}
