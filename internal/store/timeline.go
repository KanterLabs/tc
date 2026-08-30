package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

var taskTimelineKinds = map[string]struct{}{
	"agent_progress": {},
	"comment":        {},
	"task_change":    {},
}

const timelineCursorVersion = 1

// taskTimelineCursor is encoded as a URL-safe opaque value. It carries the
// complete keyset boundary rather than an offset, so inserting newer activity
// cannot shift an older page. EventCursor is zero for legacy rows that did not
// have an associated event; kind and id complete the deterministic tie-break.
type taskTimelineCursor struct {
	Version     int    `json:"v"`
	CreatedAt   string `json:"at"`
	EventCursor int64  `json:"ec"`
	Kind        string `json:"k"`
	ID          string `json:"id"`
}

type timelineSortKey struct {
	createdAt   time.Time
	createdRaw  string
	eventCursor int64
	kind        string
	kindRank    int
	id          string
}

type timelineCandidate struct {
	item TaskTimelineItem
	key  timelineSortKey
}

func timelineKindRank(kind string) int {
	switch kind {
	case "agent_progress":
		return 3
	case "comment":
		return 2
	case "task_change":
		return 1
	default:
		return 0
	}
}

func newTimelineSortKey(createdAt, kind, id string, eventCursor int64) timelineSortKey {
	parsed, _ := time.Parse(time.RFC3339Nano, createdAt)
	return timelineSortKey{
		createdAt:   parsed,
		createdRaw:  createdAt,
		eventCursor: eventCursor,
		kind:        kind,
		kindRank:    timelineKindRank(kind),
		id:          id,
	}
}

// compareTimelineKeys returns a positive value when a is newer than b. The
// timestamp is the primary activity ordering; the event cursor preserves the
// existing append order for rows written in one clock tick. Legacy rows use a
// stable kind/id tie-break because no event cursor can be safely inferred.
func compareTimelineKeys(a, b timelineSortKey) int {
	if !a.createdAt.IsZero() && !b.createdAt.IsZero() {
		if a.createdAt.After(b.createdAt) {
			return 1
		}
		if a.createdAt.Before(b.createdAt) {
			return -1
		}
	} else if a.createdRaw != b.createdRaw {
		if a.createdRaw > b.createdRaw {
			return 1
		}
		return -1
	}
	if a.eventCursor != b.eventCursor {
		if a.eventCursor > b.eventCursor {
			return 1
		}
		return -1
	}
	if a.kindRank != b.kindRank {
		if a.kindRank > b.kindRank {
			return 1
		}
		return -1
	}
	if a.kind != b.kind {
		if a.kind > b.kind {
			return 1
		}
		return -1
	}
	if a.id > b.id {
		return 1
	}
	if a.id < b.id {
		return -1
	}
	return 0
}

func encodeTaskTimelineCursor(key timelineSortKey) (string, error) {
	payload, err := json.Marshal(taskTimelineCursor{
		Version:     timelineCursorVersion,
		CreatedAt:   key.createdRaw,
		EventCursor: key.eventCursor,
		Kind:        key.kind,
		ID:          key.id,
	})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeTaskTimelineCursor(value string) (timelineSortKey, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return timelineSortKey{}, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return timelineSortKey{}, invalid("before must be a valid timeline cursor", nil)
	}
	var payload taskTimelineCursor
	if err := json.Unmarshal(decoded, &payload); err != nil || payload.Version != timelineCursorVersion || payload.CreatedAt == "" || payload.ID == "" {
		return timelineSortKey{}, invalid("before must be a valid timeline cursor", nil)
	}
	if _, ok := taskTimelineKinds[payload.Kind]; !ok {
		return timelineSortKey{}, invalid("before must be a valid timeline cursor", nil)
	}
	if _, err := time.Parse(time.RFC3339Nano, payload.CreatedAt); err != nil {
		return timelineSortKey{}, invalid("before must be a valid timeline cursor", nil)
	}
	return newTimelineSortKey(payload.CreatedAt, payload.Kind, payload.ID, payload.EventCursor), nil
}

func timelineCandidateBefore(candidate, boundary timelineSortKey) bool {
	return compareTimelineKeys(candidate, boundary) < 0
}

type timelineActorRow struct {
	id   string
	kind string
	name string
}

func timelineActor(actorID, actorKind, actorName sql.NullString) *TimelineActor {
	if !actorID.Valid || actorID.String == "" || !actorKind.Valid || !actorName.Valid {
		// A retained event can outlive its actor. Keep the activity item visible
		// while omitting a fabricated identity; progress/comment details retain
		// their original actor_id where that field is part of the payload.
		return nil
	}
	return &TimelineActor{ID: actorID.String, Kind: actorKind.String, Name: actorName.String}
}

func decodeTimelineCheckpointRefs(value string) ([]string, error) {
	var refs []string
	if err := json.Unmarshal([]byte(value), &refs); err != nil {
		return nil, fmt.Errorf("decode agent work checkpoint_refs: %w", err)
	}
	if refs == nil {
		refs = []string{}
	}
	return refs, nil
}

type timelineHistoryRow struct {
	id, taskID, operationID, actorID, state, phase, summary, nextAction string
	refsJSON                                                            string
	completed, total                                                    sql.NullInt64
	startedAt, createdAt                                                string
	generatedCommentID                                                  sql.NullString
	progressEventCursor                                                 sql.NullInt64
	actorKind, actorName                                                sql.NullString
}

type timelineCommentRow struct {
	comment                       Comment
	actorID, actorKind, actorName sql.NullString
}

type timelineEventRow struct {
	event Event
}

type timelineProjectEventRow struct {
	event                         Event
	actorID, actorKind, actorName sql.NullString
}

// ListTaskTimeline returns one stable newest-first page of the unified task
// activity stream. It deliberately reads the three durable sources separately
// and merges their typed rows in Go: this keeps legacy comments/events visible
// without pretending they contain fields that were never persisted.
func (s *Store) ListTaskTimeline(ctx context.Context, taskID string, filter TaskTimelineFilter) ([]TaskTimelineItem, bool, error) {
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	if filter.Limit > 200 {
		filter.Limit = 200
	}
	if filter.Kind != "" {
		if _, ok := taskTimelineKinds[filter.Kind]; !ok {
			return nil, false, invalid("kind is invalid", nil)
		}
	}
	boundary, err := decodeTaskTimelineCursor(filter.Before)
	if err != nil {
		return nil, false, err
	}

	// Deleted tasks are not readable through any task detail route. Keep the
	// store boundary aligned for direct callers as well.
	var exists int
	if err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE id=? AND deleted_at IS NULL)`, taskID).Scan(&exists); err != nil {
		return nil, false, err
	}
	if exists == 0 {
		return nil, false, notFound("task not found")
	}

	histories, err := s.listTimelineHistory(ctx, taskID)
	if err != nil {
		return nil, false, err
	}
	generatedComments := make(map[string]struct{}, len(histories))
	historyEvents := make(map[int64]struct{}, len(histories))
	candidates := make([]timelineCandidate, 0, len(histories))
	for _, row := range histories {
		refs, err := decodeTimelineCheckpointRefs(row.refsJSON)
		if err != nil {
			return nil, false, err
		}
		progress := &TaskTimelineProgress{
			OperationID:    row.operationID,
			ActorID:        row.actorID,
			State:          row.state,
			Phase:          row.phase,
			Summary:        row.summary,
			NextAction:     row.nextAction,
			CheckpointRefs: refs,
			StartedAt:      row.startedAt,
		}
		if row.completed.Valid {
			value := int(row.completed.Int64)
			progress.CheckpointCompleted = &value
		}
		if row.total.Valid {
			value := int(row.total.Int64)
			progress.CheckpointTotal = &value
		}
		if row.generatedCommentID.Valid && row.generatedCommentID.String != "" {
			generatedComments[row.generatedCommentID.String] = struct{}{}
		}
		eventCursor := int64(0)
		if row.progressEventCursor.Valid {
			eventCursor = row.progressEventCursor.Int64
			if eventCursor > 0 {
				historyEvents[eventCursor] = struct{}{}
			}
		}
		item := TaskTimelineItem{
			ID:        row.id,
			Kind:      "agent_progress",
			TaskID:    row.taskID,
			Actor:     timelineActor(sql.NullString{String: row.actorID, Valid: row.actorID != ""}, row.actorKind, row.actorName),
			CreatedAt: row.createdAt,
			Progress:  progress,
			Comment:   nil,
			Change:    nil,
		}
		candidates = append(candidates, timelineCandidate{item: item, key: newTimelineSortKey(row.createdAt, item.Kind, item.ID, eventCursor)})
	}

	comments, err := s.listTimelineComments(ctx, taskID)
	if err != nil {
		return nil, false, err
	}
	commentEventCursors := make(map[string]int64, len(comments))
	for _, row := range comments {
		if _, generated := generatedComments[row.comment.ID]; generated {
			continue
		}
		item := TaskTimelineItem{
			ID:        row.comment.ID,
			Kind:      "comment",
			TaskID:    row.comment.TaskID,
			Actor:     timelineActor(row.actorID, row.actorKind, row.actorName),
			CreatedAt: row.comment.CreatedAt,
			Progress:  nil,
			Comment:   &row.comment,
			Change:    nil,
		}
		// The matching comment.created event, when present, gives this comment
		// the same stable append sequence as task changes. Legacy comments that
		// predate the event feed remain usable through timestamp/id ordering.
		candidates = append(candidates, timelineCandidate{item: item, key: newTimelineSortKey(row.comment.CreatedAt, item.Kind, item.ID, 0)})
		commentEventCursors[row.comment.ID] = 0
	}

	events, err := s.listTimelineEvents(ctx, taskID)
	if err != nil {
		return nil, false, err
	}
	for _, row := range events {
		event := row.event
		commentID := timelineCommentID(event)
		if event.Type == "comment.created" && commentID != "" {
			if _, generated := generatedComments[commentID]; generated {
				continue
			}
			if _, exists := commentEventCursors[commentID]; exists {
				// The comment itself is the timeline item; do not emit a second
				// system row for its comment.created event.
				commentEventCursors[commentID] = event.Cursor
				for index := range candidates {
					if candidates[index].item.Kind == "comment" && candidates[index].item.ID == commentID {
						candidates[index].key.eventCursor = event.Cursor
						break
					}
				}
				continue
			}
			// A malformed or orphaned comment event remains visible as a
			// generic task change rather than disappearing from legacy history.
		}
		if event.Type == "task.progressed" {
			if _, linked := historyEvents[event.Cursor]; linked {
				continue
			}
			// An event written before migration 011 has no structured history
			// row. Keep it as a generic change; never infer rich progress data.
		}
		actorID := sql.NullString{}
		if event.ActorID != nil {
			actorID = sql.NullString{String: *event.ActorID, Valid: true}
		}
		actor := s.timelineActorForID(ctx, actorID)
		item := TaskTimelineItem{
			ID:        event.ID,
			Kind:      "task_change",
			TaskID:    taskID,
			Actor:     actor,
			CreatedAt: event.CreatedAt,
			Progress:  nil,
			Comment:   nil,
			Change: &TaskTimelineChange{
				EventID:   event.ID,
				EventType: event.Type,
				Payload:   event.Payload,
			},
		}
		candidates = append(candidates, timelineCandidate{item: item, key: newTimelineSortKey(event.CreatedAt, item.Kind, item.ID, event.Cursor)})
	}

	filtered := candidates[:0]
	for _, candidate := range candidates {
		if filter.Kind != "" && candidate.item.Kind != filter.Kind {
			continue
		}
		if filter.Before != "" && !timelineCandidateBefore(candidate.key, boundary) {
			continue
		}
		filtered = append(filtered, candidate)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		return compareTimelineKeys(filtered[i].key, filtered[j].key) > 0
	})
	for index := range filtered {
		cursor, err := encodeTaskTimelineCursor(filtered[index].key)
		if err != nil {
			return nil, false, err
		}
		filtered[index].item.Cursor = cursor
	}
	hasMore := len(filtered) > filter.Limit
	if hasMore {
		filtered = filtered[:filter.Limit]
	}
	result := make([]TaskTimelineItem, len(filtered))
	for index := range filtered {
		result[index] = filtered[index].item
	}
	return result, hasMore, nil
}

// ListProjectTimeline returns one stable newest-first page containing the
// unified activity stream for every non-deleted task in a project. It reads
// each durable source once with a live-task JOIN and merges those rows in one
// pass, so query work is bounded by the three activity sources rather than by
// the number of tasks on the board.
func (s *Store) ListProjectTimeline(ctx context.Context, projectID string, filter TaskTimelineFilter) ([]TaskTimelineItem, bool, error) {
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	if filter.Limit > 200 {
		filter.Limit = 200
	}
	if filter.Kind != "" {
		if _, ok := taskTimelineKinds[filter.Kind]; !ok {
			return nil, false, invalid("kind is invalid", nil)
		}
	}
	boundary, err := decodeTaskTimelineCursor(filter.Before)
	if err != nil {
		return nil, false, err
	}

	var exists int
	if err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM projects WHERE id=?)`, projectID).Scan(&exists); err != nil {
		return nil, false, err
	}
	if exists == 0 {
		return nil, false, notFound("project not found")
	}

	candidates, err := s.listProjectTimelineCandidates(ctx, projectID)
	if err != nil {
		return nil, false, err
	}

	filtered := candidates[:0]
	for _, candidate := range candidates {
		if filter.Kind != "" && candidate.item.Kind != filter.Kind {
			continue
		}
		if filter.Before != "" && !timelineCandidateBefore(candidate.key, boundary) {
			continue
		}
		filtered = append(filtered, candidate)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		return compareTimelineKeys(filtered[i].key, filtered[j].key) > 0
	})
	for index := range filtered {
		cursor, err := encodeTaskTimelineCursor(filtered[index].key)
		if err != nil {
			return nil, false, err
		}
		filtered[index].item.Cursor = cursor
	}
	hasMore := len(filtered) > filter.Limit
	if hasMore {
		filtered = filtered[:filter.Limit]
	}
	result := make([]TaskTimelineItem, len(filtered))
	for index := range filtered {
		result[index] = filtered[index].item
	}
	return result, hasMore, nil
}

// ListTimeline is a concise alias for callers that do not need the task
// prefix in the method name.
func (s *Store) ListTimeline(ctx context.Context, taskID string, filter TimelineFilter) ([]TaskTimelineItem, bool, error) {
	return s.ListTaskTimeline(ctx, taskID, filter)
}

func (s *Store) listTimelineHistory(ctx context.Context, taskID string) ([]timelineHistoryRow, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT h.id, h.task_id, h.operation_id, h.actor_id, h.state, h.phase, h.summary, h.next_action, h.checkpoint_refs, h.checkpoint_completed, h.checkpoint_total, h.started_at, h.created_at, h.generated_comment_id, h.progress_event_cursor, a.kind, a.name FROM task_agent_work_history h LEFT JOIN actors a ON a.id=h.actor_id WHERE h.task_id=? ORDER BY h.created_at DESC, h.id DESC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]timelineHistoryRow, 0)
	for rows.Next() {
		var row timelineHistoryRow
		if err := rows.Scan(&row.id, &row.taskID, &row.operationID, &row.actorID, &row.state, &row.phase, &row.summary, &row.nextAction, &row.refsJSON, &row.completed, &row.total, &row.startedAt, &row.createdAt, &row.generatedCommentID, &row.progressEventCursor, &row.actorKind, &row.actorName); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) listTimelineComments(ctx context.Context, taskID string) ([]timelineCommentRow, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT c.id, c.task_id, c.actor_id, c.body, c.created_at, c.updated_at, a.id, a.kind, a.name FROM comments c LEFT JOIN actors a ON a.id=c.actor_id WHERE c.task_id=? ORDER BY c.created_at DESC, c.id DESC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]timelineCommentRow, 0)
	for rows.Next() {
		var row timelineCommentRow
		if err := rows.Scan(&row.comment.ID, &row.comment.TaskID, &row.comment.ActorID, &row.comment.Body, &row.comment.CreatedAt, &row.comment.UpdatedAt, &row.actorID, &row.actorKind, &row.actorName); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) listTimelineEvents(ctx context.Context, taskID string) ([]timelineEventRow, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT cursor, id, type, actor_id, project_id, task_id, payload, created_at FROM events WHERE task_id=? ORDER BY created_at DESC, type DESC, cursor DESC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]timelineEventRow, 0)
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, timelineEventRow{event: event})
	}
	return result, rows.Err()
}

// The project-scoped source readers intentionally use one query per durable
// source. The JOIN on live tasks excludes soft-deleted task activity while
// retaining the item task_id needed by the merged response.
func (s *Store) listProjectTimelineHistory(ctx context.Context, projectID string) ([]timelineHistoryRow, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT h.id, h.task_id, h.operation_id, h.actor_id, h.state, h.phase, h.summary, h.next_action, h.checkpoint_refs, h.checkpoint_completed, h.checkpoint_total, h.started_at, h.created_at, h.generated_comment_id, h.progress_event_cursor, a.kind, a.name FROM task_agent_work_history h JOIN tasks t ON t.id=h.task_id LEFT JOIN actors a ON a.id=h.actor_id WHERE t.project_id=? AND t.deleted_at IS NULL ORDER BY h.created_at DESC, h.id DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]timelineHistoryRow, 0)
	for rows.Next() {
		var row timelineHistoryRow
		if err := rows.Scan(&row.id, &row.taskID, &row.operationID, &row.actorID, &row.state, &row.phase, &row.summary, &row.nextAction, &row.refsJSON, &row.completed, &row.total, &row.startedAt, &row.createdAt, &row.generatedCommentID, &row.progressEventCursor, &row.actorKind, &row.actorName); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) listProjectTimelineComments(ctx context.Context, projectID string) ([]timelineCommentRow, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT c.id, c.task_id, c.actor_id, c.body, c.created_at, c.updated_at, a.id, a.kind, a.name FROM comments c JOIN tasks t ON t.id=c.task_id LEFT JOIN actors a ON a.id=c.actor_id WHERE t.project_id=? AND t.deleted_at IS NULL ORDER BY c.created_at DESC, c.id DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]timelineCommentRow, 0)
	for rows.Next() {
		var row timelineCommentRow
		if err := rows.Scan(&row.comment.ID, &row.comment.TaskID, &row.comment.ActorID, &row.comment.Body, &row.comment.CreatedAt, &row.comment.UpdatedAt, &row.actorID, &row.actorKind, &row.actorName); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func scanProjectTimelineEvent(scanner interface{ Scan(...any) error }) (timelineProjectEventRow, error) {
	var row timelineProjectEventRow
	var actor, project, task, payload sql.NullString
	if err := scanner.Scan(&row.event.Cursor, &row.event.ID, &row.event.Type, &actor, &project, &task, &payload, &row.event.CreatedAt, &row.actorID, &row.actorKind, &row.actorName); err != nil {
		return timelineProjectEventRow{}, err
	}
	row.event.ActorID, row.event.ProjectID, row.event.TaskID = nullableString(actor), nullableString(project), nullableString(task)
	if payload.Valid {
		row.event.Payload = json.RawMessage(payload.String)
	} else {
		row.event.Payload = json.RawMessage(`{}`)
	}
	return row, nil
}

func (s *Store) listProjectTimelineEvents(ctx context.Context, projectID string) ([]timelineProjectEventRow, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT e.cursor, e.id, e.type, e.actor_id, e.project_id, e.task_id, e.payload, e.created_at, a.id, a.kind, a.name FROM events e JOIN tasks t ON t.id=e.task_id LEFT JOIN actors a ON a.id=e.actor_id WHERE t.project_id=? AND t.deleted_at IS NULL ORDER BY e.created_at DESC, e.type DESC, e.cursor DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]timelineProjectEventRow, 0)
	for rows.Next() {
		row, err := scanProjectTimelineEvent(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) listProjectTimelineCandidates(ctx context.Context, projectID string) ([]timelineCandidate, error) {
	histories, err := s.listProjectTimelineHistory(ctx, projectID)
	if err != nil {
		return nil, err
	}
	generatedComments := make(map[string]struct{}, len(histories))
	historyEvents := make(map[int64]struct{}, len(histories))
	candidates := make([]timelineCandidate, 0, len(histories))
	for _, row := range histories {
		refs, err := decodeTimelineCheckpointRefs(row.refsJSON)
		if err != nil {
			return nil, err
		}
		progress := &TaskTimelineProgress{
			OperationID:    row.operationID,
			ActorID:        row.actorID,
			State:          row.state,
			Phase:          row.phase,
			Summary:        row.summary,
			NextAction:     row.nextAction,
			CheckpointRefs: refs,
			StartedAt:      row.startedAt,
		}
		if row.completed.Valid {
			value := int(row.completed.Int64)
			progress.CheckpointCompleted = &value
		}
		if row.total.Valid {
			value := int(row.total.Int64)
			progress.CheckpointTotal = &value
		}
		if row.generatedCommentID.Valid && row.generatedCommentID.String != "" {
			generatedComments[row.generatedCommentID.String] = struct{}{}
		}
		eventCursor := int64(0)
		if row.progressEventCursor.Valid {
			eventCursor = row.progressEventCursor.Int64
			if eventCursor > 0 {
				historyEvents[eventCursor] = struct{}{}
			}
		}
		item := TaskTimelineItem{
			ID:        row.id,
			Kind:      "agent_progress",
			TaskID:    row.taskID,
			Actor:     timelineActor(sql.NullString{String: row.actorID, Valid: row.actorID != ""}, row.actorKind, row.actorName),
			CreatedAt: row.createdAt,
			Progress:  progress,
			Comment:   nil,
			Change:    nil,
		}
		candidates = append(candidates, timelineCandidate{item: item, key: newTimelineSortKey(row.createdAt, item.Kind, item.ID, eventCursor)})
	}

	comments, err := s.listProjectTimelineComments(ctx, projectID)
	if err != nil {
		return nil, err
	}
	commentEventCursors := make(map[string]int64, len(comments))
	for _, row := range comments {
		if _, generated := generatedComments[row.comment.ID]; generated {
			continue
		}
		item := TaskTimelineItem{
			ID:        row.comment.ID,
			Kind:      "comment",
			TaskID:    row.comment.TaskID,
			Actor:     timelineActor(row.actorID, row.actorKind, row.actorName),
			CreatedAt: row.comment.CreatedAt,
			Progress:  nil,
			Comment:   &row.comment,
			Change:    nil,
		}
		candidates = append(candidates, timelineCandidate{item: item, key: newTimelineSortKey(row.comment.CreatedAt, item.Kind, item.ID, 0)})
		commentEventCursors[row.comment.ID] = 0
	}

	events, err := s.listProjectTimelineEvents(ctx, projectID)
	if err != nil {
		return nil, err
	}
	for _, row := range events {
		event := row.event
		commentID := timelineCommentID(event)
		if event.Type == "comment.created" && commentID != "" {
			if _, generated := generatedComments[commentID]; generated {
				continue
			}
			if _, exists := commentEventCursors[commentID]; exists {
				// The comment itself is the timeline item; do not emit a second
				// system row for its comment.created event.
				commentEventCursors[commentID] = event.Cursor
				for index := range candidates {
					if candidates[index].item.Kind == "comment" && candidates[index].item.ID == commentID {
						candidates[index].key.eventCursor = event.Cursor
						break
					}
				}
				continue
			}
			// A malformed or orphaned comment event remains visible as a
			// generic task change rather than disappearing from legacy history.
		}
		if event.Type == "task.progressed" {
			if _, linked := historyEvents[event.Cursor]; linked {
				continue
			}
			// An event written before migration 011 has no structured history
			// row. Keep it as a generic change; never infer rich progress data.
		}
		taskID := ""
		if event.TaskID != nil {
			taskID = *event.TaskID
		}
		item := TaskTimelineItem{
			ID:        event.ID,
			Kind:      "task_change",
			TaskID:    taskID,
			Actor:     timelineActor(row.actorID, row.actorKind, row.actorName),
			CreatedAt: event.CreatedAt,
			Progress:  nil,
			Comment:   nil,
			Change: &TaskTimelineChange{
				EventID:   event.ID,
				EventType: event.Type,
				Payload:   event.Payload,
			},
		}
		candidates = append(candidates, timelineCandidate{item: item, key: newTimelineSortKey(event.CreatedAt, item.Kind, item.ID, event.Cursor)})
	}
	return candidates, nil
}

func timelineCommentID(event Event) string {
	if event.Type != "comment.created" || len(event.Payload) == 0 {
		return ""
	}
	var payload struct {
		CommentID string `json:"comment_id"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.CommentID)
}

func (s *Store) timelineActorForID(ctx context.Context, actorID sql.NullString) *TimelineActor {
	if !actorID.Valid || actorID.String == "" {
		return nil
	}
	var kind, name string
	if err := s.DB.QueryRowContext(ctx, `SELECT kind, name FROM actors WHERE id=?`, actorID.String).Scan(&kind, &name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		// Actor enrichment is best effort for event rows. The event itself is
		// still safe and useful if an actor disappears during a read.
		return nil
	}
	return &TimelineActor{ID: actorID.String, Kind: kind, Name: name}
}

// TimelineCursorForTest exposes the opaque encoding to package-local tests
// without making the wire cursor parser part of the public API contract.
func TimelineCursorForTest(createdAt, kind, id string, eventCursor int64) string {
	cursor, _ := encodeTaskTimelineCursor(newTimelineSortKey(createdAt, kind, id, eventCursor))
	return cursor
}

// ParseTimelineCursorForTest mirrors the HTTP before validation for focused
// store tests. It intentionally returns only the normalized timestamp and
// event sequence needed by callers asserting stable boundaries.
func ParseTimelineCursorForTest(value string) (string, int64, error) {
	key, err := decodeTaskTimelineCursor(value)
	if err != nil {
		return "", 0, err
	}
	return key.createdRaw, key.eventCursor, nil
}
