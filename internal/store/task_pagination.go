package store

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// TaskSort names the stable orderings exposed by the project task
// collection. Board order is the default and intentionally matches the
// visual column/task order used by the web client.
const (
	TaskSortBoard     = "board"
	TaskSortPosition  = "position"
	TaskSortNumber    = "number"
	TaskSortCreatedAt = "created_at"
	TaskSortUpdatedAt = "updated_at"
	TaskSortPriority  = "priority"
	TaskSortTitle     = "title"
)

const taskCursorPrefix = "tc1."

// ErrTaskCollectionChanged identifies a keyset continuation whose source
// collection changed after the cursor was issued. It is deliberately
// separate from ordinary resource conflicts so HTTP clients can restart the
// collection from its first page without treating the response as a task
// mutation failure.
var ErrTaskCollectionChanged = fmt.Errorf("%w: task collection changed", ErrConflict)

// taskTitleSortKey mirrors SQLite's built-in lower() function for the title
// keyset order. SQLite lower() folds only ASCII A-Z and leaves every other
// byte unchanged, while strings.ToLower would fold Unicode letters too.
func taskTitleSortKey(title string) string {
	var key strings.Builder
	key.Grow(len(title))
	for i := 0; i < len(title); i++ {
		b := title[i]
		if b >= 'A' && b <= 'Z' {
			b += 'a' - 'A'
		}
		key.WriteByte(b)
	}
	return key.String()
}

// TaskCursor is an opaque, versioned-by-format (but not secret) keyset marker.
// It is intentionally scoped to the ordering that produced it. The server
// still applies project authorization and every supplied filter on the next
// request, so a cursor never grants access to another project.
type TaskCursor struct {
	Version                int     `json:"v"`
	Sort                   string  `json:"sort"`
	Descending             bool    `json:"desc"`
	ProjectID              string  `json:"project_id"`
	CollectionRevision     int64   `json:"revision"`
	ReadAt                 string  `json:"read_at"`
	TaskCollectionRevision int64   `json:"task_collection_revision"`
	ColumnID               string  `json:"column_id,omitempty"`
	Position               float64 `json:"position,omitempty"`
	Number                 int     `json:"number,omitempty"`
	ID                     string  `json:"id"`
	Value                  string  `json:"value,omitempty"`
	PriorityRank           int     `json:"priority_rank,omitempty"`
}

// NormalizeTaskSort validates and canonicalizes a public sort name. Aliases
// are kept intentionally small so the SQL order list remains allow-listed.
func NormalizeTaskSort(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", TaskSortBoard:
		return TaskSortBoard, nil
	case TaskSortPosition:
		return TaskSortPosition, nil
	case TaskSortNumber:
		return TaskSortNumber, nil
	case TaskSortCreatedAt, "created", "created-at":
		return TaskSortCreatedAt, nil
	case TaskSortUpdatedAt, "updated", "updated-at":
		return TaskSortUpdatedAt, nil
	case TaskSortPriority:
		return TaskSortPriority, nil
	case TaskSortTitle, "name":
		return TaskSortTitle, nil
	default:
		return "", fmt.Errorf("sort must be board, position, number, created_at, updated_at, priority, or title")
	}
}

// EncodeTaskCursor serializes the last row of a page together with the
// collection snapshot that produced it. The prefix makes the format
// distinguishable from retained decimal/base64 offset cursors. Callers must
// provide the project event revision, fixed read timestamp, and task-collection
// revision from
// the same collection read; a cursor without those values cannot be safely
// continued and returns an empty string.
func EncodeTaskCursor(task Task, sort string, descending bool, collectionRevision int64, readAt time.Time, taskCollectionRevision int64) string {
	if collectionRevision < 0 || readAt.IsZero() || taskCollectionRevision < 0 {
		return ""
	}
	canonical, err := NormalizeTaskSort(sort)
	if err != nil {
		canonical = TaskSortBoard
	}
	cursor := TaskCursor{
		Version:                1,
		Sort:                   canonical,
		Descending:             descending,
		ProjectID:              task.ProjectID,
		CollectionRevision:     collectionRevision,
		ReadAt:                 readAt.UTC().Format(time.RFC3339Nano),
		TaskCollectionRevision: taskCollectionRevision,
		ColumnID:               task.ColumnID,
		Position:               task.Position,
		Number:                 task.Number,
		ID:                     task.ID,
	}
	switch canonical {
	case TaskSortCreatedAt:
		cursor.Value = task.CreatedAt
	case TaskSortUpdatedAt:
		cursor.Value = task.UpdatedAt
	case TaskSortTitle:
		cursor.Value = taskTitleSortKey(task.Title)
	case TaskSortPriority:
		cursor.PriorityRank = taskPriorityRank(task.Priority)
	}
	payload, err := json.Marshal(cursor)
	if err != nil {
		// All fields are primitive and therefore this is unreachable. Returning
		// an empty cursor is safer than exposing a partially encoded marker if
		// the struct gains an unsupported field in the future.
		return ""
	}
	return taskCursorPrefix + base64.RawURLEncoding.EncodeToString(payload)
}

// DecodeTaskCursor validates the opaque cursor returned by EncodeTaskCursor.
// Decimal and base64-encoded decimal offsets are deliberately not accepted
// here; the HTTP compatibility layer handles those as retained offset
// cursors before invoking the keyset endpoint.
func DecodeTaskCursor(value string) (TaskCursor, error) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, taskCursorPrefix) {
		return TaskCursor{}, errors.New("cursor is not a task keyset cursor")
	}
	encoded := strings.TrimPrefix(value, taskCursorPrefix)
	if encoded == "" {
		return TaskCursor{}, errors.New("cursor must not be empty")
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return TaskCursor{}, errors.New("cursor is malformed")
	}
	var cursor TaskCursor
	if err := json.Unmarshal(payload, &cursor); err != nil {
		return TaskCursor{}, errors.New("cursor is malformed")
	}
	canonical, err := NormalizeTaskSort(cursor.Sort)
	if err != nil || cursor.Version != 1 || canonical != cursor.Sort || cursor.ProjectID == "" || cursor.CollectionRevision < 0 || cursor.ReadAt == "" || cursor.TaskCollectionRevision < 0 || cursor.ID == "" || cursor.ColumnID == "" {
		return TaskCursor{}, errors.New("cursor is invalid")
	}
	readAt, err := time.Parse(time.RFC3339Nano, cursor.ReadAt)
	if err != nil || readAt.After(time.Now().UTC()) {
		return TaskCursor{}, errors.New("cursor is invalid")
	}
	switch canonical {
	case TaskSortCreatedAt, TaskSortUpdatedAt, TaskSortTitle:
		if cursor.Value == "" {
			return TaskCursor{}, errors.New("cursor is invalid")
		}
	case TaskSortPriority:
		if cursor.PriorityRank < 0 || cursor.PriorityRank > 3 {
			return TaskCursor{}, errors.New("cursor is invalid")
		}
	}
	return cursor, nil
}

func taskPriorityRank(priority string) int {
	switch priority {
	case "urgent":
		return 0
	case "high":
		return 1
	case "normal":
		return 2
	case "low":
		return 3
	default:
		return 4
	}
}

func taskPriorityExpression() string {
	return "CASE t.priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 ELSE 3 END"
}

func taskOrderSQL(sort string, descending bool) (string, error) {
	canonical, err := NormalizeTaskSort(sort)
	if err != nil {
		return "", err
	}
	direction := "ASC"
	if descending {
		direction = "DESC"
	}
	// Every order has a unique task-id tie breaker. This is required for a
	// keyset cursor to be gap-free when many tasks share the same position or
	// timestamp.
	switch canonical {
	case TaskSortBoard:
		return "c.position " + direction + ", c.id " + direction + ", t.position " + direction + ", t.number " + direction + ", t.id " + direction, nil
	case TaskSortPosition:
		return "t.position " + direction + ", t.number " + direction + ", t.id " + direction, nil
	case TaskSortNumber:
		return "t.number " + direction + ", t.id " + direction, nil
	case TaskSortCreatedAt:
		return "t.created_at " + direction + ", t.number " + direction + ", t.id " + direction, nil
	case TaskSortUpdatedAt:
		return "t.updated_at " + direction + ", t.number " + direction + ", t.id " + direction, nil
	case TaskSortPriority:
		return taskPriorityExpression() + " " + direction + ", t.number " + direction + ", t.id " + direction, nil
	case TaskSortTitle:
		return "lower(t.title) " + direction + ", t.number " + direction + ", t.id " + direction, nil
	default:
		return "", errors.New("unsupported task sort")
	}
}

// appendTaskCursorPredicate adds the strict tuple comparison for the cursor
// order. The board tuple uses the cursor's column ID in a subquery so the
// task response need not carry internal column-position metadata.
func appendTaskCursorPredicate(query string, args []any, cursor TaskCursor, descending bool) (string, []any, error) {
	operator := ">"
	if descending {
		operator = "<"
	}
	columnPosition := "(SELECT position FROM columns WHERE id=?)"
	var predicate string
	switch cursor.Sort {
	case TaskSortBoard:
		predicate = "(c.position " + operator + " " + columnPosition +
			" OR (c.position = " + columnPosition + " AND (c.id " + operator + " ? OR (c.id = ? AND (t.position " + operator + " ? OR (t.position = ? AND (t.number " + operator + " ? OR (t.number = ? AND t.id " + operator + " ?))))))))"
		args = append(args, cursor.ColumnID, cursor.ColumnID, cursor.ColumnID, cursor.ColumnID, cursor.Position, cursor.Position, cursor.Number, cursor.Number, cursor.ID)
	case TaskSortPosition:
		predicate = "(t.position " + operator + " ? OR (t.position = ? AND (t.number " + operator + " ? OR (t.number = ? AND t.id " + operator + " ?))))"
		args = append(args, cursor.Position, cursor.Position, cursor.Number, cursor.Number, cursor.ID)
	case TaskSortNumber:
		predicate = "(t.number " + operator + " ? OR (t.number = ? AND t.id " + operator + " ?))"
		args = append(args, cursor.Number, cursor.Number, cursor.ID)
	case TaskSortCreatedAt, TaskSortUpdatedAt, TaskSortTitle:
		column := "t.created_at"
		if cursor.Sort == TaskSortUpdatedAt {
			column = "t.updated_at"
		} else if cursor.Sort == TaskSortTitle {
			column = "lower(t.title)"
		}
		predicate = "(" + column + " " + operator + " ? OR (" + column + " = ? AND (t.number " + operator + " ? OR (t.number = ? AND t.id " + operator + " ?))))"
		args = append(args, cursor.Value, cursor.Value, cursor.Number, cursor.Number, cursor.ID)
	case TaskSortPriority:
		expression := taskPriorityExpression()
		predicate = "(" + expression + " " + operator + " ? OR (" + expression + " = ? AND (t.number " + operator + " ? OR (t.number = ? AND t.id " + operator + " ?))))"
		args = append(args, cursor.PriorityRank, cursor.PriorityRank, cursor.Number, cursor.Number, cursor.ID)
	default:
		return "", args, errors.New("unsupported task cursor sort")
	}
	return query + " AND " + predicate, args, nil
}
