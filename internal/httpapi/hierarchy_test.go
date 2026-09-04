package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/KanterLabs/helm/internal/store"
)

func TestTaskHierarchyHTTPContractRollupsAndIdempotency(t *testing.T) {
	fixture := newDependencyHTTPFixture(t, "HIERAPI")
	parent := fixture.dependent
	child, err := fixture.data.CreateTask(context.Background(), fixture.project.ID, store.TaskInput{Title: stringPtr("Child private title")}, fixture.actor.ID)
	if err != nil {
		t.Fatalf("create hierarchy child: %v", err)
	}

	initial := request(t, fixture.server, http.MethodGet, "/api/v1/tasks/"+parent.ID+"/hierarchy", nil, nil)
	if initial.Code != http.StatusOK || initial.Header().Get("ETag") != `"v1"` {
		t.Fatalf("initial hierarchy = %d etag=%q body=%s", initial.Code, initial.Header().Get("ETag"), initial.Body.String())
	}
	var initialGraph store.TaskHierarchy
	if err := json.Unmarshal(initial.Body.Bytes(), &initialGraph); err != nil {
		t.Fatalf("decode initial hierarchy: %v", err)
	}
	if initialGraph.Children == nil || initialGraph.Ancestors == nil || initialGraph.Descendants == nil || len(initialGraph.Children) != 0 || initialGraph.Summary.ChildCount != 0 {
		t.Fatalf("initial hierarchy = %+v, want empty collections and rollup", initialGraph)
	}

	linkHeaders := map[string]string{
		"Content-Type":    "application/json",
		"If-Match":        `"v1"`,
		"Idempotency-Key": "hierarchy-link-1",
	}
	linked := request(t, fixture.server, http.MethodPost, "/api/v1/tasks/"+child.ID+"/parent", map[string]any{"parent_id": parent.Key}, linkHeaders)
	if linked.Code != http.StatusOK || linked.Header().Get("ETag") != `"v2"` {
		t.Fatalf("link hierarchy = %d etag=%q body=%s", linked.Code, linked.Header().Get("ETag"), linked.Body.String())
	}
	var linkedTask store.Task
	if err := json.Unmarshal(linked.Body.Bytes(), &linkedTask); err != nil {
		t.Fatalf("decode linked hierarchy task: %v", err)
	}
	if linkedTask.ParentTaskID == nil || *linkedTask.ParentTaskID != parent.ID || linkedTask.Parent == nil || linkedTask.Parent.ID != parent.ID {
		t.Fatalf("linked task parent = %#v/%#v, want %q", linkedTask.ParentTaskID, linkedTask.Parent, parent.ID)
	}
	replay := request(t, fixture.server, http.MethodPost, "/api/v1/tasks/"+child.ID+"/parent", map[string]any{"parent_id": parent.Key}, linkHeaders)
	if replay.Code != linked.Code || replay.Header().Get("ETag") != linked.Header().Get("ETag") || replay.Body.String() != linked.Body.String() {
		t.Fatalf("hierarchy replay changed response: first=%d/%q/%s replay=%d/%q/%s", linked.Code, linked.Header().Get("ETag"), linked.Body.String(), replay.Code, replay.Header().Get("ETag"), replay.Body.String())
	}

	children := request(t, fixture.server, http.MethodGet, "/api/v1/tasks/"+parent.Key+"/children", nil, nil)
	if children.Code != http.StatusOK {
		t.Fatalf("children read = %d body=%s", children.Code, children.Body.String())
	}
	var childCollection struct {
		Data       []store.TaskHierarchyReference `json:"data"`
		NextCursor *string                        `json:"next_cursor"`
	}
	if err := json.Unmarshal(children.Body.Bytes(), &childCollection); err != nil {
		t.Fatalf("decode child collection: %v", err)
	}
	if len(childCollection.Data) != 1 || childCollection.Data[0].ID != child.ID || childCollection.NextCursor == nil || *childCollection.NextCursor != "" {
		t.Fatalf("child collection = %+v, want one child and empty cursor", childCollection)
	}

	graphResponse := request(t, fixture.server, http.MethodGet, "/api/v1/tasks/"+child.ID+"/hierarchy", nil, nil)
	if graphResponse.Code != http.StatusOK || graphResponse.Header().Get("ETag") != `"v2"` {
		t.Fatalf("child hierarchy = %d etag=%q body=%s", graphResponse.Code, graphResponse.Header().Get("ETag"), graphResponse.Body.String())
	}
	var graph store.TaskHierarchy
	if err := json.Unmarshal(graphResponse.Body.Bytes(), &graph); err != nil {
		t.Fatalf("decode child graph: %v", err)
	}
	if graph.Parent == nil || graph.Parent.ID != parent.ID || len(graph.Ancestors) != 1 || graph.Ancestors[0].ID != parent.ID {
		t.Fatalf("child graph = %+v, want parent and ancestor", graph)
	}

	parentAfterLink := request(t, fixture.server, http.MethodGet, "/api/v1/tasks/"+parent.ID, nil, nil)
	if parentAfterLink.Code != http.StatusOK {
		t.Fatalf("parent read after link = %d body=%s", parentAfterLink.Code, parentAfterLink.Body.String())
	}
	var parentTask store.Task
	if err := json.Unmarshal(parentAfterLink.Body.Bytes(), &parentTask); err != nil {
		t.Fatalf("decode parent after link: %v", err)
	}
	if parentTask.HierarchySummary.ChildCount != 1 || parentTask.HierarchySummary.CompletionPercent != 0 || parentTask.HierarchySummary.StateCounts["backlog"] != 1 {
		t.Fatalf("parent rollup = %+v, want one open backlog child", parentTask.HierarchySummary)
	}

	ancestors := request(t, fixture.server, http.MethodGet, "/api/v1/tasks/"+child.ID+"/ancestors", nil, nil)
	if ancestors.Code != http.StatusOK || !strings.Contains(ancestors.Body.String(), parent.ID) {
		t.Fatalf("ancestors = %d %s", ancestors.Code, ancestors.Body.String())
	}
	descendants := request(t, fixture.server, http.MethodGet, "/api/v1/tasks/"+parent.ID+"/descendants", nil, nil)
	if descendants.Code != http.StatusOK || !strings.Contains(descendants.Body.String(), child.ID) {
		t.Fatalf("descendants = %d %s", descendants.Code, descendants.Body.String())
	}

	clearHeaders := map[string]string{"If-Match": `"v2"`, "Idempotency-Key": "hierarchy-clear-1"}
	cleared := request(t, fixture.server, http.MethodDelete, "/api/v1/tasks/"+child.ID+"/parent", nil, clearHeaders)
	if cleared.Code != http.StatusOK || cleared.Header().Get("ETag") != `"v3"` {
		t.Fatalf("clear hierarchy = %d etag=%q body=%s", cleared.Code, cleared.Header().Get("ETag"), cleared.Body.String())
	}
	var clearedTask store.Task
	if err := json.Unmarshal(cleared.Body.Bytes(), &clearedTask); err != nil {
		t.Fatalf("decode cleared task: %v", err)
	}
	if clearedTask.ParentTaskID != nil || clearedTask.Parent != nil {
		t.Fatalf("cleared hierarchy parent = %#v/%#v, want nil", clearedTask.ParentTaskID, clearedTask.Parent)
	}
	parentEndpoint := request(t, fixture.server, http.MethodGet, "/api/v1/tasks/"+child.ID+"/parent", nil, nil)
	if parentEndpoint.Code != http.StatusOK || parentEndpoint.Body.String() != `{"parent":null}` {
		t.Fatalf("cleared parent endpoint = %d %s, want null", parentEndpoint.Code, parentEndpoint.Body.String())
	}

	timeline := request(t, fixture.server, http.MethodGet, "/api/v1/tasks/"+child.ID+"/timeline?kind=task_change", nil, nil)
	if timeline.Code != http.StatusOK {
		t.Fatalf("hierarchy timeline = %d body=%s", timeline.Code, timeline.Body.String())
	}
	if !strings.Contains(timeline.Body.String(), "task.parent_linked") || !strings.Contains(timeline.Body.String(), "task.parent_unlinked") {
		t.Fatalf("hierarchy timeline omitted relation events: %s", timeline.Body.String())
	}
}

func TestTaskHierarchyHTTPScopesAndMutationPreconditions(t *testing.T) {
	fixture := newDependencyHTTPFixture(t, "HIERSCOPE")
	child, err := fixture.data.CreateTask(context.Background(), fixture.project.ID, store.TaskInput{Title: stringPtr("Hierarchy child secret")}, fixture.actor.ID)
	if err != nil {
		t.Fatalf("create hierarchy child: %v", err)
	}
	readAgent, readToken := dependencyHTTPToken(t, fixture, "hierarchy read", []string{"tasks:read"}, []string{fixture.project.ID}, "agent")
	_ = readAgent
	read := request(t, fixture.server, http.MethodGet, "/api/v1/tasks/"+fixture.dependent.ID+"/hierarchy", nil, map[string]string{"Authorization": "Bearer " + readToken})
	if read.Code != http.StatusOK {
		t.Fatalf("scoped hierarchy read = %d body=%s", read.Code, read.Body.String())
	}
	writeAgent, writeToken := dependencyHTTPToken(t, fixture, "hierarchy write", []string{"tasks:write"}, []string{fixture.project.ID}, "agent")
	_ = writeAgent
	withoutVersion := request(t, fixture.server, http.MethodPost, "/api/v1/tasks/"+child.ID+"/parent", map[string]any{"parent": fixture.dependent.ID}, map[string]string{"Authorization": "Bearer " + writeToken, "Idempotency-Key": "hierarchy-no-etag"})
	if withoutVersion.Code != http.StatusPreconditionRequired || responseErrorCode(t, withoutVersion.Body.Bytes()) != "if_match_required" {
		t.Fatalf("missing hierarchy If-Match = %d body=%s", withoutVersion.Code, withoutVersion.Body.String())
	}
	missingIdempotency := request(t, fixture.server, http.MethodPost, "/api/v1/tasks/"+child.ID+"/parent", map[string]any{"parent": fixture.dependent.ID}, map[string]string{"Authorization": "Bearer " + writeToken, "If-Match": `"v1"`})
	if missingIdempotency.Code != http.StatusBadRequest || responseErrorCode(t, missingIdempotency.Body.Bytes()) != "idempotency_required" {
		t.Fatalf("missing hierarchy idempotency = %d body=%s", missingIdempotency.Code, missingIdempotency.Body.String())
	}
	writeOnly := request(t, fixture.server, http.MethodPost, "/api/v1/tasks/"+child.ID+"/parent", map[string]any{"parent": fixture.dependent.ID}, map[string]string{
		"Authorization":   "Bearer " + writeToken,
		"Content-Type":    "application/json",
		"If-Match":        `"v1"`,
		"Idempotency-Key": "hierarchy-write-only",
	})
	if writeOnly.Code != http.StatusOK || writeOnly.Header().Get("ETag") != `"v2"` || writeOnly.Body.String() != `{"id":"`+child.ID+`","version":2}` {
		t.Fatalf("write-only hierarchy mutation = %d etag=%q body=%s", writeOnly.Code, writeOnly.Header().Get("ETag"), writeOnly.Body.String())
	}
	for _, secret := range []string{child.ID, child.Key, child.Title, fixture.dependent.ID, fixture.dependent.Key, fixture.dependent.Title} {
		if strings.Contains(writeOnly.Body.String(), secret) && secret != child.ID {
			t.Fatalf("write-only hierarchy response leaked %q: %s", secret, writeOnly.Body.String())
		}
	}

	otherProject, err := fixture.data.CreateProject(context.Background(), store.ProjectInput{Key: stringPtr("HIEROTHER"), Name: stringPtr("Other")}, fixture.actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	otherTask, err := fixture.data.CreateTask(context.Background(), otherProject.ID, store.TaskInput{Title: stringPtr("Other")}, fixture.actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	foreign := request(t, fixture.server, http.MethodPost, "/api/v1/tasks/"+child.ID+"/parent", map[string]any{"parent": otherTask.ID}, map[string]string{
		"Authorization":   "Bearer " + writeToken,
		"Content-Type":    "application/json",
		"If-Match":        `"v2"`,
		"Idempotency-Key": "hierarchy-cross-project",
	})
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("cross-project scoped hierarchy parent = %d body=%s, want hidden 404", foreign.Code, foreign.Body.String())
	}
}

func TestTaskHierarchyHTTPRejectsUnknownFieldsAndDuplicateLinks(t *testing.T) {
	fixture := newDependencyHTTPFixture(t, "HIERINPUT")
	child, err := fixture.data.CreateTask(context.Background(), fixture.project.ID, store.TaskInput{Title: stringPtr("Child")}, fixture.actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	unknown := request(t, fixture.server, http.MethodPost, "/api/v1/tasks/"+child.ID+"/parent", map[string]any{"parent": fixture.dependent.ID, "extra": true}, map[string]string{"If-Match": `"v1"`, "Idempotency-Key": "hierarchy-unknown"})
	if unknown.Code != http.StatusBadRequest || responseErrorCode(t, unknown.Body.Bytes()) != "invalid_request" {
		t.Fatalf("unknown hierarchy field = %d body=%s", unknown.Code, unknown.Body.String())
	}
	linked, err := fixture.data.SetTaskParent(context.Background(), child.ID, fixture.dependent.ID, child.Version, fixture.actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := request(t, fixture.server, http.MethodPost, "/api/v1/tasks/"+child.ID+"/parent", map[string]any{"parent": fixture.dependent.ID}, map[string]string{"If-Match": `"v2"`, "Idempotency-Key": "hierarchy-duplicate"})
	if duplicate.Code != http.StatusConflict || responseErrorCode(t, duplicate.Body.Bytes()) != "hierarchy_already_exists" {
		t.Fatalf("duplicate hierarchy link = %d body=%s", duplicate.Code, duplicate.Body.String())
	}
	if linked.ParentTaskID == nil || *linked.ParentTaskID != fixture.dependent.ID {
		t.Fatalf("fixture link parent = %#v", linked.ParentTaskID)
	}
}

func TestTaskHierarchyHTTPCreateAndPatchParentAliases(t *testing.T) {
	fixture := newDependencyHTTPFixture(t, "HIERPATCH")
	created := request(t, fixture.server, http.MethodPost, "/api/v1/projects/"+fixture.project.Key+"/tasks", map[string]any{
		"title":  "Created hierarchy child",
		"parent": fixture.dependent.Key,
	}, map[string]string{"Content-Type": "application/json", "Idempotency-Key": "hierarchy-create-parent"})
	if created.Code != http.StatusCreated || created.Header().Get("ETag") != `"v1"` {
		t.Fatalf("create hierarchy child = %d etag=%q body=%s", created.Code, created.Header().Get("ETag"), created.Body.String())
	}
	var child store.Task
	if err := json.Unmarshal(created.Body.Bytes(), &child); err != nil {
		t.Fatal(err)
	}
	if child.ParentTaskID == nil || *child.ParentTaskID != fixture.dependent.ID {
		t.Fatalf("created child parent = %#v, want %q", child.ParentTaskID, fixture.dependent.ID)
	}
	cleared := request(t, fixture.server, http.MethodPatch, "/api/v1/tasks/"+child.ID, map[string]any{"parent_task_id": nil}, map[string]string{
		"Content-Type":    "application/json",
		"If-Match":        `"v1"`,
		"Idempotency-Key": "hierarchy-patch-clear",
	})
	if cleared.Code != http.StatusOK || cleared.Header().Get("ETag") != `"v2"` {
		t.Fatalf("patch hierarchy clear = %d etag=%q body=%s", cleared.Code, cleared.Header().Get("ETag"), cleared.Body.String())
	}
	var clearedTask store.Task
	if err := json.Unmarshal(cleared.Body.Bytes(), &clearedTask); err != nil {
		t.Fatal(err)
	}
	if clearedTask.ParentTaskID != nil || clearedTask.Parent != nil {
		t.Fatalf("patched hierarchy parent = %#v/%#v, want nil", clearedTask.ParentTaskID, clearedTask.Parent)
	}
}
