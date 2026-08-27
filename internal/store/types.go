package store

import (
	"context"
	cryptorand "crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrNotFound         = errors.New("not found")
	ErrConflict         = errors.New("conflict")
	ErrPrecondition     = errors.New("precondition required")
	ErrForbidden        = errors.New("forbidden")
	ErrAlreadyExists    = errors.New("already exists")
	ErrInvalid          = errors.New("invalid input")
	ErrClaimUnavailable = errors.New("claim unavailable")
)

type Error struct {
	Kind    error
	Message string
	Details any
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Kind.Error()
}
func (e *Error) Unwrap() error { return e.Kind }

func invalid(message string, details any) error {
	return &Error{Kind: ErrInvalid, Message: message, Details: details}
}
func notFound(message string) error { return &Error{Kind: ErrNotFound, Message: message} }
func conflict(message string, details any) error {
	return &Error{Kind: ErrConflict, Message: message, Details: details}
}
func forbidden(message string) error { return &Error{Kind: ErrForbidden, Message: message} }

type Actor struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Email       *string  `json:"email,omitempty"`
	Admin       bool     `json:"admin"`
	DisabledAt  *string  `json:"disabled_at,omitempty"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
	ProjectIDs  []string `json:"project_ids,omitempty"`
	Tokens      []Token  `json:"tokens,omitempty"`
}

type Token struct {
	ID         string   `json:"id"`
	AgentID    string   `json:"agent_id,omitempty"`
	ActorID    string   `json:"actor_id"`
	Name       string   `json:"name"`
	Scopes     []string `json:"scopes"`
	ProjectIDs []string `json:"project_ids"`
	ExpiresAt  *string  `json:"expires_at,omitempty"`
	CreatedAt  string   `json:"created_at"`
	LastUsedAt *string  `json:"last_used_at,omitempty"`
}

type Project struct {
	ID                 string  `json:"id"`
	Key                string  `json:"key"`
	Slug               string  `json:"slug"`
	Name               string  `json:"name"`
	Description        string  `json:"description"`
	Color              string  `json:"color"`
	Favorite           bool    `json:"favorite"`
	ArchivedAt         *string `json:"archived_at,omitempty"`
	CreatedAt          string  `json:"created_at"`
	UpdatedAt          string  `json:"updated_at"`
	TaskCount          int     `json:"task_count,omitempty"`
	CompletedCount     int     `json:"completed_count,omitempty"`
	CompletedTaskCount int     `json:"completed_task_count,omitempty"`
	OpenTaskCount      int     `json:"open_task_count,omitempty"`
	OverdueTaskCount   int     `json:"overdue_task_count,omitempty"`
	ColumnCount        int     `json:"column_count,omitempty"`
}

type Column struct {
	ID            string `json:"id"`
	ProjectID     string `json:"project_id"`
	Name          string `json:"name"`
	SemanticState string `json:"semantic_state"`
	Position      int    `json:"position"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

type Label struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	Color     string `json:"color"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type Task struct {
	ID             string  `json:"id"`
	Number         int     `json:"number"`
	Key            string  `json:"key"`
	ProjectID      string  `json:"project_id"`
	Kind           string  `json:"kind"`
	ColumnID       string  `json:"column_id"`
	Title          string  `json:"title"`
	Description    string  `json:"description"`
	Priority       string  `json:"priority"`
	Position       float64 `json:"position"`
	Assignee       *string `json:"assignee,omitempty"`
	ClaimedBy      *string `json:"claimed_by,omitempty"`
	ClaimExpiresAt *string `json:"claim_expires_at,omitempty"`
	DueAt          *string `json:"due_at,omitempty"`
	Version        int64   `json:"version"`
	CompletedAt    *string `json:"completed_at,omitempty"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
	Labels         []Label `json:"labels"`
	CommentCount   int     `json:"comment_count"`
	Bug            *Bug    `json:"bug,omitempty"`
}

// Bug contains the typed metadata associated with a bug task. The detail row
// is deliberately kept separate from tasks so existing task rows and clients
// remain valid while a bug can still carry a richer lifecycle than a task.
// Severity and resolution are nullable until triage/resolution respectively.
type Bug struct {
	ReporterID        string  `json:"reporter_id"`
	Severity          *string `json:"severity,omitempty"`
	ActualBehavior    string  `json:"actual_behavior"`
	ExpectedBehavior  string  `json:"expected_behavior,omitempty"`
	ReproductionSteps string  `json:"reproduction_steps,omitempty"`
	Environment       string  `json:"environment,omitempty"`
	AffectedVersion   string  `json:"affected_version,omitempty"`
	Resolution        *string `json:"resolution,omitempty"`
	ResolvedBy        *string `json:"resolved_by,omitempty"`
	ResolvedAt        *string `json:"resolved_at,omitempty"`
	DuplicateOf       *string `json:"duplicate_of,omitempty"`
}

// BugDetails is retained as an alias for callers that prefer the persistence
// terminology. Task.Bug uses the shorter API-facing Bug name.
type BugDetails = Bug

// BugInput is the patch/create shape for bug metadata. Set flags distinguish
// an omitted field from an explicit JSON null, which is important for nullable
// severity and for clearing optional text on a patch.
type BugInput struct {
	Severity             *string
	SeveritySet          bool
	ActualBehavior       *string
	ActualBehaviorSet    bool
	ExpectedBehavior     *string
	ExpectedBehaviorSet  bool
	ReproductionSteps    *string
	ReproductionStepsSet bool
	Environment          *string
	EnvironmentSet       bool
	AffectedVersion      *string
	AffectedVersionSet   bool
}

// TriageBugInput changes only the pre-resolution severity. A nil severity is
// accepted when SeveritySet is true to explicitly return a bug to an
// untriaged state.
type TriageBugInput struct {
	Severity    *string
	SeveritySet bool
	Priority    *string
	Assignee    *string
	AssigneeSet bool
	ColumnID    *string
}

// ResolveBugInput supplies the terminal resolution. DuplicateOf is required
// when Resolution is "duplicate" and ignored/forbidden for other resolutions.
type ResolveBugInput struct {
	Resolution     string
	DuplicateOf    *string
	DuplicateOfSet bool
	Note           string
}

type Comment struct {
	ID        string `json:"id"`
	TaskID    string `json:"task_id"`
	ActorID   string `json:"actor_id"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type Event struct {
	Cursor    int64           `json:"cursor"`
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	ActorID   *string         `json:"actor_id,omitempty"`
	ProjectID *string         `json:"project_id,omitempty"`
	TaskID    *string         `json:"task_id,omitempty"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt string          `json:"created_at"`
}

type TaskFilter struct {
	State        string
	Column       string
	Priority     string
	Label        string
	Assignee     string
	Kind         string
	Severity     string
	Reporter     string
	Resolution   string
	Query        string
	UpdatedAfter *time.Time
	// ProjectIDs restricts global issue listings to an explicit allow-list.
	// A non-nil empty slice intentionally matches no projects.
	ProjectIDs []string
	// Cursor is an opaque row offset for task and my-work listings. Event
	// cursors remain monotonic database cursors via EventFilter.After.
	Cursor int
	Limit  int
}

type ProjectFilter struct {
	IncludeArchived bool
	FavoriteOnly    bool
	// ProjectIDs restricts the query to an explicit allow-list. A non-nil empty
	// slice intentionally matches no projects, which is useful for scoped
	// bearer tokens with no effective project access.
	ProjectIDs []string
}

type EventFilter struct {
	After     int64
	ProjectID string
	// ProjectIDs restricts the query to an explicit allow-list. A non-nil empty
	// slice intentionally matches no events.
	ProjectIDs []string
	Limit      int
}

type Roadmap struct {
	Project              *Project         `json:"project,omitempty"`
	TaskTotals           int              `json:"task_total"`
	Completed            int              `json:"completed"`
	CompletionPercent    float64          `json:"completion_percent"`
	StateCounts          map[string]int   `json:"state_counts"`
	Overdue              int              `json:"overdue"`
	DueSoon              int              `json:"due_soon"`
	Upcoming             []Task           `json:"upcoming"`
	RecentActivity       []Event          `json:"recent_activity"`
	Projects             []map[string]any `json:"projects,omitempty"`
	TotalTasks           int              `json:"total_tasks,omitempty"`
	CompletedTasks       int              `json:"completed_tasks,omitempty"`
	CompletionPercentage float64          `json:"completion_percentage,omitempty"`
	OverdueCount         int              `json:"overdue_count,omitempty"`
	DueSoonCount         int              `json:"due_soon_count,omitempty"`
	UpcomingTasks        []Task           `json:"upcoming_tasks"`
}

type Store struct{ DB *sql.DB }

func New(database *sql.DB) *Store { return &Store{DB: database} }

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func parseTime(value string) (*string, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, fmt.Errorf("timestamp must be RFC3339: %w", err)
	}
	v := t.UTC().Format(time.RFC3339Nano)
	return &v, nil
}

func nullableString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func boolValue(value int) bool { return value != 0 }

func eventPayload(value any) string {
	if value == nil {
		return "{}"
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

func actorFromRow(scanner interface{ Scan(...any) error }) (Actor, error) {
	var a Actor
	var email, disabled sql.NullString
	var admin int
	if err := scanner.Scan(&a.ID, &a.Kind, &a.Name, &email, &admin, &disabled, &a.CreatedAt, &a.UpdatedAt, &a.Description); err != nil {
		return Actor{}, err
	}
	a.Email, a.DisabledAt, a.Admin = nullableString(email), nullableString(disabled), boolValue(admin)
	return a, nil
}

func projectFromRow(scanner interface{ Scan(...any) error }) (Project, error) {
	var p Project
	var archived sql.NullString
	var favorite int
	if err := scanner.Scan(&p.ID, &p.Key, &p.Slug, &p.Name, &p.Description, &p.Color, &favorite, &archived, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return Project{}, err
	}
	p.Favorite, p.ArchivedAt = boolValue(favorite), nullableString(archived)
	return p, nil
}

func columnFromRow(scanner interface{ Scan(...any) error }) (Column, error) {
	var c Column
	if err := scanner.Scan(&c.ID, &c.ProjectID, &c.Name, &c.SemanticState, &c.Position, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return Column{}, err
	}
	return c, nil
}

func labelFromRow(scanner interface{ Scan(...any) error }) (Label, error) {
	var l Label
	if err := scanner.Scan(&l.ID, &l.ProjectID, &l.Name, &l.Color, &l.CreatedAt, &l.UpdatedAt); err != nil {
		return Label{}, err
	}
	return l, nil
}

func taskFromRow(scanner interface{ Scan(...any) error }) (Task, error) {
	var t Task
	var assignee, claimed, claimExpiry, due, completed sql.NullString
	if err := scanner.Scan(&t.ID, &t.Number, &t.ProjectID, &t.Kind, &t.ColumnID, &t.Title, &t.Description, &t.Priority, &t.Position, &assignee, &claimed, &claimExpiry, &due, &t.Version, &completed, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return Task{}, err
	}
	t.Key = "" // populated by callers because it requires a project key.
	t.Assignee, t.ClaimedBy, t.ClaimExpiresAt, t.DueAt, t.CompletedAt = nullableString(assignee), nullableString(claimed), nullableString(claimExpiry), nullableString(due), nullableString(completed)
	return t, nil
}

const taskColumns = `t.id, t.number, t.project_id, t.kind, t.column_id, t.title, t.description,
	t.priority, t.position, t.assignee_id, t.claimed_by, t.claim_expires_at, t.due_at,
	t.version, t.completed_at, t.created_at, t.updated_at`

func (s *Store) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func insertEvent(ctx context.Context, tx *sql.Tx, eventType, actorID, projectID, taskID string, payload any) (int64, error) {
	id := newID()
	created := now()
	result, err := tx.ExecContext(ctx, `INSERT INTO events(id, type, actor_id, project_id, task_id, payload, created_at) VALUES (?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?)`, id, eventType, actorID, projectID, taskID, eventPayload(payload), created)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func newID() string {
	// IDs are opaque and URL-safe. Randomness failure must fail closed: using a
	// timestamp as an ID would make security-sensitive identifiers predictable.
	buf := make([]byte, 16)
	if _, err := cryptorand.Read(buf); err == nil {
		return fmt.Sprintf("%x", buf)
	}
	panic("crypto/rand unavailable")
}
