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

	// Dependency mutations use distinct sentinels so the HTTP layer can map
	// each graph validation failure to a stable response code without parsing
	// human-readable error text. They intentionally unwrap through Error just
	// like the existing store errors.
	ErrDependencySelfReference = errors.New("dependency_self_reference")
	ErrDependencyCrossProject  = errors.New("dependency_cross_project")
	ErrDependencyAlreadyExists = errors.New("dependency_already_exists")
	ErrDependencyLimitExceeded = errors.New("dependency_limit_exceeded")
	ErrDependencyCycle         = errors.New("dependency_cycle")
	ErrDependencyNotFound      = errors.New("dependency_not_found")
	ErrUnmetDependencies       = errors.New("unmet_dependencies")
	ErrDependencyInUse         = errors.New("dependency_in_use")
	ErrChecklistLimitExceeded  = errors.New("checklist_limit_exceeded")
	ErrChecklistIncomplete     = errors.New("checklist_incomplete")
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
	ID                        string  `json:"id"`
	Key                       string  `json:"key"`
	Slug                      string  `json:"slug"`
	Name                      string  `json:"name"`
	Description               string  `json:"description"`
	Color                     string  `json:"color"`
	Favorite                  bool    `json:"favorite"`
	ArchivedAt                *string `json:"archived_at,omitempty"`
	ChecklistCompletionPolicy string  `json:"checklist_completion_policy"`
	CreatedAt                 string  `json:"created_at"`
	UpdatedAt                 string  `json:"updated_at"`
	TaskCount                 int     `json:"task_count,omitempty"`
	CompletedCount            int     `json:"completed_count,omitempty"`
	CompletedTaskCount        int     `json:"completed_task_count,omitempty"`
	OpenTaskCount             int     `json:"open_task_count,omitempty"`
	OverdueTaskCount          int     `json:"overdue_task_count,omitempty"`
	ColumnCount               int     `json:"column_count,omitempty"`
	Version                   int64   `json:"version,omitempty"`
}

type Column struct {
	ID              string  `json:"id"`
	ProjectID       string  `json:"project_id"`
	Name            string  `json:"name"`
	SemanticState   string  `json:"semantic_state"`
	Position        int     `json:"position"`
	ArchivedAt      *string `json:"archived_at,omitempty"`
	OrderingVersion int64   `json:"ordering_version,omitempty"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
	Version         int64   `json:"version,omitempty"`
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
	ID                string               `json:"id"`
	Number            int                  `json:"number"`
	Key               string               `json:"key"`
	ProjectID         string               `json:"project_id"`
	Kind              string               `json:"kind"`
	ColumnID          string               `json:"column_id"`
	Title             string               `json:"title"`
	Description       string               `json:"description"`
	Priority          string               `json:"priority"`
	Position          float64              `json:"position"`
	Assignee          *string              `json:"assignee,omitempty"`
	ClaimedBy         *string              `json:"claimed_by,omitempty"`
	ClaimExpiresAt    *string              `json:"claim_expires_at,omitempty"`
	DueAt             *string              `json:"due_at,omitempty"`
	Version           int64                `json:"version"`
	CompletedAt       *string              `json:"completed_at,omitempty"`
	CreatedAt         string               `json:"created_at"`
	UpdatedAt         string               `json:"updated_at"`
	Labels            []Label              `json:"labels"`
	CommentCount      int                  `json:"comment_count"`
	Bug               *Bug                 `json:"bug,omitempty"`
	AgentWork         *AgentWork           `json:"agent_work,omitempty"`
	DependencySummary DependencySummary    `json:"dependency_summary"`
	Checklist         []TaskChecklistItem  `json:"checklist"`
	ChecklistSummary  TaskChecklistSummary `json:"checklist_summary"`
	// ParentTaskID is the persisted hierarchy edge. ParentID is a response
	// alias retained for clients that use the shorter relation naming; both
	// values are populated from the same live, same-project parent row.
	ParentTaskID     *string                 `json:"parent_task_id,omitempty"`
	ParentID         *string                 `json:"parent_id,omitempty"`
	Parent           *TaskHierarchyReference `json:"parent,omitempty"`
	HierarchySummary HierarchySummary        `json:"hierarchy_summary"`
}

// TaskChecklistItem is one ordered, actor-aware acceptance criterion on a
// task. CompletedAt and CompletedBy are nullable so an item can be reopened
// without losing its stable identity or creation timestamp.
type TaskChecklistItem struct {
	ID          string  `json:"id"`
	TaskID      string  `json:"task_id"`
	Text        string  `json:"text"`
	Position    int     `json:"position"`
	Completed   bool    `json:"completed"`
	CompletedAt *string `json:"completed_at"`
	CompletedBy *string `json:"completed_by"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

// TaskChecklistSummary is included on task reads and board collections. The
// completion warning is true only when a completed task still has open items
// under the project's default warn policy.
type TaskChecklistSummary struct {
	Total            int     `json:"total"`
	Completed        int     `json:"completed"`
	Open             int     `json:"open"`
	Percent          float64 `json:"percent"`
	CompletionPolicy string  `json:"completion_policy"`
	Warning          bool    `json:"warning"`
}

// ChecklistItemInput is used for both create and partial update operations.
// A nil field is omitted; CompletedSet and PositionSet distinguish explicit
// false/zero values from omission.
type ChecklistItemInput struct {
	Text         *string
	Completed    *bool
	CompletedSet bool
	Position     *int
	PositionSet  bool
}

// ChecklistReorderInput is normalized by the HTTP layer to a complete list of
// item IDs. Positions are allocated contiguously by the store.
type ChecklistReorderInput struct {
	ItemIDs []string
}

// ChecklistCollection is the public read shape for a task checklist. It
// carries the owning task's current version so clients can send If-Match on
// the next mutation without fetching task details separately.
type ChecklistCollection struct {
	TaskID  string               `json:"task_id"`
	Version int64                `json:"version"`
	Items   []TaskChecklistItem  `json:"data"`
	Summary TaskChecklistSummary `json:"summary"`
}

// DependencySummary is the bounded, derived graph state embedded in every
// task response.  Counts include only direct relationships to live,
// same-project tasks; unmet prerequisites are those not currently in a
// completed semantic column with a completion timestamp.
type DependencySummary struct {
	PrerequisiteCount      int  `json:"prerequisite_count"`
	UnmetPrerequisiteCount int  `json:"unmet_prerequisite_count"`
	DependentCount         int  `json:"dependent_count"`
	Blocked                bool `json:"blocked"`
}

// TaskReference is the compact relation shape used by expanded dependency
// reads. Satisfied describes the referenced task itself: for a
// prerequisite it means the prerequisite is complete; for a dependent it
// means that dependent is complete.
type TaskReference struct {
	ID          string  `json:"id"`
	Key         string  `json:"key"`
	Title       string  `json:"title"`
	CompletedAt *string `json:"completed_at"`
	Satisfied   bool    `json:"satisfied"`
}

// TaskDependencies contains the direct graph edges in both directions.
// Relations are bounded by the mutation limit and are returned in a stable
// deterministic order.
type TaskDependencies struct {
	Prerequisites []TaskReference `json:"prerequisites"`
	Dependents    []TaskReference `json:"dependents"`
}

// TaskHierarchyReference is the bounded relation shape used by hierarchy
// reads. It carries enough server-owned state for board and drawer rollups
// without recursively embedding full tasks.
type TaskHierarchyReference struct {
	ID            string     `json:"id"`
	Number        int        `json:"number"`
	Key           string     `json:"key"`
	ProjectID     string     `json:"project_id"`
	Title         string     `json:"title"`
	Kind          string     `json:"kind"`
	ColumnID      string     `json:"column_id"`
	SemanticState string     `json:"semantic_state"`
	State         string     `json:"state,omitempty"`
	Version       int64      `json:"version"`
	ParentID      *string    `json:"parent_id,omitempty"`
	CompletedAt   *string    `json:"completed_at,omitempty"`
	AgentWork     *AgentWork `json:"agent_work,omitempty"`
}

// HierarchySummary is derived from live child rows at read time. It never
// trusts browser-maintained counters or cached client state.
type HierarchySummary struct {
	ChildCount          int            `json:"child_count"`
	CompletedChildCount int            `json:"completed_child_count"`
	CompletionPercent   float64        `json:"completion_percent"`
	StateCounts         map[string]int `json:"state_counts"`
	BlockedChildCount   int            `json:"blocked_child_count"`
	LiveAgentWorkCount  int            `json:"live_agent_work_count"`
	ActionNeededCount   int            `json:"action_needed_count"`
	StaleAgentWorkCount int            `json:"stale_agent_work_count"`
}

// TaskHierarchy contains bounded direct children and the complete ancestor
// chain up to MaxTaskHierarchyDepth. Descendants is included as a bounded
// convenience for tree navigation; callers should use children for direct
// editing operations.
type TaskHierarchy struct {
	Parent      *TaskHierarchyReference  `json:"parent,omitempty"`
	Children    []TaskHierarchyReference `json:"children"`
	Ancestors   []TaskHierarchyReference `json:"ancestors"`
	Descendants []TaskHierarchyReference `json:"descendants"`
	Summary     HierarchySummary         `json:"summary"`
}

// AgentWork is the latest progress pulse published for a task. Stale and
// action-needed are derived at read time so clients do not have to infer the
// liveness of a server-owned timestamp.
type AgentWork struct {
	OperationID         string   `json:"operation_id"`
	ActorID             string   `json:"actor_id"`
	State               string   `json:"state"`
	Phase               string   `json:"phase"`
	Summary             string   `json:"summary"`
	NextAction          string   `json:"next_action"`
	CheckpointRefs      []string `json:"checkpoint_refs"`
	CheckpointCompleted *int     `json:"checkpoint_completed,omitempty"`
	CheckpointTotal     *int     `json:"checkpoint_total,omitempty"`
	StartedAt           string   `json:"started_at"`
	UpdatedAt           string   `json:"updated_at"`
	Stale               bool     `json:"stale"`
	ActionNeeded        bool     `json:"action_needed"`
}

// AgentWorkInput is a complete replacement snapshot. Omitted optional text,
// refs, and checkpoint counts clear the corresponding prior values.
type AgentWorkInput struct {
	OperationID         string   `json:"operation_id"`
	State               string   `json:"state"`
	Phase               string   `json:"phase"`
	Summary             string   `json:"summary"`
	NextAction          string   `json:"next_action"`
	CheckpointRefs      []string `json:"checkpoint_refs"`
	CheckpointCompleted *int     `json:"checkpoint_completed"`
	CheckpointTotal     *int     `json:"checkpoint_total"`
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
	ID      string `json:"id"`
	TaskID  string `json:"task_id"`
	ActorID string `json:"actor_id"`
	Body    string `json:"body"`
	// Version is incremented on every edit or tombstone mutation.  It gives
	// comment writes the same optimistic-concurrency protection as task writes.
	Version   int64  `json:"version"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	// Deleted comments are retained for audit/event integrity but are omitted
	// from ordinary comment and timeline comment reads.
	DeletedAt *string `json:"deleted_at,omitempty"`
}

// TimelineActor is the least-privilege actor identity embedded in a task
// activity item.  Timeline reads intentionally do not expose actor email,
// administrator state, token metadata, or project grants.
type TimelineActor struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// TaskTimelineProgress is one immutable structured agent-work pulse.  The
// latest pulse remains available as Task.AgentWork; this shape is the durable
// historical counterpart used by the task timeline.
type TaskTimelineProgress struct {
	OperationID         string   `json:"operation_id"`
	ActorID             string   `json:"actor_id"`
	State               string   `json:"state"`
	Phase               string   `json:"phase"`
	Summary             string   `json:"summary"`
	NextAction          string   `json:"next_action"`
	CheckpointRefs      []string `json:"checkpoint_refs"`
	CheckpointCompleted *int     `json:"checkpoint_completed"`
	CheckpointTotal     *int     `json:"checkpoint_total"`
	StartedAt           string   `json:"started_at"`
}

// TaskTimelineChange retains the original event type and safe payload.  The
// payload is deliberately not converted into guessed rich fields: legacy
// events remain visible exactly as events when no structured history exists.
type TaskTimelineChange struct {
	EventID   string          `json:"event_id"`
	EventType string          `json:"event_type"`
	Payload   json.RawMessage `json:"payload"`
}

// TaskTimelineItem is a discriminated union. Exactly one of Progress,
// Comment, or Change is non-nil for server-produced rows; the other fields are
// still emitted as JSON null so clients can switch on kind without guessing.
// Cursor is an opaque keyset token and is stable for this item across pages.
type TaskTimelineItem struct {
	ID        string                `json:"id"`
	Cursor    string                `json:"cursor"`
	Kind      string                `json:"kind"`
	TaskID    string                `json:"task_id"`
	Actor     *TimelineActor        `json:"actor"`
	CreatedAt string                `json:"created_at"`
	Progress  *TaskTimelineProgress `json:"progress"`
	Comment   *Comment              `json:"comment"`
	Change    *TaskTimelineChange   `json:"change"`
}

// TaskTimelineFilter controls a task-scoped timeline page. Before is the
// opaque cursor returned on an earlier item; an empty value starts at the
// newest activity. Kind may be agent_progress, comment, or task_change.
type TaskTimelineFilter struct {
	Before string
	Kind   string
	Limit  int
}

// TimelineFilter is a descriptive alias retained for callers that prefer a
// shorter name.
type TimelineFilter = TaskTimelineFilter

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
	State      string
	Column     string
	Priority   string
	Label      string
	Assignee   string
	Kind       string
	Severity   string
	Reporter   string
	Resolution string
	// Dependency selects derived graph readiness. "blocked" matches tasks
	// with an unmet prerequisite; "ready" matches tasks with at least one
	// prerequisite and none unmet. Empty leaves the graph state unfiltered.
	Dependency   string
	AgentState   string
	ActionNeeded bool
	LiveWork     bool
	Query        string
	UpdatedAfter *time.Time
	// CursorToken is the opaque keyset cursor used by the large-board task
	// endpoint. Cursor remains an integer offset for retained callers and
	// older collection clients; new task reads prefer CursorToken so inserts
	// and moves before a page boundary cannot cause skips or duplicates.
	CursorToken string
	// Sort selects the stable task ordering. Empty means board order. The
	// HTTP layer validates the public values, while store callers receive the
	// same invalid-input error for unsupported values.
	Sort       string
	Descending bool
	// ProjectIDs restricts global issue listings to an explicit allow-list.
	// A non-nil empty slice intentionally matches no projects.
	ProjectIDs []string
	// Cursor is an opaque row offset for task and my-work listings. Event
	// cursors remain monotonic database cursors via EventFilter.After.
	Cursor int
	Limit  int
}

// SearchSort describes one stable ordering term for global task search and
// saved views.  The HTTP layer validates the field/direction vocabulary before
// a query reaches the store; the store still normalizes values so direct
// callers cannot inject SQL through a saved view.
type SearchSort struct {
	Field     string `json:"field"`
	Direction string `json:"direction"`
}

// SearchFilter is the cross-project task search contract. ProjectIDs is the
// caller's effective project ceiling and is always applied before pagination.
// A non-nil empty slice intentionally matches no projects.
type SearchFilter struct {
	Query       string
	Key         string
	Title       string
	Description string
	Label       string
	State       string
	Priority    string
	Assignee    string
	ClaimOwner  string
	Project     string
	ProjectIDs  []string
	DueFrom     *time.Time
	DueTo       *time.Time
	Sort        []SearchSort
	Cursor      int
	Limit       int
}

// SavedView stores a named search/filter combination. Filters are kept as a
// JSON object because the public search vocabulary can grow without another
// schema migration; the HTTP layer validates keys and values when creating or
// updating a view.
type SavedView struct {
	ID          string         `json:"id"`
	ActorID     string         `json:"owner_id"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Filters     map[string]any `json:"filters"`
	Sort        []SearchSort   `json:"sort"`
	Shared      bool           `json:"shared"`
	CreatedAt   string         `json:"created_at"`
	UpdatedAt   string         `json:"updated_at"`
}

// SavedViewInput is used by create and patch operations. FiltersSet and
// SortSet distinguish an omitted patch field from an explicit empty value.
type SavedViewInput struct {
	Name        *string
	Description *string
	Filters     map[string]any
	FiltersSet  bool
	Sort        []SearchSort
	SortSet     bool
	Shared      *bool
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
	if err := scanner.Scan(&p.ID, &p.Key, &p.Slug, &p.Name, &p.Description, &p.Color, &favorite, &archived, &p.ChecklistCompletionPolicy, &p.CreatedAt, &p.UpdatedAt, &p.Version); err != nil {
		return Project{}, err
	}
	if p.ChecklistCompletionPolicy == "" {
		// Direct store tests and retained fixtures may omit the additive field.
		p.ChecklistCompletionPolicy = "warn"
	}
	p.Favorite, p.ArchivedAt = boolValue(favorite), nullableString(archived)
	return p, nil
}

func columnFromRow(scanner interface{ Scan(...any) error }) (Column, error) {
	var c Column
	var archived sql.NullString
	if err := scanner.Scan(&c.ID, &c.ProjectID, &c.Name, &c.SemanticState, &c.Position, &archived, &c.OrderingVersion, &c.CreatedAt, &c.UpdatedAt, &c.Version); err != nil {
		return Column{}, err
	}
	c.ArchivedAt = nullableString(archived)
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
	var assignee, claimed, claimExpiry, due, completed, parent sql.NullString
	if err := scanner.Scan(&t.ID, &t.Number, &t.ProjectID, &t.Kind, &t.ColumnID, &t.Title, &t.Description, &t.Priority, &t.Position, &assignee, &claimed, &claimExpiry, &due, &t.Version, &completed, &t.CreatedAt, &t.UpdatedAt, &parent); err != nil {
		return Task{}, err
	}
	t.Key = "" // populated by callers because it requires a project key.
	t.Assignee, t.ClaimedBy, t.ClaimExpiresAt, t.DueAt, t.CompletedAt, t.ParentTaskID = nullableString(assignee), nullableString(claimed), nullableString(claimExpiry), nullableString(due), nullableString(completed), nullableString(parent)
	t.ParentID = t.ParentTaskID
	return t, nil
}

const taskColumns = `t.id, t.number, t.project_id, t.kind, t.column_id, t.title, t.description,
	t.priority, t.position, t.assignee_id, t.claimed_by, t.claim_expires_at, t.due_at,
	t.version, t.completed_at, t.created_at, t.updated_at, t.parent_task_id`

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
