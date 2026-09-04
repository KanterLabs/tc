package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/KanterLabs/helm/internal/db"
)

func TestListTasksCursorUsesStableKeysetPages(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	actor, err := data.CreateActor(ctx, Actor{Kind: "human", Name: "Pagination tester"}, "")
	if err != nil {
		t.Fatalf("create actor: %v", err)
	}
	project, err := data.CreateProject(ctx, ProjectInput{Key: taskPaginationString("PAGE"), Name: taskPaginationString("Pagination")}, actor.ID)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	for i := 0; i < 7; i++ {
		if _, err := data.CreateTask(ctx, project.ID, TaskInput{Title: taskPaginationString(fmt.Sprintf("Task %d", i))}, actor.ID); err != nil {
			t.Fatalf("create task %d: %v", i, err)
		}
	}

	first, more, cursor, err := data.ListTasksCursor(ctx, project.ID, TaskFilter{Sort: TaskSortPosition, Limit: 3})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if !more || cursor == "" || len(first) != 3 {
		t.Fatalf("first page len/more/cursor = %d/%t/%q", len(first), more, cursor)
	}
	second, more, cursor2, err := data.ListTasksCursor(ctx, project.ID, TaskFilter{Sort: TaskSortPosition, CursorToken: cursor, Limit: 3})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if !more || cursor2 == "" || len(second) != 3 {
		t.Fatalf("second page len/more/cursor = %d/%t/%q", len(second), more, cursor2)
	}
	third, more, cursor3, err := data.ListTasksCursor(ctx, project.ID, TaskFilter{Sort: TaskSortPosition, CursorToken: cursor2, Limit: 3})
	if err != nil {
		t.Fatalf("third page: %v", err)
	}
	if more || cursor3 != "" || len(third) != 1 {
		t.Fatalf("third page len/more/cursor = %d/%t/%q", len(third), more, cursor3)
	}
	seen := make(map[string]bool)
	for _, page := range [][]Task{first, second, third} {
		for _, task := range page {
			if seen[task.ID] {
				t.Fatalf("task %s appeared on multiple keyset pages", task.ID)
			}
			seen[task.ID] = true
		}
	}
	if len(seen) != 7 {
		t.Fatalf("keyset pages returned %d unique tasks, want 7", len(seen))
	}
	columns, err := data.ListColumns(ctx, project.ID)
	if err != nil {
		t.Fatalf("list project columns: %v", err)
	}
	var readyColumnID string
	for _, column := range columns {
		if column.SemanticState == "ready" {
			readyColumnID = column.ID
			break
		}
	}
	if readyColumnID == "" {
		t.Fatal("ready column is missing")
	}
	if _, err := data.CreateTask(ctx, project.ID, TaskInput{Title: taskPaginationString("Ready task"), ColumnID: &readyColumnID}, actor.ID); err != nil {
		t.Fatalf("create ready task: %v", err)
	}
	boardFirst, boardMore, boardCursor, err := data.ListTasksCursor(ctx, project.ID, TaskFilter{Sort: TaskSortBoard, Limit: 7})
	if err != nil || len(boardFirst) != 7 || !boardMore || boardCursor == "" {
		t.Fatalf("board-order page = len %d, more %t, cursor %q, err %v", len(boardFirst), boardMore, boardCursor, err)
	}
	boardSecond, boardMore, _, err := data.ListTasksCursor(ctx, project.ID, TaskFilter{Sort: TaskSortBoard, CursorToken: boardCursor, Limit: 7})
	if err != nil || len(boardSecond) != 1 || boardMore {
		t.Fatalf("board-order continuation = len %d, more %t, err %v", len(boardSecond), boardMore, err)
	}
	if _, _, _, err := data.ListTasksCursor(ctx, project.ID, TaskFilter{Sort: TaskSortNumber, CursorToken: cursor, Limit: 3}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("sort mismatch error = %v, want invalid", err)
	}
}

func TestListTasksCursorFiltersBeforeApplyingCursor(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	actor, err := data.CreateActor(ctx, Actor{Kind: "human", Name: "Filter tester"}, "")
	if err != nil {
		t.Fatalf("create actor: %v", err)
	}
	project, err := data.CreateProject(ctx, ProjectInput{Key: taskPaginationString("FILTER"), Name: taskPaginationString("Filter")}, actor.ID)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	for i := 0; i < 6; i++ {
		priority := "normal"
		if i%2 == 0 {
			priority = "urgent"
		}
		if _, err := data.CreateTask(ctx, project.ID, TaskInput{Title: taskPaginationString(fmt.Sprintf("Filter task %d", i)), Priority: &priority}, actor.ID); err != nil {
			t.Fatalf("create task %d: %v", i, err)
		}
	}
	first, more, cursor, err := data.ListTasksCursor(ctx, project.ID, TaskFilter{Priority: "urgent", Sort: TaskSortNumber, Limit: 2})
	if err != nil {
		t.Fatalf("first filtered page: %v", err)
	}
	if !more || len(first) != 2 || cursor == "" {
		t.Fatalf("first filtered page len/more/cursor = %d/%t/%q", len(first), more, cursor)
	}
	second, more, _, err := data.ListTasksCursor(ctx, project.ID, TaskFilter{Priority: "urgent", Sort: TaskSortNumber, CursorToken: cursor, Limit: 2})
	if err != nil {
		t.Fatalf("second filtered page: %v", err)
	}
	if more || len(second) != 1 {
		t.Fatalf("second filtered page len/more = %d/%t, want 1/false", len(second), more)
	}
}

type taskCursorMutationFixture struct {
	ctx     context.Context
	db      *sql.DB
	store   *Store
	actor   Actor
	project Project
	tasks   []Task
	columns []Column
}

func newTaskCursorMutationFixture(t *testing.T) taskCursorMutationFixture {
	t.Helper()
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	data := New(database)
	actor, err := data.CreateActor(ctx, Actor{Kind: "human", Name: "Cursor mutation tester"}, "")
	if err != nil {
		t.Fatalf("create actor: %v", err)
	}
	project, err := data.CreateProject(ctx, ProjectInput{Key: taskPaginationString("MUTATE"), Name: taskPaginationString("Cursor mutation")}, actor.ID)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	tasks := make([]Task, 4)
	for i := range tasks {
		tasks[i], err = data.CreateTask(ctx, project.ID, TaskInput{Title: taskPaginationString(fmt.Sprintf("Cursor task %d", i+1))}, actor.ID)
		if err != nil {
			t.Fatalf("create task %d: %v", i, err)
		}
	}
	columns, err := data.ListColumns(ctx, project.ID)
	if err != nil {
		t.Fatalf("list columns: %v", err)
	}
	return taskCursorMutationFixture{ctx: ctx, db: database, store: data, actor: actor, project: project, tasks: tasks, columns: columns}
}

func TestListTasksCursorInvalidatesOnMutableCollectionChanges(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(taskCursorMutationFixture) error
	}{
		{name: "same-column reorder", mutate: func(f taskCursorMutationFixture) error {
			column := taskCursorColumnByStateForTest(f.columns, "backlog")
			_, err := f.store.ReorderTask(f.ctx, f.tasks[3].ID, TaskReorderInput{
				DestinationColumnID: column.ID, ExpectedSourceColumnID: column.ID,
				Source: "pagination-test", Reason: "reorder", Placement: "first",
			}, f.tasks[3].Version, f.actor.ID)
			return err
		}},
		{name: "cross-column move", mutate: func(f taskCursorMutationFixture) error {
			destination := taskCursorColumnByStateForTest(f.columns, "ready")
			_, err := f.store.MoveTask(f.ctx, f.tasks[3].ID, TaskMoveInput{
				DestinationColumnID: destination.ID, ExpectedSourceColumnID: f.tasks[3].ColumnID,
				Source: "pagination-test", Reason: "move",
			}, f.tasks[3].Version, f.actor.ID)
			return err
		}},
		{name: "title update", mutate: func(f taskCursorMutationFixture) error {
			title := "Cursor title changed"
			_, err := f.store.UpdateTask(f.ctx, f.tasks[3].ID, TaskInput{Title: &title}, f.tasks[3].Version, f.actor.ID)
			return err
		}},
		{name: "priority update", mutate: func(f taskCursorMutationFixture) error {
			priority := "urgent"
			_, err := f.store.UpdateTask(f.ctx, f.tasks[3].ID, TaskInput{Priority: &priority}, f.tasks[3].Version, f.actor.ID)
			return err
		}},
		{name: "timestamp update", mutate: func(f taskCursorMutationFixture) error {
			dueAt := "2030-01-01T00:00:00Z"
			_, err := f.store.UpdateTask(f.ctx, f.tasks[3].ID, TaskInput{DueAt: &dueAt, DueAtSet: true}, f.tasks[3].Version, f.actor.ID)
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newTaskCursorMutationFixture(t)
			_, more, cursor, err := fixture.store.ListTasksCursor(fixture.ctx, fixture.project.ID, TaskFilter{Sort: TaskSortNumber, Limit: 1})
			if err != nil || !more || cursor == "" {
				t.Fatalf("first page more=%t cursor=%q err=%v", more, cursor, err)
			}
			if err := test.mutate(fixture); err != nil {
				t.Fatalf("mutation: %v", err)
			}
			_, _, _, err = fixture.store.ListTasksCursor(fixture.ctx, fixture.project.ID, TaskFilter{Sort: TaskSortNumber, CursorToken: cursor, Limit: 1})
			if !errors.Is(err, ErrTaskCollectionChanged) {
				t.Fatalf("continuation error = %v, want ErrTaskCollectionChanged", err)
			}
		})
	}
}

func TestUpdateTaskNoOpPatchStillAdvancesCollectionEvent(t *testing.T) {
	fixture := newTaskCursorMutationFixture(t)
	var eventsBefore int
	if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT COUNT(*) FROM events WHERE project_id=?`, fixture.project.ID).Scan(&eventsBefore); err != nil {
		t.Fatalf("count events before empty patch: %v", err)
	}
	updated, err := fixture.store.UpdateTask(fixture.ctx, fixture.tasks[0].ID, TaskInput{}, fixture.tasks[0].Version, fixture.actor.ID)
	if err != nil {
		t.Fatalf("empty patch: %v", err)
	}
	var eventsAfter int
	if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT COUNT(*) FROM events WHERE project_id=?`, fixture.project.ID).Scan(&eventsAfter); err != nil {
		t.Fatalf("count events after empty patch: %v", err)
	}
	if eventsAfter != eventsBefore+1 || updated.Version != fixture.tasks[0].Version+1 {
		t.Fatalf("empty patch events/version = %d/%d, want %d/%d", eventsAfter, updated.Version, eventsBefore+1, fixture.tasks[0].Version+1)
	}
	kind := updated.Kind
	updatedAgain, err := fixture.store.UpdateTask(fixture.ctx, updated.ID, TaskInput{Kind: &kind}, updated.Version, fixture.actor.ID)
	if err != nil {
		t.Fatalf("same-value kind patch: %v", err)
	}
	if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT COUNT(*) FROM events WHERE project_id=?`, fixture.project.ID).Scan(&eventsAfter); err != nil {
		t.Fatalf("count events after same-value patch: %v", err)
	}
	if eventsAfter != eventsBefore+2 || updatedAgain.Version != updated.Version+1 {
		t.Fatalf("same-value kind events/version = %d/%d, want %d/%d", eventsAfter, updatedAgain.Version, eventsBefore+2, updated.Version+1)
	}
	parentID := fixture.tasks[0].ID
	child, err := fixture.store.UpdateTask(fixture.ctx, fixture.tasks[1].ID, TaskInput{ParentTaskID: &parentID}, fixture.tasks[1].Version, fixture.actor.ID)
	if err != nil {
		t.Fatalf("initial parent patch: %v", err)
	}
	if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT COUNT(*) FROM events WHERE project_id=?`, fixture.project.ID).Scan(&eventsAfter); err != nil {
		t.Fatalf("count events after initial parent patch: %v", err)
	}
	if eventsAfter != eventsBefore+3 {
		t.Fatalf("initial parent patch events = %d, want %d", eventsAfter, eventsBefore+3)
	}
	childAgain, err := fixture.store.UpdateTask(fixture.ctx, child.ID, TaskInput{ParentTaskID: &parentID}, child.Version, fixture.actor.ID)
	if err != nil {
		t.Fatalf("same-parent patch: %v", err)
	}
	if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT COUNT(*) FROM events WHERE project_id=?`, fixture.project.ID).Scan(&eventsAfter); err != nil {
		t.Fatalf("count events after same-parent patch: %v", err)
	}
	if eventsAfter != eventsBefore+4 || childAgain.Version != child.Version+1 {
		t.Fatalf("same-parent patch events/version = %d/%d, want %d/%d", eventsAfter, childAgain.Version, eventsBefore+4, child.Version+1)
	}
}

func taskCursorColumnByStateForTest(columns []Column, state string) Column {
	for _, column := range columns {
		if column.SemanticState == state {
			return column
		}
	}
	return Column{}
}

func TestListTasksCursorRejectsProjectMismatch(t *testing.T) {
	fixture := newTaskCursorMutationFixture(t)
	_, more, cursor, err := fixture.store.ListTasksCursor(fixture.ctx, fixture.project.ID, TaskFilter{Sort: TaskSortNumber, Limit: 1})
	if err != nil || !more || cursor == "" {
		t.Fatalf("first page more=%t cursor=%q err=%v", more, cursor, err)
	}
	other, err := fixture.store.CreateProject(fixture.ctx, ProjectInput{Key: taskPaginationString("OTHER"), Name: taskPaginationString("Other")}, fixture.actor.ID)
	if err != nil {
		t.Fatalf("create other project: %v", err)
	}
	_, _, _, err = fixture.store.ListTasksCursor(fixture.ctx, other.ID, TaskFilter{Sort: TaskSortNumber, CursorToken: cursor, Limit: 1})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("project mismatch error = %v, want ErrInvalid", err)
	}
}

func TestListTasksCursorHeartbeatRevisionCatchesSameTimestampMutation(t *testing.T) {
	fixture := newTaskCursorMutationFixture(t)
	for i, task := range fixture.tasks[:2] {
		// Deliberately share the same millisecond precision as HeartbeatAgentWork.
		// The second row starts below the maximum and is then refreshed to the
		// existing maximum, so MAX(updated_at) alone cannot detect the change.
		updatedAt := "2026-01-01T00:00:00.124Z"
		if i == 1 {
			updatedAt = "2026-01-01T00:00:00.123Z"
		}
		if _, err := fixture.db.ExecContext(fixture.ctx, `INSERT INTO task_agent_work(task_id, operation_id, actor_id, state, phase, summary, next_action, checkpoint_refs, started_at, updated_at) VALUES (?, ?, ?, 'working', '', 'cursor heartbeat', '', '[]', ?, ?)`, task.ID, fmt.Sprintf("cursor-heartbeat-%d", i), fixture.actor.ID, updatedAt, updatedAt); err != nil {
			t.Fatalf("insert task %d snapshot: %v", i, err)
		}
	}
	_, more, cursor, err := fixture.store.ListTasksCursor(fixture.ctx, fixture.project.ID, TaskFilter{Sort: TaskSortNumber, Limit: 1})
	if err != nil || !more || cursor == "" {
		t.Fatalf("first page more=%t cursor=%q err=%v", more, cursor, err)
	}
	if _, err := fixture.db.ExecContext(fixture.ctx, `UPDATE task_agent_work SET updated_at=? WHERE task_id=?`, "2026-01-01T00:00:00.124Z", fixture.tasks[1].ID); err != nil {
		t.Fatalf("refresh non-max snapshot to existing max: %v", err)
	}
	_, _, _, err = fixture.store.ListTasksCursor(fixture.ctx, fixture.project.ID, TaskFilter{Sort: TaskSortNumber, CursorToken: cursor, Limit: 1})
	if !errors.Is(err, ErrTaskCollectionChanged) {
		t.Fatalf("heartbeat continuation error = %v, want ErrTaskCollectionChanged", err)
	}
}

func TestListTasksCursorHeartbeatInvalidatesContinuation(t *testing.T) {
	fixture := newTaskCursorMutationFixture(t)
	operations := []string{"cursor-heartbeat-live-1", "cursor-heartbeat-live-2"}
	for i, task := range fixture.tasks[:2] {
		claimed, err := fixture.store.ClaimTask(fixture.ctx, task.ID, fixture.actor.ID, time.Hour, task.Version)
		if err != nil {
			t.Fatalf("claim task %d: %v", i, err)
		}
		published, err := fixture.store.PublishAgentWork(fixture.ctx, task.ID, AgentWorkInput{
			OperationID: operations[i], State: "working", Summary: "Cursor heartbeat",
		}, claimed.Version, fixture.actor.ID)
		if err != nil {
			t.Fatalf("publish task %d snapshot: %v", i, err)
		}
		fixture.tasks[i] = published
	}
	_, more, cursor, err := fixture.store.ListTasksCursor(fixture.ctx, fixture.project.ID, TaskFilter{Sort: TaskSortNumber, Limit: 1})
	if err != nil || !more || cursor == "" {
		t.Fatalf("first page more=%t cursor=%q err=%v", more, cursor, err)
	}
	if _, err := fixture.store.HeartbeatAgentWork(fixture.ctx, fixture.tasks[1].ID, operations[1], fixture.actor.ID); err != nil {
		t.Fatalf("heartbeat between pages: %v", err)
	}
	_, _, _, err = fixture.store.ListTasksCursor(fixture.ctx, fixture.project.ID, TaskFilter{Sort: TaskSortNumber, CursorToken: cursor, Limit: 1})
	if !errors.Is(err, ErrTaskCollectionChanged) {
		t.Fatalf("heartbeat continuation error = %v, want ErrTaskCollectionChanged", err)
	}
}

func TestListTasksCursorContinuationUsesOriginalReadAt(t *testing.T) {
	fixture := newTaskCursorMutationFixture(t)
	for i, task := range fixture.tasks[:2] {
		updatedAt := time.Now().UTC().Add(-20 * time.Minute).Format(time.RFC3339Nano)
		if _, err := fixture.db.ExecContext(fixture.ctx, `INSERT INTO task_agent_work(task_id, operation_id, actor_id, state, phase, summary, next_action, checkpoint_refs, started_at, updated_at) VALUES (?, ?, ?, 'working', '', 'snapshot', '', '[]', ?, ?)`, task.ID, fmt.Sprintf("cursor-liveness-%d", i), fixture.actor.ID, updatedAt, updatedAt); err != nil {
			t.Fatalf("insert liveness snapshot %d: %v", i, err)
		}
	}
	_, more, cursor, err := fixture.store.ListTasksCursor(fixture.ctx, fixture.project.ID, TaskFilter{Sort: TaskSortNumber, Limit: 1})
	if err != nil || !more || cursor == "" {
		t.Fatalf("first page more=%t cursor=%q err=%v", more, cursor, err)
	}
	decoded, err := DecodeTaskCursor(cursor)
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	// Re-encode the server-issued boundary with a deterministic earlier read
	// time. No collection data changes, so the collection and agent-work
	// revisions remain valid; only the liveness cutoff differs from a fresh
	// continuation clock.
	decoded.ReadAt = time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339Nano)
	payload, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("marshal test cursor: %v", err)
	}
	cursor = taskCursorPrefix + base64.RawURLEncoding.EncodeToString(payload)
	second, _, _, err := fixture.store.ListTasksCursor(fixture.ctx, fixture.project.ID, TaskFilter{Sort: TaskSortNumber, AgentState: "stale", CursorToken: cursor, Limit: 1})
	if err != nil {
		t.Fatalf("continuation with fixed read time: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("continuation classified old snapshot as stale under original read time: %+v", second)
	}
}

func TestListTasksCursorUsesOriginalReadAtForHierarchyLiveness(t *testing.T) {
	fixture := newTaskCursorMutationFixture(t)
	parent := fixture.tasks[1]
	child := fixture.tasks[2]
	var err error
	child, err = fixture.store.SetTaskParent(fixture.ctx, child.ID, parent.ID, child.Version, fixture.actor.ID)
	if err != nil {
		t.Fatalf("link hierarchy child: %v", err)
	}
	operations := []string{"cursor-hierarchy-parent", "cursor-hierarchy-child"}
	parent, err = fixture.store.ClaimTask(fixture.ctx, parent.ID, fixture.actor.ID, time.Hour, parent.Version)
	if err != nil {
		t.Fatalf("claim hierarchy parent: %v", err)
	}
	parent, err = fixture.store.PublishAgentWork(fixture.ctx, parent.ID, AgentWorkInput{
		OperationID: operations[0], State: "working", Summary: "Hierarchy parent pulse",
	}, parent.Version, fixture.actor.ID)
	if err != nil {
		t.Fatalf("publish hierarchy parent: %v", err)
	}
	child, err = fixture.store.ClaimTask(fixture.ctx, child.ID, fixture.actor.ID, time.Hour, child.Version)
	if err != nil {
		t.Fatalf("claim hierarchy child: %v", err)
	}
	child, err = fixture.store.PublishAgentWork(fixture.ctx, child.ID, AgentWorkInput{
		OperationID: operations[1], State: "working", Summary: "Hierarchy child pulse",
	}, child.Version, fixture.actor.ID)
	if err != nil {
		t.Fatalf("publish hierarchy child: %v", err)
	}
	oldPulse := time.Now().UTC().Add(-20 * time.Minute).Format(time.RFC3339Nano)
	for _, task := range []Task{parent, child} {
		if _, err := fixture.db.ExecContext(fixture.ctx, `UPDATE task_agent_work SET updated_at=? WHERE task_id=?`, oldPulse, task.ID); err != nil {
			t.Fatalf("age hierarchy pulse %s: %v", task.ID, err)
		}
	}

	first, more, cursor, err := fixture.store.ListTasksCursor(fixture.ctx, fixture.project.ID, TaskFilter{Sort: TaskSortNumber, Limit: 1})
	if err != nil || !more || cursor == "" || len(first) != 1 {
		t.Fatalf("first hierarchy page = len %d, more %t, cursor %q, err %v", len(first), more, cursor, err)
	}
	decoded, err := DecodeTaskCursor(cursor)
	if err != nil {
		t.Fatalf("decode hierarchy cursor: %v", err)
	}
	// Make the fixed read boundary old enough that the deliberately aged
	// snapshots are fresh. A hierarchy implementation that calls time.Now()
	// while enriching the continuation would incorrectly report them stale.
	decoded.ReadAt = time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339Nano)
	payload, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("marshal hierarchy cursor: %v", err)
	}
	cursor = taskCursorPrefix + base64.RawURLEncoding.EncodeToString(payload)
	second, more, cursor2, err := fixture.store.ListTasksCursor(fixture.ctx, fixture.project.ID, TaskFilter{Sort: TaskSortNumber, CursorToken: cursor, Limit: 1})
	if err != nil || !more || cursor2 == "" || len(second) != 1 || second[0].ID != parent.ID {
		t.Fatalf("second hierarchy page = %+v, more %t, cursor %q, err %v", second, more, cursor2, err)
	}
	if second[0].HierarchySummary.StaleAgentWorkCount != 0 || second[0].HierarchySummary.ActionNeededCount != 0 {
		t.Fatalf("hierarchy summary liveness = %+v, want fresh under fixed read time", second[0].HierarchySummary)
	}
	third, _, _, err := fixture.store.ListTasksCursor(fixture.ctx, fixture.project.ID, TaskFilter{Sort: TaskSortNumber, CursorToken: cursor2, Limit: 1})
	if err != nil || len(third) != 1 || third[0].ID != child.ID || third[0].Parent == nil || third[0].Parent.AgentWork == nil {
		t.Fatalf("third hierarchy page = %+v, err %v, want child with parent work", third, err)
	}
	if third[0].Parent.AgentWork.Stale || third[0].Parent.AgentWork.ActionNeeded {
		t.Fatalf("hierarchy parent liveness = %+v, want fresh under fixed read time", third[0].Parent.AgentWork)
	}
}

func TestListTasksCursorTitleSortUnicodeAndASCIIKeysetPages(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	actor, err := data.CreateActor(ctx, Actor{Kind: "human", Name: "Title pagination tester"}, "")
	if err != nil {
		t.Fatalf("create actor: %v", err)
	}
	project, err := data.CreateProject(ctx, ProjectInput{Key: taskPaginationString("TITLE"), Name: taskPaginationString("Title pagination")}, actor.ID)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	titles := []string{
		"bravo",
		"Alpha",
		"ALPHA",
		"alpha",
		"Bravo",
		"Àlpha",
		"Álpha",
		"Âlpha",
		"àlpha",
		"Zulu",
		"zulu",
	}
	byTitle := make(map[string]string, len(titles))
	for _, title := range titles {
		task, err := data.CreateTask(ctx, project.ID, TaskInput{Title: taskPaginationString(title)}, actor.ID)
		if err != nil {
			t.Fatalf("create task %q: %v", title, err)
		}
		byTitle[title] = task.ID
	}

	wantAscendingTitles := []string{
		"Alpha", "ALPHA", "alpha",
		"bravo", "Bravo",
		"Zulu", "zulu",
		"Àlpha", "Álpha", "Âlpha", "àlpha",
	}
	wantDescendingTitles := make([]string, len(wantAscendingTitles))
	for i, title := range wantAscendingTitles {
		wantDescendingTitles[len(wantDescendingTitles)-1-i] = title
	}

	for _, test := range []struct {
		name       string
		descending bool
		wantTitles []string
	}{
		{name: "ascending", wantTitles: wantAscendingTitles},
		{name: "descending", descending: true, wantTitles: wantDescendingTitles},
	} {
		t.Run(test.name, func(t *testing.T) {
			all, more, cursor, err := data.ListTasksCursor(ctx, project.ID, TaskFilter{Sort: TaskSortTitle, Descending: test.descending, Limit: len(titles)})
			if err != nil {
				t.Fatalf("list complete title order: %v", err)
			}
			if more || cursor != "" {
				t.Fatalf("complete title order more/cursor = %t/%q, want false/empty", more, cursor)
			}
			gotAllTitles := make([]string, len(all))
			for i, task := range all {
				gotAllTitles[i] = task.Title
			}
			if !reflect.DeepEqual(gotAllTitles, test.wantTitles) {
				t.Fatalf("complete title order = %#v, want %#v", gotAllTitles, test.wantTitles)
			}

			const pageSize = 2
			var gotIDs []string
			seen := make(map[string]bool, len(titles))
			var nextCursor string
			for pageNumber := 1; ; pageNumber++ {
				page, pageMore, pageCursor, err := data.ListTasksCursor(ctx, project.ID, TaskFilter{
					Sort:        TaskSortTitle,
					Descending:  test.descending,
					CursorToken: nextCursor,
					Limit:       pageSize,
				})
				if err != nil {
					t.Fatalf("page %d: %v", pageNumber, err)
				}
				if len(page) == 0 {
					t.Fatalf("page %d is empty", pageNumber)
				}
				for _, task := range page {
					if seen[task.ID] {
						t.Fatalf("task %s appeared on multiple %s pages", task.ID, test.name)
					}
					seen[task.ID] = true
					gotIDs = append(gotIDs, task.ID)
				}
				if !pageMore {
					if pageCursor != "" {
						t.Fatalf("final page cursor = %q, want empty", pageCursor)
					}
					break
				}
				if pageCursor == "" {
					t.Fatalf("page %d has more rows but no cursor", pageNumber)
				}
				nextCursor = pageCursor
			}

			wantIDs := make([]string, len(all))
			for i, task := range all {
				wantIDs[i] = byTitle[task.Title]
			}
			if !reflect.DeepEqual(gotIDs, wantIDs) {
				t.Fatalf("paged IDs = %#v, want %#v", gotIDs, wantIDs)
			}
			if len(seen) != len(titles) {
				t.Fatalf("paged unique tasks = %d, want %d", len(seen), len(titles))
			}
		})
	}
}

func TestUpdateTaskColumnChangeUsesDestinationTail(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	actor, err := data.CreateActor(ctx, Actor{Kind: "human", Name: "Move tester"}, "")
	if err != nil {
		t.Fatalf("create actor: %v", err)
	}
	project, err := data.CreateProject(ctx, ProjectInput{Key: taskPaginationString("MOVE"), Name: taskPaginationString("Move")}, actor.ID)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	columns, err := data.ListColumns(ctx, project.ID)
	if err != nil {
		t.Fatalf("list columns: %v", err)
	}
	var source, destination Column
	for _, column := range columns {
		switch column.SemanticState {
		case "backlog":
			source = column
		case "active":
			destination = column
		}
	}
	if source.ID == "" || destination.ID == "" {
		t.Fatal("backlog and active columns are required")
	}
	destinationPosition := 11.0
	if _, err := data.CreateTask(ctx, project.ID, TaskInput{Title: taskPaginationString("Existing active"), ColumnID: &destination.ID, Position: &destinationPosition}, actor.ID); err != nil {
		t.Fatalf("create destination task: %v", err)
	}
	sourcePosition := 2.0
	task, err := data.CreateTask(ctx, project.ID, TaskInput{Title: taskPaginationString("Move me"), ColumnID: &source.ID, Position: &sourcePosition}, actor.ID)
	if err != nil {
		t.Fatalf("create source task: %v", err)
	}
	moved, err := data.UpdateTask(ctx, task.ID, TaskInput{ColumnID: &destination.ID}, task.Version, actor.ID)
	if err != nil {
		t.Fatalf("move task: %v", err)
	}
	if moved.Position != destinationPosition+1 {
		t.Fatalf("moved position = %v, want destination tail %v", moved.Position, destinationPosition+1)
	}
}

func TestUpdateTaskColumnChangeWaitsForConcurrentDestinationTailWriter(t *testing.T) {
	fixture := newTaskClaimLockFixture(t, "UPDATERACE")
	columns, err := fixture.store.ListColumns(fixture.ctx, fixture.project.ID)
	if err != nil {
		t.Fatalf("list columns: %v", err)
	}
	if len(columns) < 2 {
		t.Fatalf("columns = %d, want source and destination", len(columns))
	}
	source, destination := columns[0], columns[1]
	destinationPosition := 10.0
	if _, err := fixture.store.CreateTask(fixture.ctx, fixture.project.ID, TaskInput{Title: taskPaginationString("Existing destination"), ColumnID: &destination.ID, Position: &destinationPosition}, fixture.owner.ID); err != nil {
		t.Fatalf("create destination task: %v", err)
	}
	sourcePosition := 2.0
	task, err := fixture.store.CreateTask(fixture.ctx, fixture.project.ID, TaskInput{Title: taskPaginationString("Wait for writer"), ColumnID: &source.ID, Position: &sourcePosition}, fixture.owner.ID)
	if err != nil {
		t.Fatalf("create source task: %v", err)
	}

	blocker := fixture.holdWriterLock(t)
	defer blocker.Rollback()
	var writerTailPosition float64
	if err := blocker.QueryRowContext(fixture.ctx, `SELECT COALESCE(MAX(position)+1, 0) FROM tasks WHERE column_id=? AND deleted_at IS NULL`, destination.ID).Scan(&writerTailPosition); err != nil {
		t.Fatalf("find writer destination tail: %v", err)
	}
	var writerTailNumber int
	if err := blocker.QueryRowContext(fixture.ctx, `SELECT COALESCE(MAX(number), 0)+1 FROM tasks WHERE project_id=?`, fixture.project.ID).Scan(&writerTailNumber); err != nil {
		t.Fatalf("find writer task number: %v", err)
	}
	created := time.Now().UTC().Format(time.RFC3339Nano)
	writerTailID := newID()
	if _, err := blocker.ExecContext(fixture.ctx, `INSERT INTO tasks(id, project_id, number, column_id, kind, title, description, priority, position, version, created_at, updated_at) VALUES (?, ?, ?, ?, 'task', ?, '', 'normal', ?, 1, ?, ?)`, writerTailID, fixture.project.ID, writerTailNumber, destination.ID, "Writer destination tail", writerTailPosition, created, created); err != nil {
		t.Fatalf("insert uncommitted destination tail: %v", err)
	}

	result := make(chan struct {
		task Task
		err  error
	}, 1)
	go func() {
		moved, moveErr := fixture.store.UpdateTask(fixture.ctx, task.ID, TaskInput{ColumnID: &destination.ID}, task.Version, fixture.owner.ID)
		result <- struct {
			task Task
			err  error
		}{moved, moveErr}
	}()
	select {
	case result := <-result:
		t.Fatalf("update finished while writer lock was held: task=%+v err=%v", result.task, result.err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := blocker.Commit(); err != nil {
		t.Fatalf("release writer lock: %v", err)
	}

	select {
	case moved := <-result:
		if moved.err != nil {
			t.Fatalf("update after writer wait: %v", moved.err)
		}
		if moved.task.ColumnID != destination.ID {
			t.Fatalf("moved column = %q, want %q", moved.task.ColumnID, destination.ID)
		}
		if moved.task.Position != writerTailPosition+1 {
			t.Fatalf("moved position = %v, want writer destination tail %v", moved.task.Position, writerTailPosition+1)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("update did not finish after writer lock was released")
	}
}

// BenchmarkListTasksCursor10K records the cost of fetching a bounded first
// page from a ten-thousand-task board. The setup is outside the timed region;
// the benchmark measures the same enriched response path used by HTTP.
func BenchmarkListTasksCursor10K(b *testing.B) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		b.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	actor, err := data.CreateActor(ctx, Actor{Kind: "human", Name: "Benchmark tester"}, "")
	if err != nil {
		b.Fatalf("create actor: %v", err)
	}
	project, err := data.CreateProject(ctx, ProjectInput{Key: taskPaginationString("BENCH"), Name: taskPaginationString("Benchmark")}, actor.ID)
	if err != nil {
		b.Fatalf("create project: %v", err)
	}
	var columnID string
	if err := database.QueryRowContext(ctx, `SELECT id FROM columns WHERE project_id=? AND semantic_state='backlog'`, project.ID).Scan(&columnID); err != nil {
		b.Fatalf("find backlog column: %v", err)
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		b.Fatalf("begin benchmark setup: %v", err)
	}
	created := time.Now().UTC().Format(time.RFC3339Nano)
	for i := 1; i <= 10000; i++ {
		if _, err := tx.ExecContext(ctx, `INSERT INTO tasks(id, project_id, number, column_id, kind, title, description, priority, position, version, created_at, updated_at) VALUES (?, ?, ?, ?, 'task', ?, '', 'normal', ?, 1, ?, ?)`, fmt.Sprintf("benchmark-task-%05d", i), project.ID, i, columnID, fmt.Sprintf("Benchmark task %d", i), float64(i-1), created, created); err != nil {
			tx.Rollback()
			b.Fatalf("insert benchmark task %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatalf("commit benchmark setup: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		page, more, cursor, err := data.ListTasksCursor(ctx, project.ID, TaskFilter{Column: columnID, Sort: TaskSortPosition, Limit: 50})
		if err != nil {
			b.Fatalf("list benchmark page: %v", err)
		}
		if len(page) != 50 || !more || cursor == "" {
			b.Fatalf("benchmark page len/more/cursor = %d/%t/%q", len(page), more, cursor)
		}
	}
}

func taskPaginationString(value string) *string { return &value }
