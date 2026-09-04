package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/KanterLabs/helm/internal/store"
)

func TestTaskChecklistHTTPContractAndIdempotency(t *testing.T) {
	server, _ := testServer(t, "disabled")
	projectResponse := request(t, server, http.MethodPost, "/api/v1/projects", map[string]any{
		"key": "CHECKAPI", "name": "Checklist API", "checklist_completion_policy": "require",
	}, map[string]string{"Content-Type": "application/json", "Idempotency-Key": "checklist-project"})
	if projectResponse.Code != http.StatusCreated {
		t.Fatalf("create checklist project: status=%d body=%s", projectResponse.Code, projectResponse.Body.String())
	}
	var project store.Project
	if err := json.Unmarshal(projectResponse.Body.Bytes(), &project); err != nil {
		t.Fatalf("decode checklist project: %v", err)
	}
	if project.ChecklistCompletionPolicy != "require" {
		t.Fatalf("project policy = %q, want require", project.ChecklistCompletionPolicy)
	}
	taskResponse := request(t, server, http.MethodPost, "/api/v1/projects/"+project.ID+"/tasks", map[string]any{"title": "Checklist API task"}, map[string]string{"Content-Type": "application/json"})
	if taskResponse.Code != http.StatusCreated {
		t.Fatalf("create checklist task: status=%d body=%s", taskResponse.Code, taskResponse.Body.String())
	}
	var task store.Task
	if err := json.Unmarshal(taskResponse.Body.Bytes(), &task); err != nil {
		t.Fatalf("decode checklist task: %v", err)
	}

	initial := request(t, server, http.MethodGet, "/api/v1/tasks/"+task.ID+"/checklist", nil, nil)
	if initial.Code != http.StatusOK || initial.Header().Get("ETag") != `"v1"` {
		t.Fatalf("initial checklist: status=%d etag=%q body=%s", initial.Code, initial.Header().Get("ETag"), initial.Body.String())
	}
	var collection store.ChecklistCollection
	if err := json.Unmarshal(initial.Body.Bytes(), &collection); err != nil {
		t.Fatalf("decode initial checklist: %v", err)
	}
	if collection.TaskID != task.ID || collection.Version != 1 || collection.Items == nil || len(collection.Items) != 0 {
		t.Fatalf("initial collection = %#v", collection)
	}

	addHeaders := map[string]string{"Content-Type": "application/json", "If-Match": `"v1"`, "Idempotency-Key": "checklist-add-1"}
	addedResponse := request(t, server, http.MethodPost, "/api/v1/tasks/"+task.ID+"/checklist", map[string]any{"text": "Ship API"}, addHeaders)
	if addedResponse.Code != http.StatusOK || addedResponse.Header().Get("ETag") != `"v2"` {
		t.Fatalf("add checklist item: status=%d etag=%q body=%s", addedResponse.Code, addedResponse.Header().Get("ETag"), addedResponse.Body.String())
	}
	var added store.Task
	if err := json.Unmarshal(addedResponse.Body.Bytes(), &added); err != nil {
		t.Fatalf("decode added checklist task: %v", err)
	}
	if len(added.Checklist) != 1 || added.Checklist[0].Text != "Ship API" || added.Checklist[0].Completed {
		t.Fatalf("added checklist = %#v", added.Checklist)
	}
	replay := request(t, server, http.MethodPost, "/api/v1/tasks/"+task.ID+"/checklist", map[string]any{"text": "Ship API"}, addHeaders)
	if replay.Code != addedResponse.Code || replay.Header().Get("ETag") != addedResponse.Header().Get("ETag") || replay.Body.String() != addedResponse.Body.String() {
		t.Fatalf("checklist replay changed response: first=%d/%q/%s replay=%d/%q/%s", addedResponse.Code, addedResponse.Header().Get("ETag"), addedResponse.Body.String(), replay.Code, replay.Header().Get("ETag"), replay.Body.String())
	}
	itemID := added.Checklist[0].ID

	secondResponse := request(t, server, http.MethodPost, "/api/v1/tasks/"+task.ID+"/checklist", map[string]any{"title": "Verify keyboard access"}, map[string]string{"Content-Type": "application/json", "If-Match": `"v2"`, "Idempotency-Key": "checklist-add-2"})
	if secondResponse.Code != http.StatusOK || secondResponse.Header().Get("ETag") != `"v3"` {
		t.Fatalf("add second checklist item: status=%d etag=%q body=%s", secondResponse.Code, secondResponse.Header().Get("ETag"), secondResponse.Body.String())
	}
	var second store.Task
	if err := json.Unmarshal(secondResponse.Body.Bytes(), &second); err != nil {
		t.Fatalf("decode second checklist task: %v", err)
	}
	if len(second.Checklist) != 2 {
		t.Fatalf("second checklist = %#v", second.Checklist)
	}

	completedResponse := request(t, server, http.MethodPatch, "/api/v1/tasks/"+task.ID+"/checklist/"+itemID, map[string]any{"completed": true}, map[string]string{"Content-Type": "application/json", "If-Match": `"v3"`, "Idempotency-Key": "checklist-complete-1"})
	if completedResponse.Code != http.StatusOK || completedResponse.Header().Get("ETag") != `"v4"` {
		t.Fatalf("complete checklist item: status=%d etag=%q body=%s", completedResponse.Code, completedResponse.Header().Get("ETag"), completedResponse.Body.String())
	}

	stale := request(t, server, http.MethodPatch, "/api/v1/tasks/"+task.ID+"/checklist/"+itemID, map[string]any{"text": "stale"}, map[string]string{"Content-Type": "application/json", "If-Match": `"v3"`, "Idempotency-Key": "checklist-stale"})
	if stale.Code != http.StatusConflict || responseErrorCode(t, stale.Body.Bytes()) != "stale_task" {
		t.Fatalf("stale checklist mutation: status=%d code=%s body=%s", stale.Code, responseErrorCode(t, stale.Body.Bytes()), stale.Body.String())
	}

	reordered := request(t, server, http.MethodPatch, "/api/v1/tasks/"+task.ID+"/checklist", map[string]any{"item_ids": []string{second.Checklist[1].ID, second.Checklist[0].ID}}, map[string]string{"Content-Type": "application/json", "If-Match": `"v4"`, "Idempotency-Key": "checklist-reorder-1"})
	if reordered.Code != http.StatusOK || reordered.Header().Get("ETag") != `"v5"` {
		t.Fatalf("reorder checklist: status=%d etag=%q body=%s", reordered.Code, reordered.Header().Get("ETag"), reordered.Body.String())
	}
	var eventEnvelope struct {
		Data []store.Event `json:"data"`
	}
	eventsResponse := request(t, server, http.MethodGet, "/api/v1/events?project="+project.Key+"&after=0&limit=100", nil, nil)
	if eventsResponse.Code != http.StatusOK {
		t.Fatalf("checklist events feed: status=%d body=%s", eventsResponse.Code, eventsResponse.Body.String())
	}
	if err := json.Unmarshal(eventsResponse.Body.Bytes(), &eventEnvelope); err != nil {
		t.Fatalf("decode checklist events feed: %v", err)
	}
	wantedEvents := map[string]bool{
		"task.checklist_item_added":   false,
		"task.checklist_item_updated": false,
		"task.checklist_reordered":    false,
	}
	for _, event := range eventEnvelope.Data {
		if _, wanted := wantedEvents[event.Type]; !wanted {
			continue
		}
		wantedEvents[event.Type] = true
		if event.ActorID == nil || event.ProjectID == nil || event.TaskID == nil || *event.TaskID != task.ID || event.CreatedAt == "" {
			t.Fatalf("checklist event attribution = %#v", event)
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("decode %s payload: %v", event.Type, err)
		}
	}
	for eventType, found := range wantedEvents {
		if !found {
			t.Errorf("events feed omitted checklist event %q", eventType)
		}
	}

	blocked := request(t, server, http.MethodPost, "/api/v1/tasks/"+task.ID+"/complete", nil, map[string]string{"If-Match": `"v5"`})
	if blocked.Code != http.StatusConflict || responseErrorCode(t, blocked.Body.Bytes()) != "checklist_incomplete" {
		t.Fatalf("require policy completion: status=%d code=%s body=%s", blocked.Code, responseErrorCode(t, blocked.Body.Bytes()), blocked.Body.String())
	}
	if !strings.Contains(blocked.Body.String(), "open_items") {
		t.Fatalf("require policy details missing open_items: %s", blocked.Body.String())
	}

	read := request(t, server, http.MethodGet, "/api/v1/tasks/"+task.ID+"/checklist", nil, nil)
	if read.Code != http.StatusOK || read.Header().Get("ETag") != `"v5"` {
		t.Fatalf("read checklist after rejected completion: status=%d etag=%q body=%s", read.Code, read.Header().Get("ETag"), read.Body.String())
	}
	var afterRejected store.ChecklistCollection
	if err := json.Unmarshal(read.Body.Bytes(), &afterRejected); err != nil {
		t.Fatalf("decode checklist after rejected completion: %v", err)
	}
	if afterRejected.Version != 5 || afterRejected.Summary.Open != 1 {
		t.Fatalf("rejected completion changed checklist = %#v", afterRejected)
	}
}

func TestChecklistHTTPRequiresIfMatchAndScope(t *testing.T) {
	server, _ := testServer(t, "disabled")
	projectResponse := request(t, server, http.MethodPost, "/api/v1/projects", map[string]any{"key": "CHECKGATE", "name": "Checklist gate"}, map[string]string{"Content-Type": "application/json"})
	if projectResponse.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", projectResponse.Code, projectResponse.Body.String())
	}
	var project store.Project
	if err := json.Unmarshal(projectResponse.Body.Bytes(), &project); err != nil {
		t.Fatal(err)
	}
	taskResponse := request(t, server, http.MethodPost, "/api/v1/projects/"+project.ID+"/tasks", map[string]any{"title": "gate"}, nil)
	if taskResponse.Code != http.StatusCreated {
		t.Fatalf("create task: %d %s", taskResponse.Code, taskResponse.Body.String())
	}
	var task store.Task
	if err := json.Unmarshal(taskResponse.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	missingVersion := request(t, server, http.MethodPost, "/api/v1/tasks/"+task.ID+"/checklist", map[string]any{"text": "missing validator"}, map[string]string{"Content-Type": "application/json"})
	if missingVersion.Code != http.StatusPreconditionRequired || responseErrorCode(t, missingVersion.Body.Bytes()) != "if_match_required" {
		t.Fatalf("missing checklist If-Match: status=%d code=%s body=%s", missingVersion.Code, responseErrorCode(t, missingVersion.Body.Bytes()), missingVersion.Body.String())
	}
}
