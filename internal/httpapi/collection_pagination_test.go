package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"testing"

	"roadmap/internal/store"
)

func TestProjectCollectionPaginationHasNoGaps(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatal(err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("PAGES"), Name: stringPtr("Pages")}, "actor-disabled-mode")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	columns, err := data.ListColumns(ctx, project.ID)
	if err != nil {
		t.Fatalf("list columns: %v", err)
	}
	columnIDs := make([]string, 0, len(columns))
	for _, column := range columns {
		columnIDs = append(columnIDs, column.ID)
	}
	gotColumnIDs := make([]string, 0, len(columnIDs))
	cursor := ""
	for page := 0; ; page++ {
		target := "/api/v1/projects/" + project.ID + "/columns?limit=2"
		if cursor != "" {
			target += "&cursor=" + url.QueryEscape(cursor)
		}
		response := request(t, server, http.MethodGet, target, nil, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("columns page %d status = %d, body=%s", page, response.Code, response.Body.String())
		}
		var body struct {
			Data       []store.Column `json:"data"`
			NextCursor string         `json:"next_cursor"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode columns page %d: %v", page, err)
		}
		if len(body.Data) == 0 || len(body.Data) > 2 {
			t.Fatalf("columns page %d length = %d, want 1-2", page, len(body.Data))
		}
		for _, column := range body.Data {
			gotColumnIDs = append(gotColumnIDs, column.ID)
		}
		if body.NextCursor == "" {
			break
		}
		cursor = body.NextCursor
		if page > len(columnIDs) {
			t.Fatal("columns pagination did not terminate")
		}
	}
	if !reflect.DeepEqual(gotColumnIDs, columnIDs) {
		t.Fatalf("column pages = %v, want ordered rows %v", gotColumnIDs, columnIDs)
	}

	for _, name := range []string{"alpha", "bravo", "charlie"} {
		if _, err := data.CreateLabel(ctx, project.ID, store.LabelInput{Name: name}, "actor-disabled-mode"); err != nil {
			t.Fatalf("create label %q: %v", name, err)
		}
	}
	labels, err := data.ListLabels(ctx, project.ID)
	if err != nil {
		t.Fatalf("list labels: %v", err)
	}
	labelIDs := make([]string, 0, len(labels))
	for _, label := range labels {
		labelIDs = append(labelIDs, label.ID)
	}
	gotLabelIDs := make([]string, 0, len(labelIDs))
	cursor = ""
	for page := 0; ; page++ {
		target := "/api/v1/projects/" + project.ID + "/labels?limit=2"
		if cursor != "" {
			target += "&cursor=" + url.QueryEscape(cursor)
		}
		response := request(t, server, http.MethodGet, target, nil, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("labels page %d status = %d, body=%s", page, response.Code, response.Body.String())
		}
		var body struct {
			Data       []store.Label `json:"data"`
			NextCursor string        `json:"next_cursor"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode labels page %d: %v", page, err)
		}
		if len(body.Data) == 0 || len(body.Data) > 2 {
			t.Fatalf("labels page %d length = %d, want 1-2", page, len(body.Data))
		}
		for _, label := range body.Data {
			gotLabelIDs = append(gotLabelIDs, label.ID)
		}
		if body.NextCursor == "" {
			break
		}
		cursor = body.NextCursor
		if page > len(labelIDs) {
			t.Fatal("labels pagination did not terminate")
		}
	}
	if !reflect.DeepEqual(gotLabelIDs, labelIDs) {
		t.Fatalf("label pages = %v, want ordered rows %v", gotLabelIDs, labelIDs)
	}
}

func TestAgentCollectionPaginationAndDisabledFilter(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatal(err)
	}
	agentNames := []string{"A enabled", "B disabled", "C enabled", "D enabled"}
	agents := make([]store.Actor, 0, len(agentNames))
	for _, name := range agentNames {
		agent, err := data.CreateAgent(ctx, store.Actor{Kind: "agent", Name: name}, "actor-disabled-mode", "")
		if err != nil {
			t.Fatalf("create agent %q: %v", name, err)
		}
		agents = append(agents, agent)
	}
	if _, err := data.DB.ExecContext(ctx, `UPDATE actors SET disabled_at=? WHERE id=?`, "2026-08-27T12:00:00Z", agents[1].ID); err != nil {
		t.Fatalf("disable agent: %v", err)
	}

	for _, query := range []string{"", "&disabled=false"} {
		response := request(t, server, http.MethodGet, "/api/v1/agents?limit=2"+query, nil, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("enabled agents status = %d, body=%s", response.Code, response.Body.String())
		}
		var body struct {
			Data       []store.Actor `json:"data"`
			NextCursor string        `json:"next_cursor"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode enabled agents: %v", err)
		}
		if len(body.Data) != 2 || body.NextCursor == "" {
			t.Fatalf("enabled first page = %d rows, cursor %q; want 2 rows and cursor", len(body.Data), body.NextCursor)
		}
		for _, actor := range body.Data {
			if actor.DisabledAt != nil {
				t.Fatalf("disabled actor %q leaked from %q filter", actor.ID, query)
			}
		}
		second := request(t, server, http.MethodGet, "/api/v1/agents?limit=2&disabled=false&cursor="+url.QueryEscape(body.NextCursor), nil, nil)
		if second.Code != http.StatusOK {
			t.Fatalf("enabled second page status = %d, body=%s", second.Code, second.Body.String())
		}
		var secondBody struct {
			Data       []store.Actor `json:"data"`
			NextCursor string        `json:"next_cursor"`
		}
		if err := json.Unmarshal(second.Body.Bytes(), &secondBody); err != nil {
			t.Fatalf("decode enabled second page: %v", err)
		}
		if len(secondBody.Data) != 1 || secondBody.NextCursor != "" || secondBody.Data[0].ID != agents[3].ID {
			t.Fatalf("enabled second page = %#v, cursor %q; want final %q", secondBody.Data, secondBody.NextCursor, agents[3].ID)
		}
	}

	response := request(t, server, http.MethodGet, "/api/v1/agents?limit=2&disabled=true", nil, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("all agents status = %d, body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Data       []store.Actor `json:"data"`
		NextCursor string        `json:"next_cursor"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode all agents: %v", err)
	}
	if len(body.Data) != 2 || body.NextCursor == "" {
		t.Fatalf("all agents first page = %d rows, cursor %q; want 2 rows and cursor", len(body.Data), body.NextCursor)
	}
	second := request(t, server, http.MethodGet, "/api/v1/agents?limit=2&disabled=true&cursor="+url.QueryEscape(body.NextCursor), nil, nil)
	if second.Code != http.StatusOK {
		t.Fatalf("all agents second page status = %d, body=%s", second.Code, second.Body.String())
	}
	var secondBody struct {
		Data       []store.Actor `json:"data"`
		NextCursor string        `json:"next_cursor"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &secondBody); err != nil {
		t.Fatalf("decode all agents second page: %v", err)
	}
	if len(secondBody.Data) != 2 || secondBody.NextCursor != "" || secondBody.Data[0].ID != agents[2].ID || secondBody.Data[1].ID != agents[3].ID {
		t.Fatalf("all agents second page = %#v, cursor %q; want final %q and %q", secondBody.Data, secondBody.NextCursor, agents[2].ID, agents[3].ID)
	}

	invalid := request(t, server, http.MethodGet, "/api/v1/agents?disabled=maybe", nil, nil)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid disabled filter status = %d, body=%s", invalid.Code, invalid.Body.String())
	}
}
