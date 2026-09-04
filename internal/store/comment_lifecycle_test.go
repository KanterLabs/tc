package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/KanterLabs/helm/internal/db"
)

func TestCommentLifecycleUsesVersionedTombstonesAndImmutableEvents(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	actor, project, task := createAgentWorkFixture(t, data, ctx, "COMMENTLIFECYCLE")

	comment, err := data.CreateComment(ctx, task.ID, actor.ID, "**before**")
	if err != nil {
		t.Fatalf("create comment: %v", err)
	}
	if comment.Version != 1 || comment.DeletedAt != nil {
		t.Fatalf("new comment metadata = %+v, want version 1 and live", comment)
	}

	// Actor names are resolved at read time. This keeps retained events useful
	// after an actor display-name change without copying mutable identity into
	// every event payload.
	if _, err := database.ExecContext(ctx, `UPDATE actors SET name=? WHERE id=?`, "Renamed actor", actor.ID); err != nil {
		t.Fatalf("rename actor: %v", err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE actors SET disabled_at=? WHERE id=?`, "2026-09-04T00:00:00Z", actor.ID); err != nil {
		t.Fatalf("disable actor: %v", err)
	}
	updated, err := data.UpdateComment(ctx, task.ID, comment.ID, actor.ID, "After **edit**", comment.Version, false)
	if err != nil {
		t.Fatalf("update comment: %v", err)
	}
	if updated.Version != 2 || updated.Body != "After **edit**" || updated.DeletedAt != nil {
		t.Fatalf("updated comment = %+v", updated)
	}
	if _, err := data.UpdateComment(ctx, task.ID, comment.ID, actor.ID, "stale overwrite", comment.Version, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale comment update = %v, want ErrConflict", err)
	}
	unchanged, err := data.GetComment(ctx, comment.ID)
	if err != nil {
		t.Fatalf("read comment after stale update: %v", err)
	}
	if unchanged.Version != 2 || unchanged.Body != updated.Body {
		t.Fatalf("stale update mutated comment = %+v", unchanged)
	}

	other, err := data.CreateActor(ctx, Actor{Kind: "agent", Name: "Other actor"}, "")
	if err != nil {
		t.Fatalf("create second actor: %v", err)
	}
	if _, err := data.UpdateComment(ctx, task.ID, comment.ID, other.ID, "unauthorized", updated.Version, false); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unauthorized comment update = %v, want ErrForbidden", err)
	}

	if err := data.DeleteComment(ctx, task.ID, comment.ID, actor.ID, updated.Version, false); err != nil {
		t.Fatalf("delete comment: %v", err)
	}
	if _, err := data.GetComment(ctx, comment.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted comment read = %v, want ErrNotFound", err)
	}
	comments, more, err := data.ListCommentsPage(ctx, task.ID, 20, 0)
	if err != nil {
		t.Fatalf("list active comments: %v", err)
	}
	if more || len(comments) != 0 {
		t.Fatalf("active comments after tombstone = %#v, more=%v", comments, more)
	}
	currentTask, err := data.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("read task after comment tombstone: %v", err)
	}
	if currentTask.CommentCount != 0 {
		t.Fatalf("comment count after tombstone = %d, want 0", currentTask.CommentCount)
	}

	var retainedBody string
	var retainedVersion int64
	var deletedAt sql.NullString
	if err := database.QueryRowContext(ctx, `SELECT body, version, deleted_at FROM comments WHERE id=?`, comment.ID).Scan(&retainedBody, &retainedVersion, &deletedAt); err != nil {
		t.Fatalf("read retained comment tombstone: %v", err)
	}
	if retainedBody != "After **edit**" || retainedVersion != 3 || !deletedAt.Valid {
		t.Fatalf("retained tombstone = body %q version %d deleted_at=%v", retainedBody, retainedVersion, deletedAt)
	}

	items, more, err := data.ListTaskTimeline(ctx, task.ID, TaskTimelineFilter{Limit: 50})
	if err != nil {
		t.Fatalf("list timeline after comment deletion: %v", err)
	}
	if more {
		t.Fatal("comment lifecycle timeline unexpectedly has another page")
	}
	var sawComment, sawCreated, sawUpdated, sawDeleted bool
	for _, item := range items {
		if item.Kind == "comment" && item.Comment != nil && item.Comment.ID == comment.ID {
			sawComment = true
		}
		if item.Kind == "task_change" && item.Change != nil {
			switch item.Change.EventType {
			case "comment.created":
				sawCreated = true
			case "comment.updated":
				sawUpdated = true
				if item.Actor == nil || item.Actor.Name != "Renamed actor" {
					t.Fatalf("updated event actor = %+v, want live renamed actor", item.Actor)
				}
			case "comment.deleted":
				sawDeleted = true
				if item.Actor == nil || item.Actor.Name != "Renamed actor" {
					t.Fatalf("deleted event actor = %+v, want live renamed actor", item.Actor)
				}
			}
		}
	}
	if sawComment || sawCreated || !sawUpdated || !sawDeleted {
		t.Fatalf("comment lifecycle timeline flags: comment=%v created=%v updated=%v deleted=%v", sawComment, sawCreated, sawUpdated, sawDeleted)
	}

	events, _, err := data.ListEvents(ctx, EventFilter{ProjectID: project.ID, Limit: 100})
	if err != nil {
		t.Fatalf("list lifecycle events: %v", err)
	}
	for _, event := range events {
		if event.Type != "comment.updated" && event.Type != "comment.deleted" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("decode %s payload: %v", event.Type, err)
		}
		if _, ok := payload["body"]; ok {
			t.Fatalf("%s event retained mutable comment body: %s", event.Type, event.Payload)
		}
		if payload["comment_id"] != comment.ID {
			t.Fatalf("%s payload = %#v, want comment_id %q", event.Type, payload, comment.ID)
		}
	}
}

func TestTaskTimelineWalksHighVolumeCommentsWithoutGaps(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	actor, _, task := createAgentWorkFixture(t, data, ctx, "COMMENTVOLUME")
	const commentCount = 250
	for index := 0; index < commentCount; index++ {
		if _, err := data.CreateComment(ctx, task.ID, actor.ID, fmt.Sprintf("comment %03d", index)); err != nil {
			t.Fatalf("create high-volume comment %d: %v", index, err)
		}
	}

	seen := make(map[string]struct{}, commentCount+1)
	before := ""
	for page := 0; page < commentCount+10; page++ {
		items, more, err := data.ListTaskTimeline(ctx, task.ID, TaskTimelineFilter{Before: before, Limit: 17})
		if err != nil {
			t.Fatalf("list high-volume page %d: %v", page, err)
		}
		if len(items) == 0 {
			if more {
				t.Fatal("empty high-volume timeline page reported more rows")
			}
			break
		}
		for _, item := range items {
			if _, exists := seen[item.ID]; exists {
				t.Fatalf("high-volume timeline duplicated item %q", item.ID)
			}
			seen[item.ID] = struct{}{}
			if item.Cursor == "" {
				t.Fatalf("high-volume timeline item has empty cursor: %+v", item)
			}
		}
		if !more {
			break
		}
		before = items[len(items)-1].Cursor
	}
	if len(seen) != commentCount+1 { // task.created plus one row per comment
		t.Fatalf("high-volume timeline rows = %d, want %d", len(seen), commentCount+1)
	}
}

func TestTaskTimelineKeepsDeletedActorEventsReadable(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	_, project, task := createAgentWorkFixture(t, data, ctx, "COMMENTACTOR")
	gone, err := data.CreateActor(ctx, Actor{Kind: "agent", Name: "Retired actor"}, "")
	if err != nil {
		t.Fatalf("create actor: %v", err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO events(id, type, actor_id, project_id, task_id, payload, created_at) VALUES (?, 'task.updated', ?, ?, ?, '{}', ?)`, "deleted-actor-event", gone.ID, project.ID, task.ID, "2026-09-04T00:00:01Z"); err != nil {
		t.Fatalf("insert actor event: %v", err)
	}
	if _, err := database.ExecContext(ctx, `DELETE FROM actors WHERE id=?`, gone.ID); err != nil {
		t.Fatalf("delete actor: %v", err)
	}
	items, _, err := data.ListTaskTimeline(ctx, task.ID, TaskTimelineFilter{Limit: 50})
	if err != nil {
		t.Fatalf("list timeline after actor deletion: %v", err)
	}
	for _, item := range items {
		if item.ID == "deleted-actor-event" {
			if item.Actor != nil {
				t.Fatalf("deleted actor was fabricated in timeline: %+v", item.Actor)
			}
			return
		}
	}
	t.Fatal("event with deleted actor was omitted")
}
