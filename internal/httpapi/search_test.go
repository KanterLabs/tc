package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KanterLabs/helm/internal/store"
)

func TestGlobalSearchMatchesFieldsAndPaginates(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatal(err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("FIND"), Name: stringPtr("Search project")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	label, err := data.CreateLabel(ctx, project.ID, store.LabelInput{Name: "needle"}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	assignee, err := data.CreateActor(ctx, store.Actor{Kind: "agent", Name: "Search owner"}, "")
	if err != nil {
		t.Fatal(err)
	}
	due := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
	first, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr("Unrelated title"), Description: stringPtr("Description needle"), Assignee: &assignee.ID, AssigneeSet: true, DueAt: &due, DueAtSet: true, Labels: []string{label.ID}, LabelsSet: true}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := data.ClaimTask(ctx, first.ID, assignee.ID, time.Hour, first.Version); err != nil {
		t.Fatal(err)
	}
	second, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr("Another needle"), Priority: stringPtr("urgent")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	_ = second

	for _, target := range []string{
		"/api/v1/search?q=needle&limit=1",
		"/api/v1/search?description=needle",
		"/api/v1/search?label=needle",
		"/api/v1/search?assignee=" + assignee.ID,
		"/api/v1/search?claim_owner=" + assignee.ID,
		"/api/v1/search?due_from=" + urlEscape(time.Now().UTC().Format(time.RFC3339)),
		"/api/v1/search?project=FIND",
	} {
		response := request(t, server, http.MethodGet, target, nil, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("search %s status=%d body=%s", target, response.Code, response.Body.String())
		}
	}
	page := request(t, server, http.MethodGet, "/api/v1/search?q=needle&limit=1", nil, nil)
	var body struct {
		Data       []store.Task `json:"data"`
		NextCursor string       `json:"next_cursor"`
	}
	if err := json.Unmarshal(page.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 1 || body.NextCursor == "" {
		t.Fatalf("search page=%+v, want one result and next cursor", body)
	}
	for i := range body.Data {
		if body.Data[i].Key == "" {
			t.Fatalf("search result has no task key: %+v", body.Data[i])
		}
	}
	labelledResponse := request(t, server, http.MethodGet, "/api/v1/search?label=needle", nil, nil)
	var labelled struct {
		Data []store.Task `json:"data"`
	}
	if err := json.Unmarshal(labelledResponse.Body.Bytes(), &labelled); err != nil {
		t.Fatal(err)
	}
	if len(labelled.Data) != 1 || len(labelled.Data[0].Labels) != 1 || labelled.Data[0].Labels[0].Name != "needle" {
		t.Fatalf("search result was not enriched: %+v", labelled.Data)
	}

	next := request(t, server, http.MethodGet, "/api/v1/search?q=needle&limit=1&cursor="+body.NextCursor, nil, nil)
	if next.Code != http.StatusOK || strings.Contains(next.Body.String(), body.Data[0].ID) {
		t.Fatalf("search next page=%d %s", next.Code, next.Body.String())
	}
}

func TestSavedViewLifecycleAndScopedSearch(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatal(err)
	}
	allowed, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("ALLOW"), Name: stringPtr("Allowed")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("BLOCK"), Name: stringPtr("Blocked")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := data.CreateTask(ctx, allowed.ID, store.TaskInput{Title: stringPtr("Visible result"), Priority: stringPtr("urgent")}, "actor-disabled-mode"); err != nil {
		t.Fatal(err)
	}
	if _, err := data.CreateTask(ctx, blocked.ID, store.TaskInput{Title: stringPtr("Secret result")}, "actor-disabled-mode"); err != nil {
		t.Fatal(err)
	}
	agent, err := data.CreateAgent(ctx, store.Actor{Kind: "agent", Name: "Scoped", ProjectIDs: []string{allowed.ID}}, "actor-disabled-mode", "")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := data.CreateTokenBy(ctx, agent.ID, "actor-disabled-mode", "search", []string{"tasks:read", "tasks:write"}, []string{allowed.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	headers := map[string]string{"Authorization": "Bearer " + token}
	created := request(t, server, http.MethodPost, "/api/v1/views", map[string]any{
		"name": "Urgent view", "filters": map[string]any{"priority": "urgent"},
		"sort": []map[string]string{{"field": "priority", "direction": "asc"}}, "shared": true,
	}, headers)
	if created.Code != http.StatusCreated {
		t.Fatalf("create view status=%d body=%s", created.Code, created.Body.String())
	}
	var view store.SavedView
	if err := json.Unmarshal(created.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Name != "Urgent view" || !view.Shared || view.Filters["priority"] != "urgent" {
		t.Fatalf("created view=%+v", view)
	}
	list := request(t, server, http.MethodGet, "/api/v1/views", nil, headers)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), view.ID) {
		t.Fatalf("list views status=%d body=%s", list.Code, list.Body.String())
	}
	search := request(t, server, http.MethodGet, "/api/v1/views/"+view.ID+"/search", nil, headers)
	if search.Code != http.StatusOK || !strings.Contains(search.Body.String(), "Visible result") || strings.Contains(search.Body.String(), "Secret result") {
		t.Fatalf("scoped saved search status=%d body=%s", search.Code, search.Body.String())
	}
	global := request(t, server, http.MethodGet, "/api/v1/search?q=result&limit=1", nil, headers)
	if global.Code != http.StatusOK || !strings.Contains(global.Body.String(), "Visible result") || strings.Contains(global.Body.String(), "Secret result") {
		t.Fatalf("scoped global search status=%d body=%s", global.Code, global.Body.String())
	}
	var globalPage struct {
		Data       []store.Task `json:"data"`
		NextCursor string       `json:"next_cursor"`
	}
	if err := json.Unmarshal(global.Body.Bytes(), &globalPage); err != nil {
		t.Fatal(err)
	}
	if len(globalPage.Data) != 1 || globalPage.Data[0].ProjectID != allowed.ID || globalPage.NextCursor != "" {
		t.Fatalf("scoped global page=%+v", globalPage)
	}
	invalid := request(t, server, http.MethodPost, "/api/v1/views", map[string]any{
		"name": "Invalid view", "filters": map[string]any{"unsupported": "value"},
	}, headers)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid view status=%d body=%s", invalid.Code, invalid.Body.String())
	}
	aliasList := request(t, server, http.MethodGet, "/api/v1/saved-views", nil, headers)
	if aliasList.Code != http.StatusOK || !strings.Contains(aliasList.Body.String(), view.ID) {
		t.Fatalf("saved view alias status=%d body=%s", aliasList.Code, aliasList.Body.String())
	}
	minimal := request(t, server, http.MethodPost, "/api/v1/views", map[string]any{
		"name": "Minimal view", "filters": map[string]any{},
	}, headers)
	if minimal.Code != http.StatusCreated {
		t.Fatalf("minimal view status=%d body=%s", minimal.Code, minimal.Body.String())
	}
	updated := request(t, server, http.MethodPatch, "/api/v1/views/"+view.ID, map[string]any{"name": "Urgent shared", "shared": false}, headers)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), "Urgent shared") {
		t.Fatalf("update view status=%d body=%s", updated.Code, updated.Body.String())
	}
	deleted := request(t, server, http.MethodDelete, "/api/v1/views/"+view.ID, nil, headers)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete view status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	missing := request(t, server, http.MethodGet, "/api/v1/views/"+view.ID, nil, headers)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("deleted view status=%d body=%s", missing.Code, missing.Body.String())
	}

	_ = blocked
}

type savedViewPaginationFixture struct {
	server  *Server
	data    *store.Store
	ctx     context.Context
	headers map[string]string
	owner   store.Actor
	allowed store.Project
	blocked store.Project
}

func newSavedViewPaginationFixture(t *testing.T) savedViewPaginationFixture {
	t.Helper()
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatal(err)
	}
	allowed, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("VIEWALLOW"), Name: stringPtr("View allowed")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("VIEWBLOCK"), Name: stringPtr("View blocked")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := data.CreateAgent(ctx, store.Actor{Kind: "agent", Name: "Saved view owner"}, "actor-disabled-mode", "")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := data.CreateAgent(ctx, store.Actor{Kind: "agent", Name: "Saved view reader", ProjectIDs: []string{allowed.ID}}, "actor-disabled-mode", "")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := data.CreateTokenBy(ctx, reader.ID, "actor-disabled-mode", "saved-view-reader", []string{"tasks:read"}, []string{allowed.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return savedViewPaginationFixture{
		server:  server,
		data:    data,
		ctx:     ctx,
		headers: map[string]string{"Authorization": "Bearer " + token},
		owner:   owner,
		allowed: allowed,
		blocked: blocked,
	}
}

func createSharedSavedView(t *testing.T, fixture savedViewPaginationFixture, name, projectID string) store.SavedView {
	t.Helper()
	shared := true
	view, err := fixture.data.CreateSavedView(fixture.ctx, fixture.owner.ID, store.SavedViewInput{
		Name:       stringPtr(name),
		Filters:    map[string]any{"project": projectID},
		FiltersSet: true,
		Shared:     &shared,
	})
	if err != nil {
		t.Fatalf("create saved view %q: %v", name, err)
	}
	return view
}

func decodeSavedViewPage(t *testing.T, response *httptest.ResponseRecorder) struct {
	Data       []store.SavedView `json:"data"`
	NextCursor string            `json:"next_cursor"`
} {
	t.Helper()
	var page struct {
		Data       []store.SavedView `json:"data"`
		NextCursor string            `json:"next_cursor"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode saved view page: %v; body=%s", err, response.Body.String())
	}
	return page
}

func TestSavedViewPaginationDoesNotDropVisibleViewAfterHiddenPage(t *testing.T) {
	fixture := newSavedViewPaginationFixture(t)
	for index := 1; index <= 51; index++ {
		createSharedSavedView(t, fixture, fmt.Sprintf("A-hidden-%02d", index), fixture.blocked.ID)
	}
	visible := createSharedSavedView(t, fixture, "Z-visible", fixture.allowed.ID)

	response := request(t, fixture.server, http.MethodGet, "/api/v1/views?limit=50", nil, fixture.headers)
	if response.Code != http.StatusOK {
		t.Fatalf("list saved views status=%d body=%s", response.Code, response.Body.String())
	}
	page := decodeSavedViewPage(t, response)
	if len(page.Data) != 1 || page.Data[0].ID != visible.ID || page.NextCursor != "" {
		t.Fatalf("visible page=%+v, want only the authorized view and no next cursor", page)
	}
	if strings.Contains(response.Body.String(), "A-hidden-") {
		t.Fatalf("saved view page leaked an inaccessible view: %s", response.Body.String())
	}
}

func TestSavedViewPaginationCursorResumesAuthorizedStreamWithoutLeak(t *testing.T) {
	fixture := newSavedViewPaginationFixture(t)
	for index := 1; index <= 51; index++ {
		createSharedSavedView(t, fixture, fmt.Sprintf("A-hidden-%02d", index), fixture.blocked.ID)
	}
	firstVisible := createSharedSavedView(t, fixture, "Z-visible-1", fixture.allowed.ID)
	secondVisible := createSharedSavedView(t, fixture, "Z-visible-2", fixture.allowed.ID)

	firstResponse := request(t, fixture.server, http.MethodGet, "/api/v1/views?limit=1", nil, fixture.headers)
	if firstResponse.Code != http.StatusOK {
		t.Fatalf("first saved view page status=%d body=%s", firstResponse.Code, firstResponse.Body.String())
	}
	firstPage := decodeSavedViewPage(t, firstResponse)
	if len(firstPage.Data) != 1 || firstPage.Data[0].ID != firstVisible.ID || firstPage.NextCursor == "" {
		t.Fatalf("first authorized page=%+v, want first visible view and a cursor", firstPage)
	}
	if strings.Contains(firstResponse.Body.String(), "A-hidden-") {
		t.Fatalf("first saved view page leaked an inaccessible view: %s", firstResponse.Body.String())
	}

	secondResponse := request(t, fixture.server, http.MethodGet, "/api/v1/views?limit=1&cursor="+firstPage.NextCursor, nil, fixture.headers)
	if secondResponse.Code != http.StatusOK {
		t.Fatalf("second saved view page status=%d body=%s", secondResponse.Code, secondResponse.Body.String())
	}
	secondPage := decodeSavedViewPage(t, secondResponse)
	if len(secondPage.Data) != 1 || secondPage.Data[0].ID != secondVisible.ID || secondPage.NextCursor != "" {
		t.Fatalf("second authorized page=%+v, want second visible view and terminal cursor", secondPage)
	}
	if strings.Contains(secondResponse.Body.String(), "A-hidden-") {
		t.Fatalf("second saved view page leaked an inaccessible view: %s", secondResponse.Body.String())
	}
}

func urlEscape(value string) string {
	return strings.NewReplacer(":", "%3A", "+", "%2B", " ", "%20").Replace(value)
}
