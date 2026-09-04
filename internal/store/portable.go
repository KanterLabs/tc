package store

// This file contains the portability format and its transactional importer.
// Portability is deliberately separate from the SQLite backup/restore path:
// exports contain live, user-facing records and imports only insert records
// after the complete archive has been validated.  No import operation ever
// replaces, deletes, or restores the database.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	PortableFormat        = "helm.portable"
	PortableVersion       = 1
	portableMaxProjects   = 100
	portableMaxColumns    = 1000
	portableMaxTasks      = 10000
	portableMaxLabels     = 5000
	portableMaxComments   = 50000
	portableMaxRelations  = 50000
	portableMaxEvents     = 100000
	portableMaxActivity   = 100000
	portableConflictRemap = "remap"
	portableConflictFail  = "fail"
)

// PortableSource identifies the producer without carrying deployment or
// credential metadata.  It gives operators a stable way to inspect an
// archive before importing it.
type PortableSource struct {
	Product string `json:"product"`
	API     string `json:"api"`
}

type PortableProject struct {
	ID          string  `json:"id"`
	Key         string  `json:"key"`
	Slug        string  `json:"slug"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Color       string  `json:"color"`
	Favorite    bool    `json:"favorite"`
	ArchivedAt  *string `json:"archived_at,omitempty"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

type PortableColumn struct {
	ID            string `json:"id"`
	ProjectID     string `json:"project_id"`
	Name          string `json:"name"`
	SemanticState string `json:"semantic_state"`
	Position      int    `json:"position"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

type PortableLabel struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	Color     string `json:"color"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type PortableBug struct {
	ReporterID        string  `json:"reporter_id"`
	Severity          *string `json:"severity,omitempty"`
	ActualBehavior    string  `json:"actual_behavior"`
	ExpectedBehavior  string  `json:"expected_behavior"`
	ReproductionSteps string  `json:"reproduction_steps"`
	Environment       string  `json:"environment"`
	AffectedVersion   string  `json:"affected_version"`
	Resolution        *string `json:"resolution,omitempty"`
	ResolvedBy        *string `json:"resolved_by,omitempty"`
	ResolvedAt        *string `json:"resolved_at,omitempty"`
	DuplicateOf       *string `json:"duplicate_of,omitempty"`
}

type PortableTask struct {
	ID             string       `json:"id"`
	Number         int          `json:"number"`
	ProjectID      string       `json:"project_id"`
	Kind           string       `json:"kind"`
	ColumnID       string       `json:"column_id"`
	Title          string       `json:"title"`
	Description    string       `json:"description"`
	Priority       string       `json:"priority"`
	Position       float64      `json:"position"`
	AssigneeID     *string      `json:"assignee_id,omitempty"`
	ClaimedBy      *string      `json:"claimed_by,omitempty"`
	ClaimExpiresAt *string      `json:"claim_expires_at,omitempty"`
	DueAt          *string      `json:"due_at,omitempty"`
	Version        int64        `json:"version"`
	CompletedAt    *string      `json:"completed_at,omitempty"`
	CreatedAt      string       `json:"created_at"`
	UpdatedAt      string       `json:"updated_at"`
	Bug            *PortableBug `json:"bug,omitempty"`
}

type PortableTaskLabel struct {
	TaskID  string `json:"task_id"`
	LabelID string `json:"label_id"`
}

type PortableDependency struct {
	TaskID         string  `json:"task_id"`
	PrerequisiteID string  `json:"prerequisite_task_id"`
	CreatedBy      *string `json:"created_by,omitempty"`
	CreatedAt      string  `json:"created_at"`
}

type PortableTaskLink struct {
	SourceTaskID string `json:"source_task_id"`
	TargetTaskID string `json:"target_task_id"`
	LinkType     string `json:"link_type"`
	CreatedAt    string `json:"created_at"`
}

type PortableComment struct {
	ID        string `json:"id"`
	TaskID    string `json:"task_id"`
	ActorID   string `json:"actor_id"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type PortableEvent struct {
	Cursor    int64           `json:"cursor"`
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	ActorID   *string         `json:"actor_id,omitempty"`
	ProjectID *string         `json:"project_id,omitempty"`
	TaskID    *string         `json:"task_id,omitempty"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt string          `json:"created_at"`
}

// PortableActorReference contains only safe display metadata.  Password
// hashes, tokens, sessions, and Codex credentials are never portable.
type PortableActorReference struct {
	ID          string  `json:"id"`
	Kind        string  `json:"kind"`
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	DisabledAt  *string `json:"disabled_at,omitempty"`
}

type PortableAgentWork struct {
	TaskID              string   `json:"task_id"`
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
}

type PortableAgentWorkHistory struct {
	ID                  string   `json:"id"`
	TaskID              string   `json:"task_id"`
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
	CreatedAt           string   `json:"created_at"`
	GeneratedCommentID  *string  `json:"generated_comment_id,omitempty"`
	ProgressEventCursor *int64   `json:"progress_event_cursor,omitempty"`
}

type PortableRelationships struct {
	TaskLabels   []PortableTaskLabel  `json:"task_labels"`
	Dependencies []PortableDependency `json:"dependencies"`
	TaskLinks    []PortableTaskLink   `json:"task_links"`
}

type PortableActivity struct {
	Events           []PortableEvent            `json:"events"`
	AgentWork        []PortableAgentWork        `json:"agent_work"`
	AgentWorkHistory []PortableAgentWorkHistory `json:"agent_work_history"`
}

// PortableArchive is the documented JSON portability envelope.  The nested
// relationships/activity sections are canonical.  The flat fields are
// accepted as import aliases for early clients and are omitted by exports.
type PortableArchive struct {
	Format        string                   `json:"format"`
	Version       int                      `json:"version"`
	ExportedAt    string                   `json:"exported_at"`
	Source        PortableSource           `json:"source"`
	Projects      []PortableProject        `json:"projects"`
	Columns       []PortableColumn         `json:"columns"`
	Tasks         []PortableTask           `json:"tasks"`
	Labels        []PortableLabel          `json:"labels"`
	Actors        []PortableActorReference `json:"actors,omitempty"`
	Relationships PortableRelationships    `json:"relationships"`
	Activity      PortableActivity         `json:"activity"`

	TaskLabels       []PortableTaskLabel        `json:"task_labels,omitempty"`
	Dependencies     []PortableDependency       `json:"dependencies,omitempty"`
	TaskLinks        []PortableTaskLink         `json:"task_links,omitempty"`
	Events           []PortableEvent            `json:"events,omitempty"`
	AgentWork        []PortableAgentWork        `json:"agent_work,omitempty"`
	AgentWorkHistory []PortableAgentWorkHistory `json:"agent_work_history,omitempty"`
	Comments         []PortableComment          `json:"comments"`
}

type PortableImportOptions struct {
	// TargetProjectID imports a one-project archive into an existing project.
	// The target is never overwritten; source IDs are mapped to it.
	TargetProjectID string
	// Conflict is "remap" (the default) or "fail".  Remapping never silently
	// overwrites a record and is reported in ImportReport.Remaps.
	Conflict string
	DryRun   bool
	ActorID  string
}

type PortableImportRemap struct {
	Entity string `json:"entity"`
	Source string `json:"source"`
	Target string `json:"target"`
	Field  string `json:"field,omitempty"`
	Reason string `json:"reason"`
}

type PortableImportIssue struct {
	Entity  string `json:"entity"`
	ID      string `json:"id,omitempty"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

type PortableImportCounts struct {
	ProjectsCreated     int `json:"projects_created"`
	ProjectsSkipped     int `json:"projects_skipped"`
	ColumnsCreated      int `json:"columns_created"`
	ColumnsSkipped      int `json:"columns_skipped"`
	TasksCreated        int `json:"tasks_created"`
	TasksSkipped        int `json:"tasks_skipped"`
	LabelsCreated       int `json:"labels_created"`
	LabelsSkipped       int `json:"labels_skipped"`
	TaskLabelsCreated   int `json:"task_labels_created"`
	TaskLabelsSkipped   int `json:"task_labels_skipped"`
	CommentsCreated     int `json:"comments_created"`
	CommentsSkipped     int `json:"comments_skipped"`
	DependenciesCreated int `json:"dependencies_created"`
	DependenciesSkipped int `json:"dependencies_skipped"`
	LinksCreated        int `json:"links_created"`
	LinksSkipped        int `json:"links_skipped"`
	EventsCreated       int `json:"events_created"`
	EventsSkipped       int `json:"events_skipped"`
	AgentWorkCreated    int `json:"agent_work_created"`
	AgentWorkSkipped    int `json:"agent_work_skipped"`
	HistoryCreated      int `json:"agent_work_history_created"`
	HistorySkipped      int `json:"agent_work_history_skipped"`
}

type PortableImportReport struct {
	Format   string                `json:"format"`
	Version  int                   `json:"version"`
	DryRun   bool                  `json:"dry_run"`
	Conflict string                `json:"conflict"`
	Counts   PortableImportCounts  `json:"counts"`
	Remaps   []PortableImportRemap `json:"remaps"`
	Warnings []string              `json:"warnings"`
	Errors   []PortableImportIssue `json:"errors"`
}

func (a *PortableArchive) normalize() {
	if len(a.Relationships.TaskLabels) == 0 && len(a.TaskLabels) > 0 {
		a.Relationships.TaskLabels = a.TaskLabels
	}
	if len(a.Relationships.Dependencies) == 0 && len(a.Dependencies) > 0 {
		a.Relationships.Dependencies = a.Dependencies
	}
	if len(a.Relationships.TaskLinks) == 0 && len(a.TaskLinks) > 0 {
		a.Relationships.TaskLinks = a.TaskLinks
	}
	if len(a.Activity.Events) == 0 && len(a.Events) > 0 {
		a.Activity.Events = a.Events
	}
	if len(a.Activity.AgentWork) == 0 && len(a.AgentWork) > 0 {
		a.Activity.AgentWork = a.AgentWork
	}
	if len(a.Activity.AgentWorkHistory) == 0 && len(a.AgentWorkHistory) > 0 {
		a.Activity.AgentWorkHistory = a.AgentWorkHistory
	}
	if a.Projects == nil {
		a.Projects = []PortableProject{}
	}
	if a.Columns == nil {
		a.Columns = []PortableColumn{}
	}
	if a.Tasks == nil {
		a.Tasks = []PortableTask{}
	}
	if a.Labels == nil {
		a.Labels = []PortableLabel{}
	}
	if a.Comments == nil {
		a.Comments = []PortableComment{}
	}
	if a.Actors == nil {
		a.Actors = []PortableActorReference{}
	}
	if a.Relationships.TaskLabels == nil {
		a.Relationships.TaskLabels = []PortableTaskLabel{}
	}
	if a.Relationships.Dependencies == nil {
		a.Relationships.Dependencies = []PortableDependency{}
	}
	if a.Relationships.TaskLinks == nil {
		a.Relationships.TaskLinks = []PortableTaskLink{}
	}
	if a.Activity.Events == nil {
		a.Activity.Events = []PortableEvent{}
	}
	if a.Activity.AgentWork == nil {
		a.Activity.AgentWork = []PortableAgentWork{}
	}
	if a.Activity.AgentWorkHistory == nil {
		a.Activity.AgentWorkHistory = []PortableAgentWorkHistory{}
	}
}

type portableSQL interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func portableIDsClause(column string, ids []string) (string, []any) {
	if len(ids) == 0 {
		return "", nil
	}
	pl := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		pl[i] = "?"
		args[i] = id
	}
	return " AND " + column + " IN (" + strings.Join(pl, ",") + ")", args
}

// ExportPortable returns a stable, live-data snapshot. projectIDs nil means
// every project; callers should apply their authorization ceiling before
// calling this method. Deleted tasks are intentionally not resurrected by a
// portability export; SQLite backup/restore remains the recovery mechanism.
func (s *Store) ExportPortable(ctx context.Context, projectIDs []string) (PortableArchive, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return PortableArchive{}, err
	}
	archive, err := exportPortable(ctx, tx, projectIDs)
	if err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return PortableArchive{}, errors.Join(err, fmt.Errorf("rollback portable export: %w", rollbackErr))
		}
		return PortableArchive{}, err
	}
	if err := tx.Commit(); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return PortableArchive{}, errors.Join(err, fmt.Errorf("rollback portable export: %w", rollbackErr))
		}
		return PortableArchive{}, err
	}
	return archive, nil
}

func exportPortable(ctx context.Context, q portableSQL, projectIDs []string) (PortableArchive, error) {
	archive := PortableArchive{
		Format: PortableFormat, Version: PortableVersion, ExportedAt: now(),
		Source:   PortableSource{Product: "helm", API: "/api/v1"},
		Projects: []PortableProject{}, Columns: []PortableColumn{}, Tasks: []PortableTask{},
		Labels: []PortableLabel{}, Actors: []PortableActorReference{}, Comments: []PortableComment{},
		Relationships: PortableRelationships{TaskLabels: []PortableTaskLabel{}, Dependencies: []PortableDependency{}, TaskLinks: []PortableTaskLink{}},
		Activity:      PortableActivity{Events: []PortableEvent{}, AgentWork: []PortableAgentWork{}, AgentWorkHistory: []PortableAgentWorkHistory{}},
	}
	where, args := portableIDsClause("p.id", projectIDs)
	rows, err := q.QueryContext(ctx, `SELECT p.id,p.key,p.slug,p.name,p.description,p.color,p.favorite,p.archived_at,p.created_at,p.updated_at FROM projects p WHERE 1=1`+where+` ORDER BY p.id`, args...)
	if err != nil {
		return PortableArchive{}, err
	}
	projectSet := map[string]struct{}{}
	for rows.Next() {
		var p PortableProject
		var favorite int
		var archived sql.NullString
		if err := rows.Scan(&p.ID, &p.Key, &p.Slug, &p.Name, &p.Description, &p.Color, &favorite, &archived, &p.CreatedAt, &p.UpdatedAt); err != nil {
			rows.Close()
			return PortableArchive{}, err
		}
		p.Favorite, p.ArchivedAt = boolValue(favorite), nullableString(archived)
		archive.Projects = append(archive.Projects, p)
		projectSet[p.ID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return PortableArchive{}, err
	}
	rows.Close()
	if len(projectIDs) > 0 && len(archive.Projects) != len(projectIDs) {
		return PortableArchive{}, notFound("one or more projects not found")
	}
	if len(archive.Projects) == 0 {
		return archive, nil
	}
	projectIDs = make([]string, 0, len(archive.Projects))
	for _, project := range archive.Projects {
		projectIDs = append(projectIDs, project.ID)
	}
	where, args = portableIDsClause("c.project_id", projectIDs)
	rows, err = q.QueryContext(ctx, `SELECT c.id,c.project_id,c.name,c.semantic_state,c.position,c.created_at,c.updated_at FROM columns c WHERE 1=1`+where+` ORDER BY c.project_id,c.position,c.id`, args...)
	if err != nil {
		return PortableArchive{}, err
	}
	columnSet := map[string]struct{}{}
	for rows.Next() {
		var c PortableColumn
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.Name, &c.SemanticState, &c.Position, &c.CreatedAt, &c.UpdatedAt); err != nil {
			rows.Close()
			return PortableArchive{}, err
		}
		archive.Columns = append(archive.Columns, c)
		columnSet[c.ID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return PortableArchive{}, err
	}
	rows.Close()

	where, args = portableIDsClause("t.project_id", projectIDs)
	rows, err = q.QueryContext(ctx, `SELECT t.id,t.number,t.project_id,t.kind,t.column_id,t.title,t.description,t.priority,t.position,t.assignee_id,t.claimed_by,t.claim_expires_at,t.due_at,t.version,t.completed_at,t.created_at,t.updated_at FROM tasks t WHERE t.deleted_at IS NULL`+where+` ORDER BY t.project_id,t.number,t.id`, args...)
	if err != nil {
		return PortableArchive{}, err
	}
	taskSet := map[string]struct{}{}
	actorSet := map[string]struct{}{}
	for rows.Next() {
		var task PortableTask
		var assignee, claimed, expiry, due, completed sql.NullString
		if err := rows.Scan(&task.ID, &task.Number, &task.ProjectID, &task.Kind, &task.ColumnID, &task.Title, &task.Description, &task.Priority, &task.Position, &assignee, &claimed, &expiry, &due, &task.Version, &completed, &task.CreatedAt, &task.UpdatedAt); err != nil {
			rows.Close()
			return PortableArchive{}, err
		}
		task.AssigneeID, task.ClaimedBy, task.ClaimExpiresAt, task.DueAt, task.CompletedAt = nullableString(assignee), nullableString(claimed), nullableString(expiry), nullableString(due), nullableString(completed)
		archive.Tasks = append(archive.Tasks, task)
		taskSet[task.ID] = struct{}{}
		if task.AssigneeID != nil {
			actorSet[*task.AssigneeID] = struct{}{}
		}
		if task.ClaimedBy != nil {
			actorSet[*task.ClaimedBy] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return PortableArchive{}, err
	}
	rows.Close()

	// Bug metadata is a separate one-to-one table. Keep it in the task record
	// so an archive round trip preserves the complete typed-task payload.
	where, args = portableIDsClause("t.project_id", projectIDs)
	rows, err = q.QueryContext(ctx, `SELECT bd.task_id,bd.reporter_id,bd.severity,bd.actual_behavior,bd.expected_behavior,bd.reproduction_steps,bd.environment,bd.affected_version,bd.resolution,bd.resolved_by,bd.resolved_at,bd.duplicate_of FROM bug_details bd JOIN tasks t ON t.id=bd.task_id WHERE t.deleted_at IS NULL`+where+` ORDER BY bd.task_id`, args...)
	if err != nil {
		return PortableArchive{}, err
	}
	taskIndexes := make(map[string]int, len(archive.Tasks))
	for index, task := range archive.Tasks {
		taskIndexes[task.ID] = index
	}
	for rows.Next() {
		var taskID string
		var bug PortableBug
		var severity, resolution, resolvedBy, resolvedAt, duplicateOf sql.NullString
		if err := rows.Scan(&taskID, &bug.ReporterID, &severity, &bug.ActualBehavior, &bug.ExpectedBehavior, &bug.ReproductionSteps, &bug.Environment, &bug.AffectedVersion, &resolution, &resolvedBy, &resolvedAt, &duplicateOf); err != nil {
			rows.Close()
			return PortableArchive{}, err
		}
		bug.Severity, bug.Resolution = nullableString(severity), nullableString(resolution)
		bug.ResolvedBy, bug.ResolvedAt, bug.DuplicateOf = nullableString(resolvedBy), nullableString(resolvedAt), nullableString(duplicateOf)
		if index, ok := taskIndexes[taskID]; ok {
			archive.Tasks[index].Bug = &bug
		}
		actorSet[bug.ReporterID] = struct{}{}
		if bug.ResolvedBy != nil {
			actorSet[*bug.ResolvedBy] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return PortableArchive{}, err
	}
	rows.Close()

	where, args = portableIDsClause("l.project_id", projectIDs)
	rows, err = q.QueryContext(ctx, `SELECT l.id,l.project_id,l.name,l.color,l.created_at,l.updated_at FROM labels l WHERE 1=1`+where+` ORDER BY l.project_id,lower(l.name),l.id`, args...)
	if err != nil {
		return PortableArchive{}, err
	}
	labelSet := map[string]struct{}{}
	for rows.Next() {
		var label PortableLabel
		if err := rows.Scan(&label.ID, &label.ProjectID, &label.Name, &label.Color, &label.CreatedAt, &label.UpdatedAt); err != nil {
			rows.Close()
			return PortableArchive{}, err
		}
		archive.Labels = append(archive.Labels, label)
		labelSet[label.ID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return PortableArchive{}, err
	}
	rows.Close()

	where, args = portableIDsClause("t.project_id", projectIDs)
	rows, err = q.QueryContext(ctx, `SELECT tl.task_id,tl.label_id FROM task_labels tl JOIN tasks t ON t.id=tl.task_id WHERE t.deleted_at IS NULL`+where+` ORDER BY tl.task_id,tl.label_id`, args...)
	if err != nil {
		return PortableArchive{}, err
	}
	for rows.Next() {
		var relation PortableTaskLabel
		if err := rows.Scan(&relation.TaskID, &relation.LabelID); err != nil {
			rows.Close()
			return PortableArchive{}, err
		}
		if _, ok := taskSet[relation.TaskID]; ok {
			if _, ok := labelSet[relation.LabelID]; ok {
				archive.Relationships.TaskLabels = append(archive.Relationships.TaskLabels, relation)
			}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return PortableArchive{}, err
	}
	rows.Close()

	where, args = portableIDsClause("t.project_id", projectIDs)
	rows, err = q.QueryContext(ctx, `SELECT td.task_id,td.prerequisite_task_id,td.created_by,td.created_at FROM task_dependencies td JOIN tasks t ON t.id=td.task_id WHERE t.deleted_at IS NULL`+where+` ORDER BY td.task_id,td.prerequisite_task_id`, args...)
	if err != nil {
		return PortableArchive{}, err
	}
	for rows.Next() {
		var relation PortableDependency
		var actor sql.NullString
		if err := rows.Scan(&relation.TaskID, &relation.PrerequisiteID, &actor, &relation.CreatedAt); err != nil {
			rows.Close()
			return PortableArchive{}, err
		}
		relation.CreatedBy = nullableString(actor)
		if relation.CreatedBy != nil {
			actorSet[*relation.CreatedBy] = struct{}{}
		}
		if _, ok := taskSet[relation.TaskID]; ok {
			if _, ok := taskSet[relation.PrerequisiteID]; ok {
				archive.Relationships.Dependencies = append(archive.Relationships.Dependencies, relation)
			}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return PortableArchive{}, err
	}
	rows.Close()

	where, args = portableIDsClause("source.project_id", projectIDs)
	rows, err = q.QueryContext(ctx, `SELECT link.source_task_id,link.target_task_id,link.link_type,link.created_at FROM task_links link JOIN tasks source ON source.id=link.source_task_id WHERE source.deleted_at IS NULL`+where+` ORDER BY link.source_task_id,link.target_task_id,link.link_type`, args...)
	if err != nil {
		return PortableArchive{}, err
	}
	for rows.Next() {
		var relation PortableTaskLink
		if err := rows.Scan(&relation.SourceTaskID, &relation.TargetTaskID, &relation.LinkType, &relation.CreatedAt); err != nil {
			rows.Close()
			return PortableArchive{}, err
		}
		if _, ok := taskSet[relation.SourceTaskID]; ok {
			if _, ok := taskSet[relation.TargetTaskID]; ok {
				archive.Relationships.TaskLinks = append(archive.Relationships.TaskLinks, relation)
			}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return PortableArchive{}, err
	}
	rows.Close()

	where, args = portableIDsClause("t.project_id", projectIDs)
	rows, err = q.QueryContext(ctx, `SELECT c.id,c.task_id,c.actor_id,c.body,c.created_at,c.updated_at FROM comments c JOIN tasks t ON t.id=c.task_id WHERE t.deleted_at IS NULL`+where+` ORDER BY c.task_id,c.created_at,c.id`, args...)
	if err != nil {
		return PortableArchive{}, err
	}
	for rows.Next() {
		var comment PortableComment
		if err := rows.Scan(&comment.ID, &comment.TaskID, &comment.ActorID, &comment.Body, &comment.CreatedAt, &comment.UpdatedAt); err != nil {
			rows.Close()
			return PortableArchive{}, err
		}
		archive.Comments = append(archive.Comments, comment)
		actorSet[comment.ActorID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return PortableArchive{}, err
	}
	rows.Close()

	where, args = portableIDsClause("e.project_id", projectIDs)
	rows, err = q.QueryContext(ctx, `SELECT e.cursor,e.id,e.type,e.actor_id,e.project_id,e.task_id,e.payload,e.created_at FROM events e WHERE e.project_id IS NOT NULL AND (e.task_id IS NULL OR EXISTS (SELECT 1 FROM tasks live_task WHERE live_task.id=e.task_id AND live_task.deleted_at IS NULL))`+where+` ORDER BY e.cursor`, args...)
	if err != nil {
		return PortableArchive{}, err
	}
	for rows.Next() {
		var event PortableEvent
		var actor, project, task, payload sql.NullString
		if err := rows.Scan(&event.Cursor, &event.ID, &event.Type, &actor, &project, &task, &payload, &event.CreatedAt); err != nil {
			rows.Close()
			return PortableArchive{}, err
		}
		event.ActorID, event.ProjectID, event.TaskID = nullableString(actor), nullableString(project), nullableString(task)
		if payload.Valid {
			event.Payload = json.RawMessage(payload.String)
		} else {
			event.Payload = json.RawMessage(`{}`)
		}
		archive.Activity.Events = append(archive.Activity.Events, event)
		if event.ActorID != nil {
			actorSet[*event.ActorID] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return PortableArchive{}, err
	}
	rows.Close()

	where, args = portableIDsClause("t.project_id", projectIDs)
	rows, err = q.QueryContext(ctx, `SELECT aw.task_id,aw.operation_id,aw.actor_id,aw.state,aw.phase,aw.summary,aw.next_action,aw.checkpoint_refs,aw.checkpoint_completed,aw.checkpoint_total,aw.started_at,aw.updated_at FROM task_agent_work aw JOIN tasks t ON t.id=aw.task_id WHERE t.deleted_at IS NULL`+where+` ORDER BY aw.task_id`, args...)
	if err != nil {
		return PortableArchive{}, err
	}
	for rows.Next() {
		var work PortableAgentWork
		var refs string
		var completed, total sql.NullInt64
		if err := rows.Scan(&work.TaskID, &work.OperationID, &work.ActorID, &work.State, &work.Phase, &work.Summary, &work.NextAction, &refs, &completed, &total, &work.StartedAt, &work.UpdatedAt); err != nil {
			rows.Close()
			return PortableArchive{}, err
		}
		if err := json.Unmarshal([]byte(refs), &work.CheckpointRefs); err != nil {
			rows.Close()
			return PortableArchive{}, err
		}
		if work.CheckpointRefs == nil {
			work.CheckpointRefs = []string{}
		}
		if completed.Valid {
			value := int(completed.Int64)
			work.CheckpointCompleted = &value
		}
		if total.Valid {
			value := int(total.Int64)
			work.CheckpointTotal = &value
		}
		archive.Activity.AgentWork = append(archive.Activity.AgentWork, work)
		actorSet[work.ActorID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return PortableArchive{}, err
	}
	rows.Close()

	where, args = portableIDsClause("t.project_id", projectIDs)
	rows, err = q.QueryContext(ctx, `SELECT history.id,history.task_id,history.operation_id,history.actor_id,history.state,history.phase,history.summary,history.next_action,history.checkpoint_refs,history.checkpoint_completed,history.checkpoint_total,history.started_at,history.created_at,history.generated_comment_id,history.progress_event_cursor FROM task_agent_work_history history JOIN tasks t ON t.id=history.task_id WHERE t.deleted_at IS NULL`+where+` ORDER BY history.task_id,history.created_at,history.id`, args...)
	if err != nil {
		return PortableArchive{}, err
	}
	for rows.Next() {
		var history PortableAgentWorkHistory
		var refs string
		var completed, total, eventCursor sql.NullInt64
		var generatedComment sql.NullString
		if err := rows.Scan(&history.ID, &history.TaskID, &history.OperationID, &history.ActorID, &history.State, &history.Phase, &history.Summary, &history.NextAction, &refs, &completed, &total, &history.StartedAt, &history.CreatedAt, &generatedComment, &eventCursor); err != nil {
			rows.Close()
			return PortableArchive{}, err
		}
		if err := json.Unmarshal([]byte(refs), &history.CheckpointRefs); err != nil {
			rows.Close()
			return PortableArchive{}, err
		}
		if history.CheckpointRefs == nil {
			history.CheckpointRefs = []string{}
		}
		if completed.Valid {
			value := int(completed.Int64)
			history.CheckpointCompleted = &value
		}
		if total.Valid {
			value := int(total.Int64)
			history.CheckpointTotal = &value
		}
		if generatedComment.Valid {
			history.GeneratedCommentID = &generatedComment.String
		}
		if eventCursor.Valid {
			value := eventCursor.Int64
			history.ProgressEventCursor = &value
		}
		archive.Activity.AgentWorkHistory = append(archive.Activity.AgentWorkHistory, history)
		actorSet[history.ActorID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return PortableArchive{}, err
	}
	rows.Close()

	// Include safe actor references so an import can explain missing identity
	// mappings without exporting any authentication material.
	actorIDs := make([]string, 0, len(actorSet))
	for id := range actorSet {
		actorIDs = append(actorIDs, id)
	}
	sort.Strings(actorIDs)
	for _, id := range actorIDs {
		var actor PortableActorReference
		var disabled sql.NullString
		if err := q.QueryRowContext(ctx, `SELECT id,kind,name,description,disabled_at FROM actors WHERE id=?`, id).Scan(&actor.ID, &actor.Kind, &actor.Name, &actor.Description, &disabled); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return PortableArchive{}, err
		}
		actor.DisabledAt = nullableString(disabled)
		archive.Actors = append(archive.Actors, actor)
	}
	_ = columnSet // retained for clarity: column references are validated on import.
	return archive, nil
}

func portableImportError(report PortableImportReport, message string) error {
	if len(report.Errors) == 0 {
		report.Errors = []PortableImportIssue{{Entity: "archive", Message: message}}
	}
	return &Error{Kind: ErrInvalid, Message: message, Details: report}
}

func portableConflictError(report PortableImportReport, message string) error {
	return &Error{Kind: ErrConflict, Message: message, Details: report}
}

func portableSafeID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if r <= 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func portableTimestamp(value string, optional bool) bool {
	if strings.TrimSpace(value) == "" {
		return optional
	}
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

func addPortableIssue(report *PortableImportReport, entity, id, field, message string) {
	if len(report.Errors) >= 200 {
		return
	}
	report.Errors = append(report.Errors, PortableImportIssue{Entity: entity, ID: id, Field: field, Message: message})
}

// portableAgentWorkValidationField keeps the shared runtime validator's
// precise error on the same field in an import report. The validator itself
// lives in agent_work.go so live API writes and portable writes cannot drift
// on operation IDs, text lengths, checkpoint references, or progress pairs.
func portableAgentWorkValidationField(message string) string {
	switch {
	case strings.HasPrefix(message, "operation_id"):
		return "operation_id"
	case strings.HasPrefix(message, "state"):
		return "state"
	case strings.HasPrefix(message, "phase"):
		return "phase"
	case strings.HasPrefix(message, "summary"):
		return "summary"
	case strings.HasPrefix(message, "next_action"):
		return "next_action"
	case strings.HasPrefix(message, "checkpoint_refs"):
		return "checkpoint_refs"
	case strings.HasPrefix(message, "checkpoint_completed"), strings.HasPrefix(message, "checkpoint_total"), strings.HasPrefix(message, "checkpoint progress"):
		return "checkpoint"
	default:
		return "activity"
	}
}

func portableValidateAgentWork(report *PortableImportReport, entity, id string, input AgentWorkInput) (AgentWorkInput, bool) {
	validated, err := validateAgentWorkInput(input)
	if err == nil {
		return validated, true
	}
	addPortableIssue(report, entity, id, portableAgentWorkValidationField(err.Error()), err.Error())
	return input, false
}

func validatePortableArchive(archive *PortableArchive, report *PortableImportReport) {
	archive.normalize()
	report.Format, report.Version = archive.Format, archive.Version
	if archive.Format != PortableFormat {
		addPortableIssue(report, "archive", "", "format", "format must be helm.portable")
	}
	if archive.Version != PortableVersion {
		addPortableIssue(report, "archive", "", "version", fmt.Sprintf("version %d is not supported", archive.Version))
	}
	if archive.ExportedAt != "" && !portableTimestamp(archive.ExportedAt, false) {
		addPortableIssue(report, "archive", "", "exported_at", "exported_at must be RFC3339")
	}
	if len(archive.Projects) == 0 {
		addPortableIssue(report, "archive", "", "projects", "at least one project is required")
	}
	if len(archive.Projects) > portableMaxProjects {
		addPortableIssue(report, "archive", "", "projects", "too many projects")
	}
	if len(archive.Columns) > portableMaxColumns {
		addPortableIssue(report, "archive", "", "columns", "too many columns")
	}
	if len(archive.Tasks) > portableMaxTasks {
		addPortableIssue(report, "archive", "", "tasks", "too many tasks")
	}
	if len(archive.Labels) > portableMaxLabels {
		addPortableIssue(report, "archive", "", "labels", "too many labels")
	}
	if len(archive.Comments) > portableMaxComments {
		addPortableIssue(report, "archive", "", "comments", "too many comments")
	}
	if len(archive.Relationships.TaskLabels)+len(archive.Relationships.Dependencies)+len(archive.Relationships.TaskLinks) > portableMaxRelations {
		addPortableIssue(report, "archive", "", "relationships", "too many relationships")
	}
	if len(archive.Activity.Events) > portableMaxEvents {
		addPortableIssue(report, "archive", "", "activity.events", "too many events")
	}
	if len(archive.Activity.AgentWork)+len(archive.Activity.AgentWorkHistory) > portableMaxActivity {
		addPortableIssue(report, "archive", "", "activity", "too many activity records")
	}
	projectSet, columnSet, taskSet, labelSet := map[string]PortableProject{}, map[string]PortableColumn{}, map[string]PortableTask{}, map[string]PortableLabel{}
	projectKeySet, projectSlugSet := map[string]string{}, map[string]string{}
	for _, project := range archive.Projects {
		if !portableSafeID(project.ID) {
			addPortableIssue(report, "project", project.ID, "id", "id is required and must not contain whitespace")
		}
		if _, exists := projectSet[project.ID]; exists {
			addPortableIssue(report, "project", project.ID, "id", "duplicate project id")
		}
		projectSet[project.ID] = project
		if !projectKeyPattern.MatchString(project.Key) {
			addPortableIssue(report, "project", project.ID, "key", "key is invalid")
		}
		if !projectSlugPattern.MatchString(project.Slug) {
			addPortableIssue(report, "project", project.ID, "slug", "slug is invalid")
		}
		if !colorPattern.MatchString(project.Color) {
			addPortableIssue(report, "project", project.ID, "color", "color is invalid")
		}
		if project.Name == "" || len(project.Name) > 200 {
			addPortableIssue(report, "project", project.ID, "name", "name is invalid")
		}
		if len(project.Description) > 20000 {
			addPortableIssue(report, "project", project.ID, "description", "description is too long")
		}
		projectKey := strings.ToUpper(strings.TrimSpace(project.Key))
		if previous, exists := projectKeySet[projectKey]; exists && previous != project.ID {
			addPortableIssue(report, "project", project.ID, "key", "duplicate project key in archive")
		}
		projectKeySet[projectKey] = project.ID
		projectSlug := strings.ToLower(strings.TrimSpace(project.Slug))
		if previous, exists := projectSlugSet[projectSlug]; exists && previous != project.ID {
			addPortableIssue(report, "project", project.ID, "slug", "duplicate project slug in archive")
		}
		projectSlugSet[projectSlug] = project.ID
		if !portableTimestamp(project.CreatedAt, false) || !portableTimestamp(project.UpdatedAt, false) || (project.ArchivedAt != nil && !portableTimestamp(*project.ArchivedAt, false)) {
			addPortableIssue(report, "project", project.ID, "timestamp", "timestamps must be RFC3339")
		}
	}
	// Build the complete task index before validating individual task payloads;
	// bug duplicate_of references are allowed to point forward in archive order.
	for _, task := range archive.Tasks {
		if _, exists := taskSet[task.ID]; exists {
			addPortableIssue(report, "task", task.ID, "id", "duplicate task id")
		}
		taskSet[task.ID] = task
	}
	for _, column := range archive.Columns {
		if !portableSafeID(column.ID) {
			addPortableIssue(report, "column", column.ID, "id", "id is invalid")
		}
		if _, exists := columnSet[column.ID]; exists {
			addPortableIssue(report, "column", column.ID, "id", "duplicate column id")
		}
		columnSet[column.ID] = column
		if _, ok := projectSet[column.ProjectID]; !ok {
			addPortableIssue(report, "column", column.ID, "project_id", "project does not exist in archive")
		}
		if column.Name == "" || len(column.Name) > 100 {
			addPortableIssue(report, "column", column.ID, "name", "name is invalid")
		}
		if !validState(column.SemanticState) {
			addPortableIssue(report, "column", column.ID, "semantic_state", "semantic_state is invalid")
		}
		if column.Position < 0 {
			addPortableIssue(report, "column", column.ID, "position", "position must be non-negative")
		}
		if !portableTimestamp(column.CreatedAt, false) || !portableTimestamp(column.UpdatedAt, false) {
			addPortableIssue(report, "column", column.ID, "timestamp", "timestamps must be RFC3339")
		}
	}
	for _, task := range archive.Tasks {
		if !portableSafeID(task.ID) {
			addPortableIssue(report, "task", task.ID, "id", "id is invalid")
		}
		if _, ok := projectSet[task.ProjectID]; !ok {
			addPortableIssue(report, "task", task.ID, "project_id", "project does not exist in archive")
		}
		column, ok := columnSet[task.ColumnID]
		if !ok {
			addPortableIssue(report, "task", task.ID, "column_id", "column does not exist in archive")
		} else if column.ProjectID != task.ProjectID {
			addPortableIssue(report, "task", task.ID, "column_id", "column belongs to another project")
		}
		if task.Number <= 0 {
			addPortableIssue(report, "task", task.ID, "number", "number must be positive")
		}
		if task.Kind != defaultTaskKind && task.Kind != bugKind {
			addPortableIssue(report, "task", task.ID, "kind", "kind must be task or bug")
		}
		if !validPriority(task.Priority) {
			addPortableIssue(report, "task", task.ID, "priority", "priority is invalid")
		}
		if task.AssigneeID != nil && !portableSafeID(*task.AssigneeID) {
			addPortableIssue(report, "task", task.ID, "assignee_id", "assignee id is invalid")
		}
		if task.ClaimedBy != nil && !portableSafeID(*task.ClaimedBy) {
			addPortableIssue(report, "task", task.ID, "claimed_by", "claimed_by id is invalid")
		}
		if task.Title == "" || len(task.Title) > 500 {
			addPortableIssue(report, "task", task.ID, "title", "title is invalid")
		}
		if len(task.Description) > 100000 || math.IsNaN(task.Position) || math.IsInf(task.Position, 0) || task.Position < 0 || task.Position > 1e12 {
			addPortableIssue(report, "task", task.ID, "position", "task position is invalid")
		}
		if task.Version <= 0 {
			addPortableIssue(report, "task", task.ID, "version", "version must be positive")
		}
		if !portableTimestamp(task.CreatedAt, false) || !portableTimestamp(task.UpdatedAt, false) || (task.DueAt != nil && !portableTimestamp(*task.DueAt, false)) || (task.ClaimExpiresAt != nil && !portableTimestamp(*task.ClaimExpiresAt, false)) || (task.CompletedAt != nil && !portableTimestamp(*task.CompletedAt, false)) {
			addPortableIssue(report, "task", task.ID, "timestamp", "timestamps must be RFC3339")
		}
		if task.Kind == bugKind {
			if task.Bug == nil || strings.TrimSpace(task.Bug.ActualBehavior) == "" {
				addPortableIssue(report, "task", task.ID, "bug", "bug details with actual_behavior are required")
			}
			if task.Bug != nil {
				if !portableSafeID(task.Bug.ReporterID) {
					addPortableIssue(report, "task", task.ID, "bug.reporter_id", "reporter id is invalid")
				}
				if task.Bug.Severity != nil && !validBugSeverity(*task.Bug.Severity) {
					addPortableIssue(report, "task", task.ID, "bug.severity", "severity is invalid")
				}
				if task.Bug.Resolution != nil && !validBugResolution(*task.Bug.Resolution) {
					addPortableIssue(report, "task", task.ID, "bug.resolution", "resolution is invalid")
				}
				if task.Bug.ResolvedAt != nil && !portableTimestamp(*task.Bug.ResolvedAt, false) {
					addPortableIssue(report, "task", task.ID, "bug.resolved_at", "resolved_at must be RFC3339")
				}
				if task.Bug.ResolvedBy != nil && !portableSafeID(*task.Bug.ResolvedBy) {
					addPortableIssue(report, "task", task.ID, "bug.resolved_by", "resolved_by is invalid")
				}
				if task.Bug.DuplicateOf != nil && *task.Bug.DuplicateOf == task.ID {
					addPortableIssue(report, "task", task.ID, "bug.duplicate_of", "duplicate_of cannot reference itself")
				}
				if task.Bug.DuplicateOf != nil {
					if duplicate, ok := taskSet[*task.Bug.DuplicateOf]; !ok {
						addPortableIssue(report, "task", task.ID, "bug.duplicate_of", "duplicate_of task does not exist in archive")
					} else if duplicate.ProjectID != task.ProjectID {
						addPortableIssue(report, "task", task.ID, "bug.duplicate_of", "duplicate_of must reference a task in the same project")
					}
				}
			}
		} else if task.Bug != nil {
			addPortableIssue(report, "task", task.ID, "bug", "bug details require kind bug")
		}
	}
	labelNameSet := map[string]string{}
	for _, label := range archive.Labels {
		if !portableSafeID(label.ID) {
			addPortableIssue(report, "label", label.ID, "id", "id is invalid")
		}
		if _, exists := labelSet[label.ID]; exists {
			addPortableIssue(report, "label", label.ID, "id", "duplicate label id")
		}
		labelSet[label.ID] = label
		if _, ok := projectSet[label.ProjectID]; !ok {
			addPortableIssue(report, "label", label.ID, "project_id", "project does not exist in archive")
		}
		if label.Name == "" || len(label.Name) > 100 || !colorPattern.MatchString(label.Color) {
			addPortableIssue(report, "label", label.ID, "label", "label name or color is invalid")
		}
		labelName := label.ProjectID + "\x00" + strings.ToLower(strings.TrimSpace(label.Name))
		if previous, exists := labelNameSet[labelName]; exists && previous != label.ID {
			addPortableIssue(report, "label", label.ID, "name", "duplicate label name in project")
		}
		labelNameSet[labelName] = label.ID
		if !portableTimestamp(label.CreatedAt, false) || !portableTimestamp(label.UpdatedAt, false) {
			addPortableIssue(report, "label", label.ID, "timestamp", "timestamps must be RFC3339")
		}
	}
	actorSet := map[string]struct{}{}
	for _, actor := range archive.Actors {
		if !portableSafeID(actor.ID) {
			addPortableIssue(report, "actor", actor.ID, "id", "actor id is invalid")
		}
		if _, exists := actorSet[actor.ID]; exists {
			addPortableIssue(report, "actor", actor.ID, "id", "duplicate actor id")
		}
		actorSet[actor.ID] = struct{}{}
		if actor.Kind != "human" && actor.Kind != "agent" {
			addPortableIssue(report, "actor", actor.ID, "kind", "actor kind must be human or agent")
		}
		if strings.TrimSpace(actor.Name) == "" || len(actor.Name) > 200 {
			addPortableIssue(report, "actor", actor.ID, "name", "actor name is invalid")
		}
		if len(actor.Description) > 20000 {
			addPortableIssue(report, "actor", actor.ID, "description", "actor description is too long")
		}
		if actor.DisabledAt != nil && !portableTimestamp(*actor.DisabledAt, false) {
			addPortableIssue(report, "actor", actor.ID, "disabled_at", "disabled_at must be RFC3339")
		}
	}
	relationSeen := map[string]struct{}{}
	for _, relation := range archive.Relationships.TaskLabels {
		task, taskOK := taskSet[relation.TaskID]
		if !portableSafeID(relation.TaskID) || !portableSafeID(relation.LabelID) {
			addPortableIssue(report, "task_label", relation.TaskID, "id", "relationship identifiers are invalid")
		}
		if !taskOK {
			addPortableIssue(report, "task_label", relation.TaskID, "task_id", "task does not exist in archive")
		}
		label, labelOK := labelSet[relation.LabelID]
		if !labelOK {
			addPortableIssue(report, "task_label", relation.LabelID, "label_id", "label does not exist in archive")
		} else if taskOK && task.ProjectID != label.ProjectID {
			addPortableIssue(report, "task_label", relation.TaskID, "label_id", "task labels must stay within one project")
		}
		key := "label\x00" + relation.TaskID + "\x00" + relation.LabelID
		if _, exists := relationSeen[key]; exists {
			addPortableIssue(report, "task_label", key, "id", "duplicate relationship")
		}
		relationSeen[key] = struct{}{}
	}
	graph := map[string][]string{}
	for _, relation := range archive.Relationships.Dependencies {
		dependent, dependentOK := taskSet[relation.TaskID]
		prerequisite, prerequisiteOK := taskSet[relation.PrerequisiteID]
		if !portableSafeID(relation.TaskID) || !portableSafeID(relation.PrerequisiteID) {
			addPortableIssue(report, "dependency", relation.TaskID, "task_id", "task identifiers are invalid")
		}
		if !dependentOK || !prerequisiteOK {
			addPortableIssue(report, "dependency", relation.TaskID, "task_id", "both tasks must exist in archive")
			continue
		}
		if dependent.ProjectID != prerequisite.ProjectID {
			addPortableIssue(report, "dependency", relation.TaskID, "project_id", "dependencies must stay within one project")
		}
		if relation.TaskID == relation.PrerequisiteID {
			addPortableIssue(report, "dependency", relation.TaskID, "prerequisite_task_id", "dependency cannot reference itself")
		}
		key := "dependency\x00" + relation.TaskID + "\x00" + relation.PrerequisiteID
		if _, exists := relationSeen[key]; exists {
			addPortableIssue(report, "dependency", key, "id", "duplicate relationship")
		}
		relationSeen[key] = struct{}{}
		graph[relation.TaskID] = append(graph[relation.TaskID], relation.PrerequisiteID)
		if relation.CreatedBy != nil && !portableSafeID(*relation.CreatedBy) {
			addPortableIssue(report, "dependency", key, "created_by", "created_by is invalid")
		}
		if !portableTimestamp(relation.CreatedAt, false) {
			addPortableIssue(report, "dependency", key, "created_at", "created_at must be RFC3339")
		}
	}
	linkSeen, linkTypeSeen := map[string]struct{}{}, map[string]string{}
	for _, relation := range archive.Relationships.TaskLinks {
		sourceTask, sourceOK := taskSet[relation.SourceTaskID]
		if !sourceOK {
			addPortableIssue(report, "task_link", relation.SourceTaskID, "source_task_id", "source task does not exist")
		}
		targetTask, targetOK := taskSet[relation.TargetTaskID]
		if !targetOK {
			addPortableIssue(report, "task_link", relation.TargetTaskID, "target_task_id", "target task does not exist")
		} else if sourceOK && sourceTask.ProjectID != targetTask.ProjectID {
			addPortableIssue(report, "task_link", relation.SourceTaskID, "target_task_id", "task links must stay within one project")
		}
		if relation.SourceTaskID == relation.TargetTaskID {
			addPortableIssue(report, "task_link", relation.SourceTaskID, "target_task_id", "link cannot reference itself")
		}
		if strings.TrimSpace(relation.LinkType) == "" || len(relation.LinkType) > 200 {
			addPortableIssue(report, "task_link", relation.SourceTaskID, "link_type", "link_type is invalid")
		}
		if !portableTimestamp(relation.CreatedAt, false) {
			addPortableIssue(report, "task_link", relation.SourceTaskID, "created_at", "created_at must be RFC3339")
		}
		if !portableSafeID(relation.SourceTaskID) || !portableSafeID(relation.TargetTaskID) {
			addPortableIssue(report, "task_link", relation.SourceTaskID, "task_id", "task identifiers are invalid")
		}
		key := relation.SourceTaskID + "\x00" + relation.TargetTaskID + "\x00" + relation.LinkType
		if _, exists := linkSeen[key]; exists {
			addPortableIssue(report, "task_link", key, "id", "duplicate relationship")
		}
		linkSeen[key] = struct{}{}
		typeKey := relation.SourceTaskID + "\x00" + relation.LinkType
		if previous, exists := linkTypeSeen[typeKey]; exists && previous != relation.TargetTaskID {
			addPortableIssue(report, "task_link", relation.SourceTaskID, "link_type", "link_type may target only one task per source")
		}
		linkTypeSeen[typeKey] = relation.TargetTaskID
	}
	if hasPortableCycle(graph) {
		addPortableIssue(report, "dependency", "", "relationships", "dependencies contain a cycle")
	}
	commentSet := map[string]struct{}{}
	for _, comment := range archive.Comments {
		if !portableSafeID(comment.ID) || !portableSafeID(comment.TaskID) || !portableSafeID(comment.ActorID) {
			addPortableIssue(report, "comment", comment.ID, "id", "comment identifiers are invalid")
		}
		if _, ok := taskSet[comment.TaskID]; !ok {
			addPortableIssue(report, "comment", comment.ID, "task_id", "task does not exist in archive")
		}
		if strings.TrimSpace(comment.Body) == "" || len(comment.Body) > 20000 {
			addPortableIssue(report, "comment", comment.ID, "body", "comment body is invalid")
		}
		if !portableTimestamp(comment.CreatedAt, false) || !portableTimestamp(comment.UpdatedAt, false) {
			addPortableIssue(report, "comment", comment.ID, "timestamp", "timestamps must be RFC3339")
		}
		if _, exists := commentSet[comment.ID]; exists {
			addPortableIssue(report, "comment", comment.ID, "id", "duplicate comment id")
		}
		commentSet[comment.ID] = struct{}{}
	}
	eventSet, eventCursorSet := map[string]struct{}{}, map[int64]struct{}{}
	for _, event := range archive.Activity.Events {
		if !portableSafeID(event.ID) || strings.TrimSpace(event.Type) == "" || len(event.Type) > 200 {
			addPortableIssue(report, "event", event.ID, "id", "event id or type is invalid")
		}
		if _, exists := eventSet[event.ID]; exists {
			addPortableIssue(report, "event", event.ID, "id", "duplicate event id")
		}
		eventSet[event.ID] = struct{}{}
		if event.Cursor < 0 {
			addPortableIssue(report, "event", event.ID, "cursor", "cursor must be non-negative")
		}
		if event.Cursor > 0 {
			if _, exists := eventCursorSet[event.Cursor]; exists {
				addPortableIssue(report, "event", event.ID, "cursor", "duplicate event cursor")
			}
			eventCursorSet[event.Cursor] = struct{}{}
		}
		if event.ActorID != nil && !portableSafeID(*event.ActorID) {
			addPortableIssue(report, "event", event.ID, "actor_id", "actor id is invalid")
		}
		if event.ProjectID != nil {
			if _, ok := projectSet[*event.ProjectID]; !ok {
				addPortableIssue(report, "event", event.ID, "project_id", "project does not exist in archive")
			}
		}
		if event.TaskID != nil {
			task, ok := taskSet[*event.TaskID]
			if !ok {
				addPortableIssue(report, "event", event.ID, "task_id", "task does not exist in archive")
			} else if event.ProjectID != nil && *event.ProjectID != task.ProjectID {
				addPortableIssue(report, "event", event.ID, "project_id", "event project must match event task project")
			}
		}
		if len(event.Payload) == 0 || !json.Valid(event.Payload) {
			addPortableIssue(report, "event", event.ID, "payload", "payload must be valid JSON")
		}
		if !portableTimestamp(event.CreatedAt, false) {
			addPortableIssue(report, "event", event.ID, "created_at", "created_at must be RFC3339")
		}
	}
	workTaskSet := map[string]struct{}{}
	for index := range archive.Activity.AgentWork {
		work := &archive.Activity.AgentWork[index]
		if _, exists := workTaskSet[work.TaskID]; exists {
			addPortableIssue(report, "agent_work", work.TaskID, "task_id", "duplicate live work snapshot")
		}
		workTaskSet[work.TaskID] = struct{}{}
		if _, ok := taskSet[work.TaskID]; !ok {
			addPortableIssue(report, "agent_work", work.TaskID, "task_id", "task does not exist in archive")
		}
		if !portableSafeID(work.TaskID) || !portableSafeID(work.ActorID) {
			addPortableIssue(report, "agent_work", work.TaskID, "id", "agent work identifiers are invalid")
		}
		validated, valid := portableValidateAgentWork(report, "agent_work", work.TaskID, AgentWorkInput{OperationID: work.OperationID, State: work.State, Phase: work.Phase, Summary: work.Summary, NextAction: work.NextAction, CheckpointRefs: work.CheckpointRefs, CheckpointCompleted: work.CheckpointCompleted, CheckpointTotal: work.CheckpointTotal})
		if valid {
			work.OperationID, work.State, work.Phase, work.Summary, work.NextAction, work.CheckpointRefs = validated.OperationID, validated.State, validated.Phase, validated.Summary, validated.NextAction, validated.CheckpointRefs
			work.CheckpointCompleted, work.CheckpointTotal = validated.CheckpointCompleted, validated.CheckpointTotal
		}
		if !portableTimestamp(work.StartedAt, false) || !portableTimestamp(work.UpdatedAt, false) {
			addPortableIssue(report, "agent_work", work.TaskID, "timestamp", "timestamps must be RFC3339")
		}
	}
	historySet := map[string]struct{}{}
	for index := range archive.Activity.AgentWorkHistory {
		history := &archive.Activity.AgentWorkHistory[index]
		if _, exists := historySet[history.ID]; exists {
			addPortableIssue(report, "agent_work_history", history.ID, "id", "duplicate history id")
		}
		historySet[history.ID] = struct{}{}
		if _, ok := taskSet[history.TaskID]; !ok {
			addPortableIssue(report, "agent_work_history", history.ID, "task_id", "task does not exist in archive")
		}
		if !portableSafeID(history.ID) || !portableSafeID(history.TaskID) || !portableSafeID(history.ActorID) {
			addPortableIssue(report, "agent_work_history", history.ID, "id", "history identifiers are invalid")
		}
		if !portableTimestamp(history.StartedAt, false) || !portableTimestamp(history.CreatedAt, false) {
			addPortableIssue(report, "agent_work_history", history.ID, "timestamp", "timestamps must be RFC3339")
		}
		validated, valid := portableValidateAgentWork(report, "agent_work_history", history.ID, AgentWorkInput{OperationID: history.OperationID, State: history.State, Phase: history.Phase, Summary: history.Summary, NextAction: history.NextAction, CheckpointRefs: history.CheckpointRefs, CheckpointCompleted: history.CheckpointCompleted, CheckpointTotal: history.CheckpointTotal})
		if valid {
			history.OperationID, history.State, history.Phase, history.Summary, history.NextAction, history.CheckpointRefs = validated.OperationID, validated.State, validated.Phase, validated.Summary, validated.NextAction, validated.CheckpointRefs
			history.CheckpointCompleted, history.CheckpointTotal = validated.CheckpointCompleted, validated.CheckpointTotal
		}
		if history.GeneratedCommentID != nil {
			if _, exists := commentSet[*history.GeneratedCommentID]; !exists {
				addPortableIssue(report, "agent_work_history", history.ID, "generated_comment_id", "generated comment does not exist in archive")
			}
		}
	}
}

func hasPortableCycle(graph map[string][]string) bool {
	state := map[string]uint8{}
	var visit func(string) bool
	visit = func(node string) bool {
		if state[node] == 1 {
			return true
		}
		if state[node] == 2 {
			return false
		}
		state[node] = 1
		for _, next := range graph[node] {
			if visit(next) {
				return true
			}
		}
		state[node] = 2
		return false
	}
	for node := range graph {
		if visit(node) {
			return true
		}
	}
	return false
}

func portableStableID(entity, source string) string {
	sum := sha256.Sum256([]byte(PortableFormat + "\x00" + entity + "\x00" + source))
	return hex.EncodeToString(sum[:16])
}

func portableCandidateID(entity, source string, used map[string]struct{}) string {
	for attempt := 0; ; attempt++ {
		candidate := portableCandidateIDAt(entity, source, attempt)
		if _, exists := used[candidate]; !exists {
			return candidate
		}
	}
}

func portableCandidateIDAt(entity, source string, attempt int) string {
	if attempt > 0 {
		source += ":" + strconv.Itoa(attempt+1)
	}
	return portableStableID(entity, source)
}

// portableFindMatchingCandidate searches the deterministic remap sequence for
// a previously imported copy. Looking beyond the first candidate matters when
// an unrelated destination row already occupied that candidate during the
// original import: retries must find the copy that was actually inserted,
// rather than creating a new suffix each time.
func portableFindMatchingCandidate(entity, source string, inspect func(string) (occupied, matching bool, err error)) (string, bool, error) {
	for attempt := 0; ; attempt++ {
		candidate := portableCandidateIDAt(entity, source, attempt)
		if candidate == source {
			continue
		}
		occupied, matched, err := inspect(candidate)
		if err != nil {
			return "", false, err
		}
		if !occupied {
			// The original import would have selected the first free
			// candidate, so no later suffix can be its remap.
			return "", false, nil
		}
		if matched {
			return candidate, true, nil
		}
	}
}

func portableSuffix(base, suffix string, max int) string {
	base = strings.TrimSpace(base)
	if len(base)+len(suffix) > max {
		base = base[:max-len(suffix)]
	}
	base = strings.TrimRight(base, "-_")
	if base == "" {
		base = "IMPORT"
	}
	return base + suffix
}

func portableUniqueKey(base, sourceID string, used map[string]struct{}) string {
	base = strings.ToUpper(strings.TrimSpace(base))
	for attempt := 0; attempt < 1000; attempt++ {
		candidate := base
		if attempt > 0 {
			candidate = portableSuffix(base, "-"+strconv.Itoa(attempt+1), 16)
		}
		if _, exists := used[candidate]; !exists {
			return candidate
		}
	}
	hash := sha256.Sum256([]byte(sourceID))
	return portableSuffix("IMPORT", "-"+hex.EncodeToString(hash[:])[:8], 16)
}

func portableUniqueSlug(base, sourceID string, used map[string]struct{}) string {
	base = strings.ToLower(strings.TrimSpace(base))
	for attempt := 0; attempt < 1000; attempt++ {
		candidate := base
		if attempt > 0 {
			candidate = portableSuffix(base, "-"+strconv.Itoa(attempt+1), 64)
		}
		if _, exists := used[candidate]; !exists {
			return candidate
		}
	}
	hash := sha256.Sum256([]byte(sourceID))
	return portableSuffix("import", "-"+hex.EncodeToString(hash[:])[:8], 64)
}

func portableStringMap(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

type portableProjectPlan struct {
	source PortableProject
	id     string
	key    string
	slug   string
	create bool
}

type portableColumnPlan struct {
	source    PortableColumn
	id        string
	projectID string
	position  int
	create    bool
}

type portableLabelPlan struct {
	source    PortableLabel
	id        string
	projectID string
	create    bool
}

type portableTaskPlan struct {
	source         PortableTask
	id             string
	projectID      string
	columnID       string
	number         int
	assigneeID     *string
	claimedBy      *string
	claimExpiresAt *string
	create         bool
}

type portableCommentPlan struct {
	source  PortableComment
	id      string
	taskID  string
	actorID string
	create  bool
}

type portableDependencyPlan struct {
	source         PortableDependency
	taskID         string
	prerequisiteID string
	createdBy      *string
	create         bool
}

type portableDependencyNode struct {
	ID            string
	ProjectID     string
	SemanticState string
	Live          bool
	Completed     bool
	ClaimActive   bool
}

type portableLinkPlan struct {
	source       PortableTaskLink
	sourceTaskID string
	targetTaskID string
	create       bool
}

type portableEventPlan struct {
	source    PortableEvent
	id        string
	actorID   *string
	projectID *string
	taskID    *string
	create    bool
}

type portableWorkPlan struct {
	source  PortableAgentWork
	taskID  string
	actorID string
	create  bool
}

type portableHistoryPlan struct {
	source             PortableAgentWorkHistory
	id                 string
	taskID             string
	actorID            string
	generatedCommentID *string
	create             bool
}

type portableImportPlan struct {
	archive        PortableArchive
	report         PortableImportReport
	options        PortableImportOptions
	projects       []portableProjectPlan
	columns        []portableColumnPlan
	labels         []portableLabelPlan
	tasks          []portableTaskPlan
	taskLabels     []PortableTaskLabel
	dependencies   []portableDependencyPlan
	links          []portableLinkPlan
	comments       []portableCommentPlan
	events         []portableEventPlan
	work           []portableWorkPlan
	history        []portableHistoryPlan
	projectMap     map[string]string
	columnMap      map[string]string
	taskMap        map[string]string
	labelMap       map[string]string
	commentMap     map[string]string
	eventCursorMap map[int64]int64
}

func portableExistingProject(q portableSQL, id string) (PortableProject, bool, error) {
	var project PortableProject
	var favorite int
	var archived sql.NullString
	err := q.QueryRowContext(context.Background(), `SELECT id,key,slug,name,description,color,favorite,archived_at,created_at,updated_at FROM projects WHERE id=?`, id).Scan(&project.ID, &project.Key, &project.Slug, &project.Name, &project.Description, &project.Color, &favorite, &archived, &project.CreatedAt, &project.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PortableProject{}, false, nil
	}
	if err != nil {
		return PortableProject{}, false, err
	}
	project.Favorite, project.ArchivedAt = boolValue(favorite), nullableString(archived)
	return project, true, nil
}

func portableExistingColumn(q portableSQL, id string) (PortableColumn, bool, error) {
	var column PortableColumn
	err := q.QueryRowContext(context.Background(), `SELECT id,project_id,name,semantic_state,position,created_at,updated_at FROM columns WHERE id=?`, id).Scan(&column.ID, &column.ProjectID, &column.Name, &column.SemanticState, &column.Position, &column.CreatedAt, &column.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PortableColumn{}, false, nil
	}
	if err != nil {
		return PortableColumn{}, false, err
	}
	return column, true, nil
}

func portableExistingLabel(q portableSQL, id string) (PortableLabel, bool, error) {
	var label PortableLabel
	err := q.QueryRowContext(context.Background(), `SELECT id,project_id,name,color,created_at,updated_at FROM labels WHERE id=?`, id).Scan(&label.ID, &label.ProjectID, &label.Name, &label.Color, &label.CreatedAt, &label.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PortableLabel{}, false, nil
	}
	if err != nil {
		return PortableLabel{}, false, err
	}
	return label, true, nil
}

func portableExistingTask(q portableSQL, id string) (PortableTask, bool, error) {
	var task PortableTask
	var assignee, claimed, expiry, due, completed sql.NullString
	err := q.QueryRowContext(context.Background(), `SELECT id,number,project_id,kind,column_id,title,description,priority,position,assignee_id,claimed_by,claim_expires_at,due_at,version,completed_at,created_at,updated_at FROM tasks WHERE id=?`, id).Scan(&task.ID, &task.Number, &task.ProjectID, &task.Kind, &task.ColumnID, &task.Title, &task.Description, &task.Priority, &task.Position, &assignee, &claimed, &expiry, &due, &task.Version, &completed, &task.CreatedAt, &task.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PortableTask{}, false, nil
	}
	if err != nil {
		return PortableTask{}, false, err
	}
	task.AssigneeID, task.ClaimedBy, task.ClaimExpiresAt, task.DueAt, task.CompletedAt = nullableString(assignee), nullableString(claimed), nullableString(expiry), nullableString(due), nullableString(completed)
	var bug Bug
	bug, bugErr := bugFromRow(q.QueryRowContext(context.Background(), `SELECT reporter_id, severity, actual_behavior, expected_behavior, reproduction_steps, environment, affected_version, resolution, resolved_by, resolved_at, duplicate_of FROM bug_details WHERE task_id=?`, id))
	if bugErr == nil {
		task.Bug = &PortableBug{ReporterID: bug.ReporterID, Severity: bug.Severity, ActualBehavior: bug.ActualBehavior, ExpectedBehavior: bug.ExpectedBehavior, ReproductionSteps: bug.ReproductionSteps, Environment: bug.Environment, AffectedVersion: bug.AffectedVersion, Resolution: bug.Resolution, ResolvedBy: bug.ResolvedBy, ResolvedAt: bug.ResolvedAt, DuplicateOf: bug.DuplicateOf}
	} else if !errors.Is(bugErr, sql.ErrNoRows) {
		return PortableTask{}, false, bugErr
	}
	return task, true, nil
}

func portableExistingComment(q portableSQL, id string) (PortableComment, bool, error) {
	var comment PortableComment
	err := q.QueryRowContext(context.Background(), `SELECT id,task_id,actor_id,body,created_at,updated_at FROM comments WHERE id=?`, id).Scan(&comment.ID, &comment.TaskID, &comment.ActorID, &comment.Body, &comment.CreatedAt, &comment.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PortableComment{}, false, nil
	}
	if err != nil {
		return PortableComment{}, false, err
	}
	return comment, true, nil
}

func portableIDSet(q portableSQL, table string) (map[string]struct{}, error) {
	// table is selected only from fixed internal call sites below; never pass
	// user input here.
	rows, err := q.QueryContext(context.Background(), `SELECT id FROM `+table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]struct{}{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result[id] = struct{}{}
	}
	return result, rows.Err()
}

func portableProjectFieldsEqual(a PortableProject, b PortableProject) bool {
	return a.Key == b.Key && a.Slug == b.Slug && a.Name == b.Name && a.Description == b.Description && a.Color == b.Color && a.Favorite == b.Favorite && stringPointerValue(a.ArchivedAt) == stringPointerValue(b.ArchivedAt) && a.CreatedAt == b.CreatedAt && a.UpdatedAt == b.UpdatedAt
}

func portableProjectMetadataEqual(a PortableProject, b PortableProject) bool {
	return a.Name == b.Name && a.Description == b.Description && a.Color == b.Color && a.Favorite == b.Favorite && stringPointerValue(a.ArchivedAt) == stringPointerValue(b.ArchivedAt) && a.CreatedAt == b.CreatedAt && a.UpdatedAt == b.UpdatedAt
}

func stringPointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func portableColumnFieldsEqual(a PortableColumn, b PortableColumn) bool {
	return a.ProjectID == b.ProjectID && a.Name == b.Name && a.SemanticState == b.SemanticState && a.Position == b.Position && a.CreatedAt == b.CreatedAt && a.UpdatedAt == b.UpdatedAt
}

func portableColumnFieldsEqualIgnoringPosition(a PortableColumn, b PortableColumn) bool {
	a.Position, b.Position = 0, 0
	return portableColumnFieldsEqual(a, b)
}

func portableLabelFieldsEqual(a PortableLabel, b PortableLabel) bool {
	return a.ProjectID == b.ProjectID && strings.EqualFold(a.Name, b.Name) && a.Color == b.Color && a.CreatedAt == b.CreatedAt && a.UpdatedAt == b.UpdatedAt
}

func portableTaskFieldsEqual(a, b PortableTask) bool {
	return a.Number == b.Number && a.ProjectID == b.ProjectID && a.Kind == b.Kind && a.ColumnID == b.ColumnID && a.Title == b.Title && a.Description == b.Description && a.Priority == b.Priority && a.Position == b.Position && stringPointerValue(a.AssigneeID) == stringPointerValue(b.AssigneeID) && stringPointerValue(a.ClaimedBy) == stringPointerValue(b.ClaimedBy) && stringPointerValue(a.ClaimExpiresAt) == stringPointerValue(b.ClaimExpiresAt) && stringPointerValue(a.DueAt) == stringPointerValue(b.DueAt) && a.Version == b.Version && stringPointerValue(a.CompletedAt) == stringPointerValue(b.CompletedAt) && a.CreatedAt == b.CreatedAt && a.UpdatedAt == b.UpdatedAt && portableBugFieldsEqual(a.Bug, b.Bug)
}

func portableTaskFieldsEqualIgnoringNumber(a, b PortableTask) bool {
	a.Number, b.Number = 0, 0
	return portableTaskFieldsEqual(a, b)
}

func portableBugFieldsEqual(a, b *PortableBug) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.ReporterID == b.ReporterID && stringPointerValue(a.Severity) == stringPointerValue(b.Severity) && a.ActualBehavior == b.ActualBehavior && a.ExpectedBehavior == b.ExpectedBehavior && a.ReproductionSteps == b.ReproductionSteps && a.Environment == b.Environment && a.AffectedVersion == b.AffectedVersion && stringPointerValue(a.Resolution) == stringPointerValue(b.Resolution) && stringPointerValue(a.ResolvedBy) == stringPointerValue(b.ResolvedBy) && stringPointerValue(a.ResolvedAt) == stringPointerValue(b.ResolvedAt) && stringPointerValue(a.DuplicateOf) == stringPointerValue(b.DuplicateOf)
}

func portableCommentFieldsEqual(a, b PortableComment) bool {
	return a.TaskID == b.TaskID && a.ActorID == b.ActorID && a.Body == b.Body && a.CreatedAt == b.CreatedAt && a.UpdatedAt == b.UpdatedAt
}

func portableAddRemap(report *PortableImportReport, entity, source, target, field, reason string) {
	if source == target && field == "" {
		return
	}
	report.Remaps = append(report.Remaps, PortableImportRemap{Entity: entity, Source: source, Target: target, Field: field, Reason: reason})
}

func portableMapActor(q portableSQL, source, importer string, report *PortableImportReport, cache map[string]string) (string, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return "", nil
	}
	if target, ok := cache[source]; ok {
		return target, nil
	}
	var exists int
	if err := q.QueryRowContext(context.Background(), `SELECT EXISTS(SELECT 1 FROM actors WHERE id=?)`, source).Scan(&exists); err != nil {
		return "", err
	}
	if exists != 0 {
		cache[source] = source
		return source, nil
	}
	cache[source] = importer
	portableAddRemap(report, "actor", source, importer, "id", "actor is not present on the destination; attributed to the importing actor")
	return importer, nil
}

func portableMapOptionalActor(q portableSQL, source *string, importer string, report *PortableImportReport, cache map[string]string) (*string, error) {
	if source == nil {
		return nil, nil
	}
	target, err := portableMapActor(q, *source, importer, report, cache)
	if err != nil {
		return nil, err
	}
	return &target, nil
}

func portableExistingNamedLabel(q portableSQL, projectID, name string) (PortableLabel, bool, error) {
	var label PortableLabel
	err := q.QueryRowContext(context.Background(), `SELECT id,project_id,name,color,created_at,updated_at FROM labels WHERE project_id=? AND lower(name)=lower(?) LIMIT 1`, projectID, name).Scan(&label.ID, &label.ProjectID, &label.Name, &label.Color, &label.CreatedAt, &label.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PortableLabel{}, false, nil
	}
	if err != nil {
		return PortableLabel{}, false, err
	}
	return label, true, nil
}

func portableExistingColumnPosition(q portableSQL, projectID string, position int) (bool, error) {
	var exists int
	err := q.QueryRowContext(context.Background(), `SELECT EXISTS(SELECT 1 FROM columns WHERE project_id=? AND position=?)`, projectID, position).Scan(&exists)
	return exists != 0, err
}

func portableExistingTaskNumber(q portableSQL, projectID string, number int) (bool, error) {
	var exists int
	err := q.QueryRowContext(context.Background(), `SELECT EXISTS(SELECT 1 FROM tasks WHERE project_id=? AND number=?)`, projectID, number).Scan(&exists)
	return exists != 0, err
}

func portableNextColumnPosition(used map[int]struct{}) int {
	position := 0
	for {
		if _, exists := used[position]; !exists {
			return position
		}
		position++
	}
}

func portableNextTaskNumber(used map[int]struct{}) int {
	number := 1
	for {
		if _, exists := used[number]; !exists {
			return number
		}
		number++
	}
}

func portableTaskLabelExists(q portableSQL, taskID, labelID string) (bool, error) {
	var exists int
	err := q.QueryRowContext(context.Background(), `SELECT EXISTS(SELECT 1 FROM task_labels WHERE task_id=? AND label_id=?)`, taskID, labelID).Scan(&exists)
	return exists != 0, err
}

func portableDependencyExists(q portableSQL, taskID, prerequisiteID string) (bool, error) {
	var exists int
	err := q.QueryRowContext(context.Background(), `SELECT EXISTS(SELECT 1 FROM task_dependencies WHERE task_id=? AND prerequisite_task_id=?)`, taskID, prerequisiteID).Scan(&exists)
	return exists != 0, err
}

func portableExistingDependencyNode(ctx context.Context, q portableSQL, taskID string) (portableDependencyNode, bool, error) {
	var node portableDependencyNode
	var live, completed, claimActive int
	err := q.QueryRowContext(ctx, `SELECT t.id,t.project_id,t.deleted_at IS NULL,t.completed_at IS NOT NULL,COALESCE(c.semantic_state,''),CASE WHEN t.claimed_by IS NOT NULL AND t.claim_expires_at IS NOT NULL AND julianday(t.claim_expires_at)>julianday('now') THEN 1 ELSE 0 END FROM tasks t LEFT JOIN columns c ON c.id=t.column_id WHERE t.id=?`, taskID).Scan(&node.ID, &node.ProjectID, &live, &completed, &node.SemanticState, &claimActive)
	if errors.Is(err, sql.ErrNoRows) {
		return portableDependencyNode{}, false, nil
	}
	if err != nil {
		return portableDependencyNode{}, false, err
	}
	node.Live, node.Completed, node.ClaimActive = live != 0, completed != 0, claimActive != 0
	return node, true, nil
}

func portablePlannedDependencyNode(task portableTaskPlan, columnStates map[string]string) portableDependencyNode {
	claimActive := false
	if task.claimedBy != nil && task.claimExpiresAt != nil {
		if expires, err := time.Parse(time.RFC3339Nano, *task.claimExpiresAt); err == nil {
			claimActive = expires.After(time.Now().UTC())
		}
	}
	return portableDependencyNode{ID: task.id, ProjectID: task.projectID, SemanticState: columnStates[task.columnID], Live: true, Completed: task.source.CompletedAt != nil, ClaimActive: claimActive}
}

func portableDependencyGraphHasPath(graph map[string]map[string]struct{}, start, target string) bool {
	if start == target {
		return true
	}
	visited := map[string]struct{}{}
	stack := []string{start}
	for len(stack) > 0 {
		last := len(stack) - 1
		node := stack[last]
		stack = stack[:last]
		if _, seen := visited[node]; seen {
			continue
		}
		visited[node] = struct{}{}
		for next := range graph[node] {
			if next == target {
				return true
			}
			if _, seen := visited[next]; !seen {
				stack = append(stack, next)
			}
		}
	}
	return false
}

func portableLinkExists(q portableSQL, source, target, linkType string) (bool, error) {
	var exists int
	err := q.QueryRowContext(context.Background(), `SELECT EXISTS(SELECT 1 FROM task_links WHERE source_task_id=? AND target_task_id=? AND link_type=?)`, source, target, linkType).Scan(&exists)
	return exists != 0, err
}

// portableExistingLinkTarget checks the actual uniqueness boundary enforced by
// task_links_source_type_idx. Checking the full source/target/type triple is
// insufficient: a different target with the same source and type still makes
// the INSERT fail, so imports must surface that conflict during preflight.
func portableExistingLinkTarget(q portableSQL, source, linkType string) (string, bool, error) {
	var target string
	err := q.QueryRowContext(context.Background(), `SELECT target_task_id FROM task_links WHERE source_task_id=? AND link_type=?`, source, linkType).Scan(&target)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return target, true, nil
}

func portableEventExists(q portableSQL, id string) (bool, error) {
	var exists int
	err := q.QueryRowContext(context.Background(), `SELECT EXISTS(SELECT 1 FROM events WHERE id=?)`, id).Scan(&exists)
	return exists != 0, err
}

func portableExistingEvent(q portableSQL, id string) (PortableEvent, bool, error) {
	var event PortableEvent
	var actor, project, task, payload sql.NullString
	err := q.QueryRowContext(context.Background(), `SELECT cursor,id,type,actor_id,project_id,task_id,payload,created_at FROM events WHERE id=?`, id).Scan(&event.Cursor, &event.ID, &event.Type, &actor, &project, &task, &payload, &event.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PortableEvent{}, false, nil
	}
	if err != nil {
		return PortableEvent{}, false, err
	}
	event.ActorID, event.ProjectID, event.TaskID = nullableString(actor), nullableString(project), nullableString(task)
	if payload.Valid {
		event.Payload = json.RawMessage(payload.String)
	} else {
		event.Payload = json.RawMessage(`{}`)
	}
	return event, true, nil
}

func portableEventFieldsEqual(a, b PortableEvent) bool {
	return a.ID == b.ID && a.Type == b.Type && stringPointerValue(a.ActorID) == stringPointerValue(b.ActorID) && stringPointerValue(a.ProjectID) == stringPointerValue(b.ProjectID) && stringPointerValue(a.TaskID) == stringPointerValue(b.TaskID) && string(a.Payload) == string(b.Payload) && a.CreatedAt == b.CreatedAt
}

func portableEventFieldsEqualIgnoringID(a, b PortableEvent) bool {
	a.ID, b.ID = "", ""
	return portableEventFieldsEqual(a, b)
}

func portableWorkExists(q portableSQL, taskID string) (bool, error) {
	var exists int
	err := q.QueryRowContext(context.Background(), `SELECT EXISTS(SELECT 1 FROM task_agent_work WHERE task_id=?)`, taskID).Scan(&exists)
	return exists != 0, err
}

func portableExistingWork(q portableSQL, taskID string) (PortableAgentWork, bool, error) {
	var work PortableAgentWork
	var refs string
	var completed, total sql.NullInt64
	err := q.QueryRowContext(context.Background(), `SELECT task_id,operation_id,actor_id,state,phase,summary,next_action,checkpoint_refs,checkpoint_completed,checkpoint_total,started_at,updated_at FROM task_agent_work WHERE task_id=?`, taskID).Scan(&work.TaskID, &work.OperationID, &work.ActorID, &work.State, &work.Phase, &work.Summary, &work.NextAction, &refs, &completed, &total, &work.StartedAt, &work.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PortableAgentWork{}, false, nil
	}
	if err != nil {
		return PortableAgentWork{}, false, err
	}
	if err := json.Unmarshal([]byte(refs), &work.CheckpointRefs); err != nil {
		return PortableAgentWork{}, false, err
	}
	if work.CheckpointRefs == nil {
		work.CheckpointRefs = []string{}
	}
	if completed.Valid {
		value := int(completed.Int64)
		work.CheckpointCompleted = &value
	}
	if total.Valid {
		value := int(total.Int64)
		work.CheckpointTotal = &value
	}
	return work, true, nil
}

func portableWorkFieldsEqual(a, b PortableAgentWork) bool {
	return a.TaskID == b.TaskID && a.OperationID == b.OperationID && a.ActorID == b.ActorID && a.State == b.State && a.Phase == b.Phase && a.Summary == b.Summary && a.NextAction == b.NextAction && strings.Join(a.CheckpointRefs, "\x00") == strings.Join(b.CheckpointRefs, "\x00") && intPointerValue(a.CheckpointCompleted) == intPointerValue(b.CheckpointCompleted) && intPointerValue(a.CheckpointTotal) == intPointerValue(b.CheckpointTotal) && a.StartedAt == b.StartedAt && a.UpdatedAt == b.UpdatedAt
}

func portableHistoryExists(q portableSQL, id string) (bool, error) {
	var exists int
	err := q.QueryRowContext(context.Background(), `SELECT EXISTS(SELECT 1 FROM task_agent_work_history WHERE id=?)`, id).Scan(&exists)
	return exists != 0, err
}

func portableExistingHistory(q portableSQL, id string) (PortableAgentWorkHistory, bool, error) {
	var history PortableAgentWorkHistory
	var refs string
	var completed, total sql.NullInt64
	var generatedComment sql.NullString
	var progress sql.NullInt64
	err := q.QueryRowContext(context.Background(), `SELECT id,task_id,operation_id,actor_id,state,phase,summary,next_action,checkpoint_refs,checkpoint_completed,checkpoint_total,started_at,created_at,generated_comment_id,progress_event_cursor FROM task_agent_work_history WHERE id=?`, id).Scan(&history.ID, &history.TaskID, &history.OperationID, &history.ActorID, &history.State, &history.Phase, &history.Summary, &history.NextAction, &refs, &completed, &total, &history.StartedAt, &history.CreatedAt, &generatedComment, &progress)
	if errors.Is(err, sql.ErrNoRows) {
		return PortableAgentWorkHistory{}, false, nil
	}
	if err != nil {
		return PortableAgentWorkHistory{}, false, err
	}
	if err := json.Unmarshal([]byte(refs), &history.CheckpointRefs); err != nil {
		return PortableAgentWorkHistory{}, false, err
	}
	if completed.Valid {
		value := int(completed.Int64)
		history.CheckpointCompleted = &value
	}
	if total.Valid {
		value := int(total.Int64)
		history.CheckpointTotal = &value
	}
	if generatedComment.Valid {
		history.GeneratedCommentID = &generatedComment.String
	}
	if progress.Valid {
		value := progress.Int64
		history.ProgressEventCursor = &value
	}
	return history, true, nil
}

func portableHistoryFieldsEqual(a, b PortableAgentWorkHistory) bool {
	return a.ID == b.ID && a.TaskID == b.TaskID && a.OperationID == b.OperationID && a.ActorID == b.ActorID && a.State == b.State && a.Phase == b.Phase && a.Summary == b.Summary && a.NextAction == b.NextAction && strings.Join(a.CheckpointRefs, "\x00") == strings.Join(b.CheckpointRefs, "\x00") && intPointerValue(a.CheckpointCompleted) == intPointerValue(b.CheckpointCompleted) && intPointerValue(a.CheckpointTotal) == intPointerValue(b.CheckpointTotal) && a.StartedAt == b.StartedAt && a.CreatedAt == b.CreatedAt && stringPointerValue(a.GeneratedCommentID) == stringPointerValue(b.GeneratedCommentID) && int64PointerValue(a.ProgressEventCursor) == int64PointerValue(b.ProgressEventCursor)
}

func portableHistoryFieldsEqualIgnoringID(a, b PortableAgentWorkHistory) bool {
	a.ID, b.ID = "", ""
	return portableHistoryFieldsEqual(a, b)
}

// portableValidateActivityPlan mirrors the foreign-key and CHECK boundaries
// of the activity tables before the write transaction reaches execution. The
// task/comment rows may themselves be part of this import and therefore are
// considered planned targets. This keeps a dry-run report and a real import
// on the same validation path instead of discovering an agent-work-history
// failure halfway through an otherwise valid archive.
func portableValidateActivityPlan(ctx context.Context, q portableSQL, plan *portableImportPlan) error {
	plannedTasks := map[string]struct{}{}
	for _, task := range plan.tasks {
		if task.create {
			plannedTasks[task.id] = struct{}{}
		}
	}
	plannedComments := map[string]struct{}{}
	for _, comment := range plan.comments {
		if comment.create {
			plannedComments[comment.id] = struct{}{}
		}
	}
	plannedEventCursors := map[int64]struct{}{}
	for _, event := range plan.events {
		if event.create && event.source.Cursor > 0 {
			plannedEventCursors[event.source.Cursor] = struct{}{}
		}
	}
	existsID := func(table, column, value string) (bool, error) {
		// table and column are selected from the fixed call sites below; neither
		// receives archive/user input.
		var exists int
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM `+table+` WHERE `+column+`=?)`, value).Scan(&exists); err != nil {
			return false, err
		}
		return exists != 0, nil
	}
	validTask := func(taskID string) (bool, error) {
		if _, planned := plannedTasks[taskID]; planned {
			return true, nil
		}
		return existsID("tasks", "id", taskID)
	}
	validActor := func(actorID string) (bool, error) {
		return existsID("actors", "id", actorID)
	}
	for _, work := range plan.work {
		if !work.create {
			continue
		}
		if valid, err := validTask(work.taskID); err != nil {
			return err
		} else if !valid {
			addPortableIssue(&plan.report, "agent_work", work.source.TaskID, "task_id", "task target is not available for the activity snapshot")
		}
		if valid, err := validActor(work.actorID); err != nil {
			return err
		} else if !valid {
			addPortableIssue(&plan.report, "agent_work", work.source.TaskID, "actor_id", "actor target is not available for the activity snapshot")
		}
		if _, err := portableMarshalRefs(work.source.CheckpointRefs); err != nil {
			addPortableIssue(&plan.report, "agent_work", work.source.TaskID, "checkpoint_refs", "checkpoint_refs cannot be encoded as a JSON array")
		}
	}
	for _, history := range plan.history {
		if !history.create {
			continue
		}
		if valid, err := validTask(history.taskID); err != nil {
			return err
		} else if !valid {
			addPortableIssue(&plan.report, "agent_work_history", history.source.ID, "task_id", "task target is not available for the activity history row")
		}
		if valid, err := validActor(history.actorID); err != nil {
			return err
		} else if !valid {
			addPortableIssue(&plan.report, "agent_work_history", history.source.ID, "actor_id", "actor target is not available for the activity history row")
		}
		if _, err := portableMarshalRefs(history.source.CheckpointRefs); err != nil {
			addPortableIssue(&plan.report, "agent_work_history", history.source.ID, "checkpoint_refs", "checkpoint_refs cannot be encoded as a JSON array")
		}
		if history.generatedCommentID != nil {
			if _, planned := plannedComments[*history.generatedCommentID]; !planned {
				valid, err := existsID("comments", "id", *history.generatedCommentID)
				if err != nil {
					return err
				}
				if !valid {
					addPortableIssue(&plan.report, "agent_work_history", history.source.ID, "generated_comment_id", "generated comment target is not available for the activity history row")
				}
			}
		}
		if history.source.ProgressEventCursor != nil {
			cursor := *history.source.ProgressEventCursor
			if _, planned := plannedEventCursors[cursor]; !planned {
				if _, mapped := plan.eventCursorMap[cursor]; !mapped {
					// Execution deliberately clears an event cursor that was not
					// included in the archive. It is not a foreign-key violation;
					// the write path will report the explicit remap/warning.
					continue
				}
			}
		}
	}
	return nil
}

func intPointerValue(value *int) int {
	if value == nil {
		return -1
	}
	return *value
}

func int64PointerValue(value *int64) int64 {
	if value == nil {
		return -1
	}
	return *value
}

// buildPortableImportPlan performs every structural and destination conflict
// check through q.  It is called inside the eventual write transaction, so a
// concurrent writer cannot invalidate a successful validation between the
// final read and the first insert.
func buildPortableImportPlan(ctx context.Context, q portableSQL, archive PortableArchive, options PortableImportOptions) (portableImportPlan, error) {
	archive.normalize()
	report := PortableImportReport{Format: archive.Format, Version: archive.Version, DryRun: options.DryRun, Conflict: options.Conflict, Remaps: []PortableImportRemap{}, Warnings: []string{}, Errors: []PortableImportIssue{}}
	if options.Conflict == "" {
		options.Conflict = portableConflictRemap
		report.Conflict = options.Conflict
	}
	if options.Conflict != portableConflictRemap && options.Conflict != portableConflictFail {
		addPortableIssue(&report, "import", "", "conflict", "conflict must be remap or fail")
	}
	validatePortableArchive(&archive, &report)
	if len(archive.Projects) > 1 && strings.TrimSpace(options.TargetProjectID) != "" {
		addPortableIssue(&report, "import", options.TargetProjectID, "target_project", "target_project can only be used with a one-project archive")
	}
	if strings.TrimSpace(options.ActorID) == "" {
		addPortableIssue(&report, "import", "", "actor", "an importing actor is required")
	} else {
		var exists int
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM actors WHERE id=?)`, options.ActorID).Scan(&exists); err != nil {
			return portableImportPlan{}, err
		}
		if exists == 0 {
			addPortableIssue(&report, "import", options.ActorID, "actor", "importing actor was not found")
		}
	}
	if len(report.Errors) > 0 {
		return portableImportPlan{archive: archive, report: report, options: options}, portableImportError(report, "portable archive validation failed")
	}

	plan := portableImportPlan{
		archive: archive, report: report, options: options,
		projects: []portableProjectPlan{}, columns: []portableColumnPlan{}, labels: []portableLabelPlan{}, tasks: []portableTaskPlan{},
		taskLabels: []PortableTaskLabel{}, dependencies: []portableDependencyPlan{}, links: []portableLinkPlan{}, comments: []portableCommentPlan{}, events: []portableEventPlan{}, work: []portableWorkPlan{}, history: []portableHistoryPlan{},
		projectMap: map[string]string{}, columnMap: map[string]string{}, taskMap: map[string]string{}, labelMap: map[string]string{}, commentMap: map[string]string{}, eventCursorMap: map[int64]int64{},
	}
	usedProjectIDs, err := portableIDSet(q, "projects")
	if err != nil {
		return portableImportPlan{}, err
	}
	usedColumnIDs, err := portableIDSet(q, "columns")
	if err != nil {
		return portableImportPlan{}, err
	}
	usedLabelIDs, err := portableIDSet(q, "labels")
	if err != nil {
		return portableImportPlan{}, err
	}
	usedTaskIDs, err := portableIDSet(q, "tasks")
	if err != nil {
		return portableImportPlan{}, err
	}
	usedCommentIDs, err := portableIDSet(q, "comments")
	if err != nil {
		return portableImportPlan{}, err
	}
	usedEventIDs, err := portableIDSet(q, "events")
	if err != nil {
		return portableImportPlan{}, err
	}
	usedHistoryIDs, err := portableIDSet(q, "task_agent_work_history")
	if err != nil {
		return portableImportPlan{}, err
	}
	usedProjectKeys, err := portableIDSet(q, "projects")
	if err != nil {
		return portableImportPlan{}, err
	}
	usedProjectSlugs := map[string]struct{}{}
	rows, err := q.QueryContext(ctx, `SELECT lower(slug) FROM projects`)
	if err != nil {
		return portableImportPlan{}, err
	}
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			rows.Close()
			return portableImportPlan{}, err
		}
		usedProjectSlugs[slug] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return portableImportPlan{}, err
	}
	rows.Close()
	// The project key set is case-normalized because the API treats keys as
	// uppercase identifiers while SQLite's UNIQUE constraint is byte-sensitive.
	rows, err = q.QueryContext(ctx, `SELECT upper(key) FROM projects`)
	if err != nil {
		return portableImportPlan{}, err
	}
	usedProjectKeys = map[string]struct{}{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return portableImportPlan{}, err
		}
		usedProjectKeys[key] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return portableImportPlan{}, err
	}
	rows.Close()

	// Project IDs are resolved first because every downstream relation is
	// project-scoped.  Existing exact matches are reused only when their
	// immutable export fields agree; no project is ever overwritten.
	for _, source := range archive.Projects {
		originalKey, originalSlug := source.Key, source.Slug
		targetID := source.ID
		create := true
		if options.TargetProjectID != "" {
			targetID = options.TargetProjectID
			create = false
			if targetID != source.ID {
				portableAddRemap(&plan.report, "project", source.ID, targetID, "id", "import target project selected by caller")
			}
			plan.report.Counts.ProjectsSkipped++
			var existing PortableProject
			var exists bool
			existing, exists, err = portableExistingProject(q, targetID)
			if err != nil {
				return portableImportPlan{}, err
			}
			if !exists {
				addPortableIssue(&plan.report, "import", targetID, "target_project", "target project was not found")
			}
			if exists && !portableProjectMetadataEqual(existing, source) {
				plan.report.Warnings = append(plan.report.Warnings, "target project metadata was retained; import never overwrites an existing project")
			}
		} else {
			existing, exists, lookupErr := portableExistingProject(q, source.ID)
			if lookupErr != nil {
				return portableImportPlan{}, lookupErr
			}
			if exists && portableProjectFieldsEqual(existing, source) {
				create = false
				plan.report.Counts.ProjectsSkipped++
			} else if exists && portableProjectMetadataEqual(existing, source) {
				// A previous import can keep the stable project ID while remapping
				// only its globally unique key and/or slug. Treat that as the same
				// imported subtree on retry instead of creating another project.
				create = false
				plan.report.Counts.ProjectsSkipped++
				if existing.Key != source.Key {
					portableAddRemap(&plan.report, "project", source.Key, existing.Key, "key", "reused key remap from an earlier import")
				}
				if existing.Slug != source.Slug {
					portableAddRemap(&plan.report, "project", source.Slug, existing.Slug, "slug", "reused slug remap from an earlier import")
				}
			} else {
				if exists && options.Conflict == portableConflictFail {
					return portableImportPlan{}, portableConflictError(plan.report, "project id conflict")
				}
				candidateID, candidateMatches := "", false
				if exists {
					// A deterministic remapped ID lets a retry discover the same
					// project even when an earlier candidate was already occupied.
					candidateID, candidateMatches, err = portableFindMatchingCandidate("project", source.ID, func(id string) (bool, bool, error) {
						candidate, candidateExists, lookupErr := portableExistingProject(q, id)
						if lookupErr != nil {
							return false, false, lookupErr
						}
						return candidateExists, candidateExists && portableProjectMetadataEqual(candidate, source), nil
					})
					if err != nil {
						return portableImportPlan{}, err
					}
				}
				if candidateMatches {
					candidate, candidateExists, lookupErr := portableExistingProject(q, candidateID)
					if lookupErr != nil {
						return portableImportPlan{}, lookupErr
					}
					if !candidateExists {
						return portableImportPlan{}, fmt.Errorf("portable remap candidate disappeared")
					}
					targetID, create = candidateID, false
					plan.report.Counts.ProjectsSkipped++
					portableAddRemap(&plan.report, "project", source.ID, targetID, "id", "reused deterministic remap from an earlier import")
					if candidate.Key != source.Key {
						portableAddRemap(&plan.report, "project", originalKey, candidate.Key, "key", "reused key remap from an earlier import")
					}
					if candidate.Slug != source.Slug {
						portableAddRemap(&plan.report, "project", originalSlug, candidate.Slug, "slug", "reused slug remap from an earlier import")
					}
				} else {
					targetID = source.ID
					if exists {
						targetID = portableCandidateID("project", source.ID, usedProjectIDs)
					}
					if _, taken := usedProjectIDs[targetID]; taken {
						targetID = portableCandidateID("project", source.ID, usedProjectIDs)
					}
					usedProjectIDs[targetID] = struct{}{}
					if targetID != source.ID {
						portableAddRemap(&plan.report, "project", source.ID, targetID, "id", "source id conflicted with a destination record")
					}
					if _, taken := usedProjectKeys[strings.ToUpper(source.Key)]; taken {
						source.Key = portableUniqueKey(source.Key, source.ID, usedProjectKeys)
						portableAddRemap(&plan.report, "project", originalKey, source.Key, "key", "project key conflicted with a destination record")
					}
					if _, taken := usedProjectSlugs[strings.ToLower(source.Slug)]; taken {
						source.Slug = portableUniqueSlug(source.Slug, source.ID, usedProjectSlugs)
						portableAddRemap(&plan.report, "project", originalSlug, source.Slug, "slug", "project slug conflicted with a destination record")
					}
					usedProjectKeys[strings.ToUpper(source.Key)] = struct{}{}
					usedProjectSlugs[strings.ToLower(source.Slug)] = struct{}{}
					plan.report.Counts.ProjectsCreated++
				}
			}
		}
		plan.projectMap[source.ID] = targetID
		plan.projects = append(plan.projects, portableProjectPlan{source: source, id: targetID, key: source.Key, slug: source.Slug, create: create})
	}
	if len(plan.report.Errors) > 0 {
		return plan, portableImportError(plan.report, "portable destination validation failed")
	}

	// Resolve columns and their unique project positions.
	columnUsedPositions := map[string]map[int]struct{}{}
	for _, source := range archive.Columns {
		projectID := plan.projectMap[source.ProjectID]
		if columnUsedPositions[projectID] == nil {
			columnUsedPositions[projectID] = map[int]struct{}{}
			rows, err := q.QueryContext(ctx, `SELECT position FROM columns WHERE project_id=?`, projectID)
			if err != nil {
				return portableImportPlan{}, err
			}
			for rows.Next() {
				var position int
				if err := rows.Scan(&position); err != nil {
					rows.Close()
					return portableImportPlan{}, err
				}
				columnUsedPositions[projectID][position] = struct{}{}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return portableImportPlan{}, err
			}
			rows.Close()
		}
		targetID, create := source.ID, true
		existing, exists, err := portableExistingColumn(q, targetID)
		if err != nil {
			return portableImportPlan{}, err
		}
		mappedSource := source
		mappedSource.ProjectID = projectID
		if exists && existing.ProjectID == projectID && portableColumnFieldsEqual(existing, mappedSource) {
			create = false
			plan.report.Counts.ColumnsSkipped++
		} else if exists && existing.ProjectID == projectID && portableColumnFieldsEqualIgnoringPosition(existing, mappedSource) {
			// The source position may have been remapped around an existing
			// destination column on the first import. Reuse the stable source ID
			// and retain the already-imported position on every retry.
			create = false
			plan.report.Counts.ColumnsSkipped++
			if existing.Position != source.Position {
				portableAddRemap(&plan.report, "column", strconv.Itoa(source.Position), strconv.Itoa(existing.Position), "position", "reused position remap from an earlier import")
			}
			plan.columns = append(plan.columns, portableColumnPlan{source: source, id: targetID, projectID: projectID, position: existing.Position, create: false})
			plan.columnMap[source.ID] = targetID
			continue
		} else {
			if exists && options.Conflict == portableConflictFail {
				return portableImportPlan{}, portableConflictError(plan.report, "column id conflict")
			}
			if exists {
				// Search the complete deterministic sequence so a retry reuses
				// whichever suffix was selected when an earlier candidate was
				// already occupied by unrelated destination data.
				candidateID, candidateMatches, lookupErr := portableFindMatchingCandidate("column", source.ID, func(id string) (bool, bool, error) {
					candidate, candidateExists, candidateErr := portableExistingColumn(q, id)
					if candidateErr != nil {
						return false, false, candidateErr
					}
					return candidateExists, candidateExists && candidate.ProjectID == projectID && portableColumnFieldsEqualIgnoringPosition(candidate, mappedSource), nil
				})
				if lookupErr != nil {
					return portableImportPlan{}, lookupErr
				}
				if candidateMatches {
					candidate, candidateExists, candidateErr := portableExistingColumn(q, candidateID)
					if candidateErr != nil {
						return portableImportPlan{}, candidateErr
					}
					if !candidateExists {
						return portableImportPlan{}, fmt.Errorf("portable remap candidate disappeared")
					}
					targetID, create = candidateID, false
					plan.report.Counts.ColumnsSkipped++
					portableAddRemap(&plan.report, "column", source.ID, targetID, "id", "reused deterministic remap from an earlier import")
					if candidate.Position != source.Position {
						portableAddRemap(&plan.report, "column", strconv.Itoa(source.Position), strconv.Itoa(candidate.Position), "position", "reused position remap from an earlier import")
					}
					plan.columns = append(plan.columns, portableColumnPlan{source: source, id: targetID, projectID: projectID, position: candidate.Position, create: false})
					plan.columnMap[source.ID] = targetID
					continue
				}
				targetID = portableCandidateID("column", source.ID, usedColumnIDs)
			}
			if _, taken := usedColumnIDs[targetID]; taken {
				targetID = portableCandidateID("column", source.ID, usedColumnIDs)
			}
			usedColumnIDs[targetID] = struct{}{}
			if targetID != source.ID {
				portableAddRemap(&plan.report, "column", source.ID, targetID, "id", "source id conflicted with a destination record")
			}
			position := source.Position
			if _, taken := columnUsedPositions[projectID][position]; taken {
				position = portableNextColumnPosition(columnUsedPositions[projectID])
				portableAddRemap(&plan.report, "column", strconv.Itoa(source.Position), strconv.Itoa(position), "position", "column position conflicted within the destination project")
			}
			columnUsedPositions[projectID][position] = struct{}{}
			plan.columns = append(plan.columns, portableColumnPlan{source: source, id: targetID, projectID: projectID, position: position, create: true})
			plan.report.Counts.ColumnsCreated++
			plan.columnMap[source.ID] = targetID
			continue
		}
		plan.columns = append(plan.columns, portableColumnPlan{source: source, id: targetID, projectID: projectID, position: source.Position, create: create})
		plan.columnMap[source.ID] = targetID
	}

	// Labels are reused by stable IDs or (for a separately created label with
	// the same name) by project/name.  Both mappings are explicitly reported.
	for _, source := range archive.Labels {
		projectID := plan.projectMap[source.ProjectID]
		targetID, create := source.ID, true
		existing, exists, err := portableExistingLabel(q, targetID)
		if err != nil {
			return portableImportPlan{}, err
		}
		mappedSource := source
		mappedSource.ProjectID = projectID
		if exists && existing.ProjectID == projectID && portableLabelFieldsEqual(existing, mappedSource) {
			create = false
			plan.report.Counts.LabelsSkipped++
		} else if named, namedExists, lookupErr := portableExistingNamedLabel(q, projectID, source.Name); lookupErr != nil {
			return portableImportPlan{}, lookupErr
		} else if namedExists {
			if options.Conflict == portableConflictFail && !portableLabelFieldsEqual(named, mappedSource) {
				return portableImportPlan{}, portableConflictError(plan.report, "label name conflicts with a destination record")
			}
			targetID, create = named.ID, false
			plan.report.Counts.LabelsSkipped++
			if targetID != source.ID {
				portableAddRemap(&plan.report, "label", source.ID, targetID, "id", "same project label name already exists")
			}
			if !portableLabelFieldsEqual(named, mappedSource) {
				plan.report.Warnings = append(plan.report.Warnings, fmt.Sprintf("label %s was retained because the destination already has the same name", source.ID))
			}
		} else {
			if exists && options.Conflict == portableConflictFail {
				return portableImportPlan{}, portableConflictError(plan.report, "label id conflict")
			}
			if exists {
				targetID = portableCandidateID("label", source.ID, usedLabelIDs)
			}
			// Reuse the first deterministic remap on retries when its fields
			// still match the source label.
			if exists {
				candidateID := portableStableID("label", source.ID)
				if candidateID != source.ID {
					candidate, candidateExists, lookupErr := portableExistingLabel(q, candidateID)
					if lookupErr != nil {
						return portableImportPlan{}, lookupErr
					}
					if candidateExists && candidate.ProjectID == projectID && portableLabelFieldsEqual(candidate, mappedSource) {
						targetID, create = candidateID, false
						plan.report.Counts.LabelsSkipped++
						portableAddRemap(&plan.report, "label", source.ID, targetID, "id", "reused deterministic remap from an earlier import")
						plan.labels = append(plan.labels, portableLabelPlan{source: source, id: targetID, projectID: projectID, create: false})
						plan.labelMap[source.ID] = targetID
						continue
					}
				}
			}
			if _, taken := usedLabelIDs[targetID]; taken {
				targetID = portableCandidateID("label", source.ID, usedLabelIDs)
			}
			usedLabelIDs[targetID] = struct{}{}
			if targetID != source.ID {
				portableAddRemap(&plan.report, "label", source.ID, targetID, "id", "source id conflicted with a destination record")
			}
			plan.report.Counts.LabelsCreated++
		}
		plan.labels = append(plan.labels, portableLabelPlan{source: source, id: targetID, projectID: projectID, create: create})
		plan.labelMap[source.ID] = targetID
	}

	actorMap := map[string]string{}
	usedNumbers := map[string]map[int]struct{}{}
	for _, source := range archive.Tasks {
		projectID := plan.projectMap[source.ProjectID]
		if usedNumbers[projectID] == nil {
			usedNumbers[projectID] = map[int]struct{}{}
			rows, err := q.QueryContext(ctx, `SELECT number FROM tasks WHERE project_id=?`, projectID)
			if err != nil {
				return portableImportPlan{}, err
			}
			for rows.Next() {
				var number int
				if err := rows.Scan(&number); err != nil {
					rows.Close()
					return portableImportPlan{}, err
				}
				usedNumbers[projectID][number] = struct{}{}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return portableImportPlan{}, err
			}
			rows.Close()
		}
		targetID, create := source.ID, true
		mappedSource := source
		mappedSource.ProjectID, mappedSource.ColumnID = projectID, plan.columnMap[source.ColumnID]
		mappedSource.AssigneeID, err = portableMapOptionalActor(q, source.AssigneeID, options.ActorID, &plan.report, actorMap)
		if err != nil {
			return portableImportPlan{}, err
		}
		mappedSource.ClaimedBy, err = portableMapOptionalActor(q, source.ClaimedBy, options.ActorID, &plan.report, actorMap)
		if err != nil {
			return portableImportPlan{}, err
		}
		if source.Bug != nil {
			mappedBug := *source.Bug
			mappedBug.ReporterID, err = portableMapActor(q, source.Bug.ReporterID, options.ActorID, &plan.report, actorMap)
			if err != nil {
				return portableImportPlan{}, err
			}
			if mappedBug.ResolvedBy != nil {
				mappedBug.ResolvedBy, err = portableMapOptionalActor(q, mappedBug.ResolvedBy, options.ActorID, &plan.report, actorMap)
				if err != nil {
					return portableImportPlan{}, err
				}
			}
			if mappedBug.DuplicateOf != nil {
				if mapped, ok := plan.taskMap[*mappedBug.DuplicateOf]; ok {
					mappedBug.DuplicateOf = &mapped
				}
			}
			mappedSource.Bug = &mappedBug
		}
		existing, exists, lookupErr := portableExistingTask(q, targetID)
		if lookupErr != nil {
			return portableImportPlan{}, lookupErr
		}
		if exists && existing.ProjectID == projectID && portableTaskFieldsEqual(existing, mappedSource) {
			create = false
			plan.report.Counts.TasksSkipped++
		} else if exists && existing.ProjectID == projectID && portableTaskFieldsEqualIgnoringNumber(existing, mappedSource) {
			// Task numbers are project-local and may have been advanced around
			// existing destination rows. A retry must reuse the same task ID and
			// number remap rather than creating a duplicate task subtree.
			create = false
			plan.report.Counts.TasksSkipped++
			if existing.Number != source.Number {
				portableAddRemap(&plan.report, "task", strconv.Itoa(source.Number), strconv.Itoa(existing.Number), "number", "reused number remap from an earlier import")
			}
			plan.tasks = append(plan.tasks, portableTaskPlan{source: mappedSource, id: targetID, projectID: projectID, columnID: mappedSource.ColumnID, number: existing.Number, assigneeID: mappedSource.AssigneeID, claimedBy: mappedSource.ClaimedBy, claimExpiresAt: mappedSource.ClaimExpiresAt, create: false})
			plan.taskMap[source.ID] = targetID
			continue
		} else {
			if exists && options.Conflict == portableConflictFail {
				return portableImportPlan{}, portableConflictError(plan.report, "task id conflict")
			}
			if exists {
				// Remapped task IDs are deterministic. Search all candidates on
				// retry so an unrelated collision on the first candidate cannot
				// cause a second copy to be inserted.
				candidateID, candidateMatches, lookupErr := portableFindMatchingCandidate("task", source.ID, func(id string) (bool, bool, error) {
					candidate, candidateExists, candidateErr := portableExistingTask(q, id)
					if candidateErr != nil {
						return false, false, candidateErr
					}
					return candidateExists, candidateExists && candidate.ProjectID == projectID && portableTaskFieldsEqualIgnoringNumber(candidate, mappedSource), nil
				})
				if lookupErr != nil {
					return portableImportPlan{}, lookupErr
				}
				if candidateMatches {
					candidate, candidateExists, candidateErr := portableExistingTask(q, candidateID)
					if candidateErr != nil {
						return portableImportPlan{}, candidateErr
					}
					if !candidateExists {
						return portableImportPlan{}, fmt.Errorf("portable remap candidate disappeared")
					}
					targetID, create = candidateID, false
					plan.report.Counts.TasksSkipped++
					portableAddRemap(&plan.report, "task", source.ID, targetID, "id", "reused deterministic remap from an earlier import")
					if candidate.Number != source.Number {
						portableAddRemap(&plan.report, "task", strconv.Itoa(source.Number), strconv.Itoa(candidate.Number), "number", "reused number remap from an earlier import")
					}
					plan.tasks = append(plan.tasks, portableTaskPlan{source: mappedSource, id: targetID, projectID: projectID, columnID: mappedSource.ColumnID, number: candidate.Number, assigneeID: mappedSource.AssigneeID, claimedBy: mappedSource.ClaimedBy, claimExpiresAt: mappedSource.ClaimExpiresAt, create: false})
					plan.taskMap[source.ID] = targetID
					continue
				}
				targetID = portableCandidateID("task", source.ID, usedTaskIDs)
			}
			if _, taken := usedTaskIDs[targetID]; taken {
				targetID = portableCandidateID("task", source.ID, usedTaskIDs)
			}
			usedTaskIDs[targetID] = struct{}{}
			if targetID != source.ID {
				portableAddRemap(&plan.report, "task", source.ID, targetID, "id", "source id conflicted with a destination record")
			}
			number := source.Number
			if _, taken := usedNumbers[projectID][number]; taken {
				number = portableNextTaskNumber(usedNumbers[projectID])
				portableAddRemap(&plan.report, "task", strconv.Itoa(source.Number), strconv.Itoa(number), "number", "task number conflicted within the destination project")
			}
			usedNumbers[projectID][number] = struct{}{}
			plan.report.Counts.TasksCreated++
			plan.tasks = append(plan.tasks, portableTaskPlan{source: mappedSource, id: targetID, projectID: projectID, columnID: mappedSource.ColumnID, number: number, assigneeID: mappedSource.AssigneeID, claimedBy: mappedSource.ClaimedBy, claimExpiresAt: mappedSource.ClaimExpiresAt, create: true})
			plan.taskMap[source.ID] = targetID
			continue
		}
		plan.tasks = append(plan.tasks, portableTaskPlan{source: mappedSource, id: targetID, projectID: projectID, columnID: mappedSource.ColumnID, number: source.Number, assigneeID: mappedSource.AssigneeID, claimedBy: mappedSource.ClaimedBy, claimExpiresAt: mappedSource.ClaimExpiresAt, create: create})
		plan.taskMap[source.ID] = targetID
	}

	// A bug can refer forward to another task, so remap duplicate_of after the
	// complete task map exists.
	taskIDRemaps := map[string]string{}
	for index := range plan.tasks {
		if plan.tasks[index].source.Bug == nil || plan.tasks[index].source.Bug.DuplicateOf == nil {
			continue
		}
		if mapped, ok := plan.taskMap[*plan.tasks[index].source.Bug.DuplicateOf]; ok {
			plan.tasks[index].source.Bug.DuplicateOf = &mapped
		}
	}
	// Candidate matching above happens while task IDs are being resolved. A
	// forward duplicate_of reference is not mapped until the complete task map
	// exists, so retry matching those bug tasks once more after the remap pass.
	// This prevents a deterministic retry from inserting a second bug when its
	// duplicate target was itself remapped.
	for changed := true; changed; {
		changed = false
		for index := range plan.tasks {
			if !plan.tasks[index].create || plan.tasks[index].source.Bug == nil || plan.tasks[index].source.Bug.DuplicateOf == nil {
				continue
			}
			sourceID := plan.tasks[index].source.ID
			candidateID, candidateMatches, lookupErr := portableFindMatchingCandidate("task", sourceID, func(id string) (bool, bool, error) {
				candidate, candidateExists, candidateErr := portableExistingTask(q, id)
				if candidateErr != nil {
					return false, false, candidateErr
				}
				return candidateExists, candidateExists && candidate.ProjectID == plan.tasks[index].projectID && portableTaskFieldsEqualIgnoringNumber(candidate, plan.tasks[index].source), nil
			})
			if lookupErr != nil {
				return portableImportPlan{}, lookupErr
			}
			if !candidateMatches {
				continue
			}
			candidate, candidateExists, candidateErr := portableExistingTask(q, candidateID)
			if candidateErr != nil {
				return portableImportPlan{}, candidateErr
			}
			if !candidateExists {
				return portableImportPlan{}, fmt.Errorf("portable remap candidate disappeared")
			}
			oldID := plan.tasks[index].id
			plan.tasks[index].id, plan.tasks[index].number, plan.tasks[index].create = candidateID, candidate.Number, false
			taskIDRemaps[oldID] = candidateID
			plan.taskMap[sourceID] = candidateID
			plan.report.Counts.TasksCreated--
			plan.report.Counts.TasksSkipped++
			portableAddRemap(&plan.report, "task", sourceID, candidateID, "id", "reused deterministic remap after relationship remapping")
			if candidate.Number != plan.tasks[index].source.Number {
				portableAddRemap(&plan.report, "task", strconv.Itoa(plan.tasks[index].source.Number), strconv.Itoa(candidate.Number), "number", "reused number remap after relationship remapping")
			}
			changed = true
		}
		for index := range plan.tasks {
			if plan.tasks[index].source.Bug == nil || plan.tasks[index].source.Bug.DuplicateOf == nil {
				continue
			}
			for {
				mapped, ok := taskIDRemaps[*plan.tasks[index].source.Bug.DuplicateOf]
				if !ok || mapped == *plan.tasks[index].source.Bug.DuplicateOf {
					break
				}
				plan.tasks[index].source.Bug.DuplicateOf = &mapped
			}
		}
	}

	for _, relation := range archive.Relationships.TaskLabels {
		taskID, labelID := plan.taskMap[relation.TaskID], plan.labelMap[relation.LabelID]
		exists, err := portableTaskLabelExists(q, taskID, labelID)
		if err != nil {
			return portableImportPlan{}, err
		}
		if exists {
			plan.report.Counts.TaskLabelsSkipped++
		} else {
			plan.report.Counts.TaskLabelsCreated++
		}
		if !exists {
			plan.taskLabels = append(plan.taskLabels, PortableTaskLabel{TaskID: taskID, LabelID: labelID})
		}
	}
	dependencyPrerequisiteCounts := map[string]int{}
	dependencyDependentCounts := map[string]int{}
	dependencyCountsLoaded := map[string]bool{}
	loadDependencyCounts := func(taskID string) error {
		if dependencyCountsLoaded[taskID] {
			return nil
		}
		var prerequisiteCount, dependentCount int
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_dependencies WHERE task_id=?`, taskID).Scan(&prerequisiteCount); err != nil {
			return err
		}
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_dependencies WHERE prerequisite_task_id=?`, taskID).Scan(&dependentCount); err != nil {
			return err
		}
		dependencyPrerequisiteCounts[taskID] = prerequisiteCount
		dependencyDependentCounts[taskID] = dependentCount
		dependencyCountsLoaded[taskID] = true
		return nil
	}
	dependencyGraph := map[string]map[string]struct{}{}
	rows, err = q.QueryContext(ctx, `SELECT task_id,prerequisite_task_id FROM task_dependencies`)
	if err != nil {
		return portableImportPlan{}, err
	}
	for rows.Next() {
		var taskID, prerequisiteID string
		if err := rows.Scan(&taskID, &prerequisiteID); err != nil {
			rows.Close()
			return portableImportPlan{}, err
		}
		if dependencyGraph[taskID] == nil {
			dependencyGraph[taskID] = map[string]struct{}{}
		}
		dependencyGraph[taskID][prerequisiteID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return portableImportPlan{}, err
	}
	rows.Close()
	columnStates := map[string]string{}
	for _, column := range plan.columns {
		columnStates[column.id] = column.source.SemanticState
	}
	dependencyNodes := map[string]portableDependencyNode{}
	for _, task := range plan.tasks {
		if task.create {
			dependencyNodes[task.id] = portablePlannedDependencyNode(task, columnStates)
		}
	}
	loadDependencyNode := func(taskID string) (portableDependencyNode, bool, error) {
		if node, planned := dependencyNodes[taskID]; planned {
			return node, true, nil
		}
		return portableExistingDependencyNode(ctx, q, taskID)
	}
	dependencyCycleConflict := false
	dependencyLifecycleIssueSeen := map[string]struct{}{}
	for _, relation := range archive.Relationships.Dependencies {
		taskID, prerequisiteID := plan.taskMap[relation.TaskID], plan.taskMap[relation.PrerequisiteID]
		createdBy, err := portableMapOptionalActor(q, relation.CreatedBy, options.ActorID, &plan.report, actorMap)
		if err != nil {
			return portableImportPlan{}, err
		}
		exists, err := portableDependencyExists(q, taskID, prerequisiteID)
		if err != nil {
			return portableImportPlan{}, err
		}
		if exists {
			plan.report.Counts.DependenciesSkipped++
			plan.dependencies = append(plan.dependencies, portableDependencyPlan{source: relation, taskID: taskID, prerequisiteID: prerequisiteID, createdBy: createdBy, create: false})
			continue
		}
		dependentNode, dependentOK, err := loadDependencyNode(taskID)
		if err != nil {
			return portableImportPlan{}, err
		}
		prerequisiteNode, prerequisiteOK, err := loadDependencyNode(prerequisiteID)
		if err != nil {
			return portableImportPlan{}, err
		}
		if !dependentOK {
			addPortableIssue(&plan.report, "dependency", relation.TaskID, "task_id", "destination task is not available")
		}
		if !prerequisiteOK {
			addPortableIssue(&plan.report, "dependency", relation.PrerequisiteID, "prerequisite_task_id", "destination prerequisite is not available")
		}
		if !dependentOK || !prerequisiteOK {
			continue
		}
		if !dependentNode.Live || !prerequisiteNode.Live || dependentNode.ProjectID != prerequisiteNode.ProjectID {
			addPortableIssue(&plan.report, "dependency", relation.TaskID, "task_id", "dependencies must stay within one live project")
			continue
		}
		if portableDependencyGraphHasPath(dependencyGraph, prerequisiteID, taskID) {
			addPortableIssue(&plan.report, "dependency", relation.TaskID, "prerequisite_task_id", "dependency would create a cycle in the destination graph")
			dependencyCycleConflict = true
			continue
		}
		if err := loadDependencyCounts(taskID); err != nil {
			return portableImportPlan{}, err
		}
		if err := loadDependencyCounts(prerequisiteID); err != nil {
			return portableImportPlan{}, err
		}
		limitExceeded := false
		if dependencyPrerequisiteCounts[taskID] >= maxDirectTaskDependencies {
			addPortableIssue(&plan.report, "dependency", relation.TaskID, "task_id", fmt.Sprintf("destination task already has %d direct prerequisites; the limit is %d", dependencyPrerequisiteCounts[taskID], maxDirectTaskDependencies))
			limitExceeded = true
		}
		if dependencyDependentCounts[prerequisiteID] >= maxDirectTaskDependencies {
			addPortableIssue(&plan.report, "dependency", relation.PrerequisiteID, "prerequisite_task_id", fmt.Sprintf("destination prerequisite already has %d direct dependents; the limit is %d", dependencyDependentCounts[prerequisiteID], maxDirectTaskDependencies))
			limitExceeded = true
		}
		if limitExceeded {
			// Do not queue an INSERT that SQLite will reject. The accumulated
			// report is returned by both dry-run and write paths before any
			// mutation occurs.
			continue
		}
		if dependencyGraph[taskID] == nil {
			dependencyGraph[taskID] = map[string]struct{}{}
		}
		dependencyGraph[taskID][prerequisiteID] = struct{}{}
		dependencyPrerequisiteCounts[taskID]++
		dependencyDependentCounts[prerequisiteID]++
		plan.report.Counts.DependenciesCreated++
		plan.dependencies = append(plan.dependencies, portableDependencyPlan{source: relation, taskID: taskID, prerequisiteID: prerequisiteID, createdBy: createdBy, create: true})
		if dependentNode.SemanticState == "active" || dependentNode.SemanticState == "completed" || dependentNode.ClaimActive {
			for candidateID := range dependencyGraph[taskID] {
				candidate, candidateOK, candidateErr := loadDependencyNode(candidateID)
				if candidateErr != nil {
					return portableImportPlan{}, candidateErr
				}
				issueKey := taskID + "\x00" + candidateID
				if !candidateOK || !candidate.Live || candidate.ProjectID != dependentNode.ProjectID || !candidate.Completed || candidate.SemanticState != "completed" {
					if _, alreadyReported := dependencyLifecycleIssueSeen[issueKey]; !alreadyReported {
						addPortableIssue(&plan.report, "dependency", relation.TaskID, "task_id", "dependency prerequisites are not satisfied for this task state")
						dependencyLifecycleIssueSeen[issueKey] = struct{}{}
					}
				}
			}
		}
	}
	linkConflict := false
	for _, relation := range archive.Relationships.TaskLinks {
		sourceID, targetID := plan.taskMap[relation.SourceTaskID], plan.taskMap[relation.TargetTaskID]
		existingTarget, linkTypeExists, err := portableExistingLinkTarget(q, sourceID, relation.LinkType)
		if err != nil {
			return portableImportPlan{}, err
		}
		if linkTypeExists && existingTarget == targetID {
			plan.report.Counts.LinksSkipped++
			plan.links = append(plan.links, portableLinkPlan{source: relation, sourceTaskID: sourceID, targetTaskID: targetID, create: false})
			continue
		}
		if linkTypeExists {
			if options.Conflict == portableConflictFail {
				addPortableIssue(&plan.report, "task_link", relation.SourceTaskID, "target_task_id", fmt.Sprintf("destination already links source task %s with type %q to task %s", sourceID, relation.LinkType, existingTarget))
				linkConflict = true
				continue
			}
			plan.report.Counts.LinksSkipped++
			portableAddRemap(&plan.report, "task_link", relation.TargetTaskID, existingTarget, "target_task_id", "destination already has a different target for this source and link type; destination relationship was retained")
			plan.report.Warnings = append(plan.report.Warnings, fmt.Sprintf("task link %s/%s was retained because the destination already has target %s", relation.SourceTaskID, relation.LinkType, existingTarget))
			plan.links = append(plan.links, portableLinkPlan{source: relation, sourceTaskID: sourceID, targetTaskID: targetID, create: false})
			continue
		}
		plan.report.Counts.LinksCreated++
		plan.links = append(plan.links, portableLinkPlan{source: relation, sourceTaskID: sourceID, targetTaskID: targetID, create: true})
	}
	if linkConflict {
		return plan, portableConflictError(plan.report, "task link conflicts with a destination relationship")
	}
	for _, source := range archive.Comments {
		taskID := plan.taskMap[source.TaskID]
		actorID, err := portableMapActor(q, source.ActorID, options.ActorID, &plan.report, actorMap)
		if err != nil {
			return portableImportPlan{}, err
		}
		targetID, create := source.ID, true
		mapped := source
		mapped.TaskID, mapped.ActorID = taskID, actorID
		existing, exists, err := portableExistingComment(q, targetID)
		if err != nil {
			return portableImportPlan{}, err
		}
		if exists && portableCommentFieldsEqual(existing, mapped) {
			create = false
			plan.report.Counts.CommentsSkipped++
		} else {
			if exists && options.Conflict == portableConflictFail {
				return portableImportPlan{}, portableConflictError(plan.report, "comment id conflict")
			}
			if exists {
				// Keep retries idempotent when the source comment ID was remapped
				// during an earlier import, including when its first candidate was
				// already occupied by unrelated destination data.
				candidateID, candidateMatches, lookupErr := portableFindMatchingCandidate("comment", source.ID, func(id string) (bool, bool, error) {
					candidate, candidateExists, candidateErr := portableExistingComment(q, id)
					if candidateErr != nil {
						return false, false, candidateErr
					}
					return candidateExists, candidateExists && portableCommentFieldsEqual(candidate, mapped), nil
				})
				if lookupErr != nil {
					return portableImportPlan{}, lookupErr
				}
				if candidateMatches {
					targetID, create = candidateID, false
					plan.report.Counts.CommentsSkipped++
					portableAddRemap(&plan.report, "comment", source.ID, targetID, "id", "reused deterministic remap from an earlier import")
					plan.comments = append(plan.comments, portableCommentPlan{source: mapped, id: targetID, taskID: taskID, actorID: actorID, create: false})
					plan.commentMap[source.ID] = targetID
					continue
				}
				targetID = portableCandidateID("comment", source.ID, usedCommentIDs)
			}
			if _, taken := usedCommentIDs[targetID]; taken {
				targetID = portableCandidateID("comment", source.ID, usedCommentIDs)
			}
			usedCommentIDs[targetID] = struct{}{}
			if targetID != source.ID {
				portableAddRemap(&plan.report, "comment", source.ID, targetID, "id", "source id conflicted with a destination record")
			}
			plan.report.Counts.CommentsCreated++
		}
		plan.comments = append(plan.comments, portableCommentPlan{source: mapped, id: targetID, taskID: taskID, actorID: actorID, create: create})
		plan.commentMap[source.ID] = targetID
	}

	for _, source := range archive.Activity.Events {
		targetID := source.ID
		mapped := source
		if source.ActorID != nil {
			mapped.ActorID, err = portableMapOptionalActor(q, source.ActorID, options.ActorID, &plan.report, actorMap)
			if err != nil {
				return portableImportPlan{}, err
			}
		}
		if source.ProjectID != nil {
			target := plan.projectMap[*source.ProjectID]
			mapped.ProjectID = &target
		}
		if source.TaskID != nil {
			target := plan.taskMap[*source.TaskID]
			mapped.TaskID = &target
			if mapped.ProjectID == nil {
				if task, ok := archiveTaskByID(archive.Tasks, *source.TaskID); ok {
					project := plan.projectMap[task.ProjectID]
					mapped.ProjectID = &project
				}
			}
		}
		existing, exists, err := portableExistingEvent(q, targetID)
		if err != nil {
			return portableImportPlan{}, err
		}
		create := true
		if exists && portableEventFieldsEqual(existing, mapped) {
			create = false
			plan.report.Counts.EventsSkipped++
			if source.Cursor > 0 {
				plan.eventCursorMap[source.Cursor] = existing.Cursor
			}
		} else {
			if exists && options.Conflict == portableConflictFail {
				return portableImportPlan{}, portableConflictError(plan.report, "event id conflict")
			}
			if exists {
				// Event IDs are stable across installations. Reuse a matching
				// deterministic candidate on retries, even when an earlier
				// candidate was occupied by unrelated destination data.
				candidateID, candidateMatches, lookupErr := portableFindMatchingCandidate("event", source.ID, func(id string) (bool, bool, error) {
					candidate, candidateExists, candidateErr := portableExistingEvent(q, id)
					if candidateErr != nil {
						return false, false, candidateErr
					}
					return candidateExists, candidateExists && portableEventFieldsEqualIgnoringID(candidate, mapped), nil
				})
				if lookupErr != nil {
					return portableImportPlan{}, lookupErr
				}
				if candidateMatches {
					candidate, candidateExists, candidateErr := portableExistingEvent(q, candidateID)
					if candidateErr != nil {
						return portableImportPlan{}, candidateErr
					}
					if !candidateExists {
						return portableImportPlan{}, fmt.Errorf("portable remap candidate disappeared")
					}
					targetID, create = candidateID, false
					plan.report.Counts.EventsSkipped++
					portableAddRemap(&plan.report, "event", source.ID, targetID, "id", "reused deterministic remap from an earlier import")
					if source.Cursor > 0 {
						plan.eventCursorMap[source.Cursor] = candidate.Cursor
					}
					plan.events = append(plan.events, portableEventPlan{source: mapped, id: targetID, actorID: mapped.ActorID, projectID: mapped.ProjectID, taskID: mapped.TaskID, create: false})
					continue
				}
				targetID = portableCandidateID("event", source.ID, usedEventIDs)
			}
			if _, taken := usedEventIDs[targetID]; taken {
				targetID = portableCandidateID("event", source.ID, usedEventIDs)
			}
			usedEventIDs[targetID] = struct{}{}
			if targetID != source.ID {
				portableAddRemap(&plan.report, "event", source.ID, targetID, "id", "source id conflicted with a destination record")
			}
			plan.report.Counts.EventsCreated++
		}
		plan.events = append(plan.events, portableEventPlan{source: mapped, id: targetID, actorID: mapped.ActorID, projectID: mapped.ProjectID, taskID: mapped.TaskID, create: create})
	}
	for _, source := range archive.Activity.AgentWork {
		taskID := plan.taskMap[source.TaskID]
		actorID, err := portableMapActor(q, source.ActorID, options.ActorID, &plan.report, actorMap)
		if err != nil {
			return portableImportPlan{}, err
		}
		mappedWork := source
		mappedWork.TaskID, mappedWork.ActorID = taskID, actorID
		existingWork, exists, err := portableExistingWork(q, taskID)
		if err != nil {
			return portableImportPlan{}, err
		}
		create := !exists
		if exists && portableWorkFieldsEqual(existingWork, mappedWork) {
			plan.report.Counts.AgentWorkSkipped++
		} else if exists {
			if options.Conflict == portableConflictFail {
				return portableImportPlan{}, portableConflictError(plan.report, "agent work snapshot conflicts with a destination record")
			}
			plan.report.Warnings = append(plan.report.Warnings, fmt.Sprintf("agent work snapshot for task %s was retained because the destination already has a different snapshot", taskID))
			plan.report.Counts.AgentWorkSkipped++
		} else {
			plan.report.Counts.AgentWorkCreated++
		}
		plan.work = append(plan.work, portableWorkPlan{source: source, taskID: taskID, actorID: actorID, create: create})
	}
	for _, source := range archive.Activity.AgentWorkHistory {
		taskID := plan.taskMap[source.TaskID]
		actorID, err := portableMapActor(q, source.ActorID, options.ActorID, &plan.report, actorMap)
		if err != nil {
			return portableImportPlan{}, err
		}
		generatedCommentID := source.GeneratedCommentID
		if generatedCommentID != nil {
			mapped := plan.commentMap[*generatedCommentID]
			if mapped != "" {
				generatedCommentID = &mapped
			}
		}
		mappedHistory := source
		mappedHistory.TaskID, mappedHistory.ActorID = taskID, actorID
		mappedHistory.GeneratedCommentID = generatedCommentID
		if source.ProgressEventCursor != nil {
			if mappedCursor, ok := plan.eventCursorMap[*source.ProgressEventCursor]; ok {
				mappedHistory.ProgressEventCursor = &mappedCursor
			}
		}
		targetID, create := source.ID, true
		existingHistory, existingHistoryOK, err := portableExistingHistory(q, targetID)
		if err != nil {
			return portableImportPlan{}, err
		}
		mappedHistory.ID = targetID
		if existingHistoryOK && portableHistoryFieldsEqual(existingHistory, mappedHistory) {
			create = false
			plan.report.Counts.HistorySkipped++
		} else {
			if existingHistoryOK && options.Conflict == portableConflictFail {
				return portableImportPlan{}, portableConflictError(plan.report, "agent work history id conflict")
			}
			if existingHistoryOK {
				candidateID, candidateMatches, lookupErr := portableFindMatchingCandidate("agent_work_history", source.ID, func(id string) (bool, bool, error) {
					candidate, candidateExists, candidateErr := portableExistingHistory(q, id)
					if candidateErr != nil {
						return false, false, candidateErr
					}
					return candidateExists, candidateExists && portableHistoryFieldsEqualIgnoringID(candidate, mappedHistory), nil
				})
				if lookupErr != nil {
					return portableImportPlan{}, lookupErr
				}
				if candidateMatches {
					_, candidateExists, candidateErr := portableExistingHistory(q, candidateID)
					if candidateErr != nil {
						return portableImportPlan{}, candidateErr
					}
					if !candidateExists {
						return portableImportPlan{}, fmt.Errorf("portable remap candidate disappeared")
					}
					targetID, create = candidateID, false
					plan.report.Counts.HistorySkipped++
					portableAddRemap(&plan.report, "agent_work_history", source.ID, targetID, "id", "reused deterministic remap from an earlier import")
				}
			}
			if create {
				if existingHistoryOK {
					targetID = portableCandidateID("agent_work_history", source.ID, usedHistoryIDs)
				}
				if _, taken := usedHistoryIDs[targetID]; taken {
					targetID = portableCandidateID("agent_work_history", source.ID, usedHistoryIDs)
				}
				usedHistoryIDs[targetID] = struct{}{}
				if targetID != source.ID {
					portableAddRemap(&plan.report, "agent_work_history", source.ID, targetID, "id", "source id conflicted with a destination record")
				}
				plan.report.Counts.HistoryCreated++
			}
		}
		mappedHistory.ID = targetID
		plan.history = append(plan.history, portableHistoryPlan{source: source, id: targetID, taskID: taskID, actorID: actorID, generatedCommentID: generatedCommentID, create: create})
	}
	if err := portableValidateActivityPlan(ctx, q, &plan); err != nil {
		return portableImportPlan{}, err
	}
	if dependencyCycleConflict {
		return plan, portableConflictError(plan.report, "portable dependency validation found a destination cycle")
	}
	if len(plan.report.Errors) > 0 {
		return plan, portableImportError(plan.report, "portable destination validation failed")
	}
	return plan, nil
}

func archiveTaskByID(tasks []PortableTask, id string) (PortableTask, bool) {
	for _, task := range tasks {
		if task.ID == id {
			return task, true
		}
	}
	return PortableTask{}, false
}

func portableStringArg(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func portableMarshalRefs(refs []string) (string, error) {
	if refs == nil {
		refs = []string{}
	}
	encoded, err := json.Marshal(refs)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// ValidatePortable performs all archive and destination checks without opening
// a write transaction or changing any row. The returned report is useful for
// a UI preview and is also embedded in validation errors by the HTTP layer.
func (s *Store) ValidatePortable(ctx context.Context, archive PortableArchive, options PortableImportOptions) (PortableImportReport, error) {
	options.DryRun = true
	archive.normalize()
	plan, err := buildPortableImportPlan(ctx, s.DB, archive, options)
	if plan.report.Format == "" {
		plan.report = PortableImportReport{Format: archive.Format, Version: archive.Version, DryRun: true, Conflict: options.Conflict, Remaps: []PortableImportRemap{}, Warnings: []string{}, Errors: []PortableImportIssue{}}
	}
	return plan.report, err
}

// ImportPortable inserts a validated archive in one transaction. Existing
// rows are reused only when their stable identifiers and exported fields are
// equal; otherwise IDs/keys/numbers are deterministically remapped and every
// remap is returned. It never updates, deletes, or replaces destination data.
func (s *Store) ImportPortable(ctx context.Context, archive PortableArchive, options PortableImportOptions) (PortableImportReport, error) {
	if options.Conflict == "" {
		options.Conflict = portableConflictRemap
	}
	if options.DryRun {
		return s.ValidatePortable(ctx, archive, options)
	}
	archive.normalize()
	report := PortableImportReport{Format: archive.Format, Version: archive.Version, Conflict: options.Conflict, Remaps: []PortableImportRemap{}, Warnings: []string{}, Errors: []PortableImportIssue{}}
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		plan, planErr := buildPortableImportPlan(ctx, tx, archive, options)
		report = plan.report
		if planErr != nil {
			return planErr
		}
		executeErr := executePortableImportPlan(ctx, tx, &plan)
		report = plan.report
		return executeErr
	})
	return report, err
}

func bumpPortableTaskCollectionRevisions(ctx context.Context, tx *sql.Tx, plan *portableImportPlan) error {
	changedProjects := make(map[string]struct{})
	addProject := func(projectID string) {
		if projectID != "" {
			changedProjects[projectID] = struct{}{}
		}
	}
	taskProjects := make(map[string]string, len(plan.tasks)*2)
	for _, task := range plan.tasks {
		if task.id != "" && task.projectID != "" {
			taskProjects[task.id] = task.projectID
		}
		if task.source.ID != "" && task.projectID != "" {
			taskProjects[task.source.ID] = task.projectID
		}
	}
	labelProjects := make(map[string]string, len(plan.labels))
	for _, label := range plan.labels {
		if label.id != "" && label.projectID != "" {
			labelProjects[label.id] = label.projectID
		}
		if label.source.ID != "" && label.projectID != "" {
			labelProjects[label.source.ID] = label.projectID
		}
	}
	for _, project := range plan.projects {
		if project.create {
			addProject(project.id)
		}
	}
	for _, column := range plan.columns {
		if column.create {
			addProject(column.projectID)
		}
	}
	for _, label := range plan.labels {
		if label.create {
			addProject(label.projectID)
		}
	}
	for _, task := range plan.tasks {
		if task.create {
			addProject(task.projectID)
		}
	}
	// taskLabels only contains rows that preflight determined to be new; an
	// existing relation is counted as skipped and is not appended to the plan.
	for _, relation := range plan.taskLabels {
		if projectID := taskProjects[relation.TaskID]; projectID != "" {
			addProject(projectID)
		} else {
			addProject(labelProjects[relation.LabelID])
		}
	}
	for _, relation := range plan.dependencies {
		if !relation.create {
			continue
		}
		addProject(taskProjects[relation.taskID])
		if _, known := taskProjects[relation.taskID]; !known {
			addProject(taskProjects[relation.prerequisiteID])
		}
	}
	for _, relation := range plan.links {
		if !relation.create {
			continue
		}
		addProject(taskProjects[relation.sourceTaskID])
		if _, known := taskProjects[relation.sourceTaskID]; !known {
			addProject(taskProjects[relation.targetTaskID])
		}
	}
	for _, comment := range plan.comments {
		if comment.create {
			addProject(taskProjects[comment.taskID])
		}
	}
	for _, event := range plan.events {
		if !event.create {
			continue
		}
		if event.projectID != nil {
			addProject(*event.projectID)
		}
		if event.taskID != nil {
			addProject(taskProjects[*event.taskID])
		}
	}
	for _, work := range plan.work {
		if work.create {
			addProject(taskProjects[work.taskID])
		}
	}
	for _, history := range plan.history {
		if history.create {
			addProject(taskProjects[history.taskID])
		}
	}
	projectIDs := make([]string, 0, len(changedProjects))
	for projectID := range changedProjects {
		projectIDs = append(projectIDs, projectID)
	}
	if len(projectIDs) == 0 {
		return nil
	}
	sort.Strings(projectIDs)
	where, args := portableIDsClause("id", projectIDs)
	_, err := tx.ExecContext(ctx, `UPDATE projects SET task_collection_revision=task_collection_revision+1 WHERE 1=1`+where, args...)
	return err
}

func executePortableImportPlan(ctx context.Context, tx *sql.Tx, plan *portableImportPlan) error {
	for _, project := range plan.projects {
		if !project.create {
			continue
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO projects(id,key,slug,name,description,color,favorite,archived_at,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?)`, project.id, project.key, project.slug, project.source.Name, project.source.Description, project.source.Color, boolInt(project.source.Favorite), portableStringArg(project.source.ArchivedAt), project.source.CreatedAt, project.source.UpdatedAt)
		if err != nil {
			return err
		}
	}
	for _, column := range plan.columns {
		if !column.create {
			continue
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO columns(id,project_id,name,semantic_state,position,created_at,updated_at) VALUES (?,?,?,?,?,?,?)`, column.id, column.projectID, column.source.Name, column.source.SemanticState, column.position, column.source.CreatedAt, column.source.UpdatedAt)
		if err != nil {
			return err
		}
	}
	for _, label := range plan.labels {
		if !label.create {
			continue
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO labels(id,project_id,name,color,created_at,updated_at) VALUES (?,?,?,?,?,?)`, label.id, label.projectID, label.source.Name, label.source.Color, label.source.CreatedAt, label.source.UpdatedAt)
		if err != nil {
			return err
		}
	}
	for _, task := range plan.tasks {
		if !task.create {
			continue
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO tasks(id,project_id,number,column_id,kind,title,description,priority,position,assignee_id,claimed_by,claim_expires_at,due_at,version,completed_at,deleted_at,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULL,?,?)`, task.id, task.projectID, task.number, task.columnID, task.source.Kind, task.source.Title, task.source.Description, task.source.Priority, task.source.Position, portableStringArg(task.assigneeID), portableStringArg(task.claimedBy), portableStringArg(task.claimExpiresAt), portableStringArg(task.source.DueAt), task.source.Version, portableStringArg(task.source.CompletedAt), task.source.CreatedAt, task.source.UpdatedAt)
		if err != nil {
			return err
		}
	}
	// Bug details reference tasks, including duplicate_of, so insert them only
	// after all task rows exist. This preserves the complete typed-task state.
	for _, task := range plan.tasks {
		if !task.create || task.source.Bug == nil {
			continue
		}
		bug := task.source.Bug
		_, err := tx.ExecContext(ctx, `INSERT INTO bug_details(task_id,reporter_id,severity,actual_behavior,expected_behavior,reproduction_steps,environment,affected_version,resolution,resolved_by,resolved_at,duplicate_of) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, task.id, bug.ReporterID, portableStringArg(bug.Severity), bug.ActualBehavior, bug.ExpectedBehavior, bug.ReproductionSteps, bug.Environment, bug.AffectedVersion, portableStringArg(bug.Resolution), portableStringArg(bug.ResolvedBy), portableStringArg(bug.ResolvedAt), portableStringArg(bug.DuplicateOf))
		if err != nil {
			return err
		}
	}
	for _, relation := range plan.taskLabels {
		_, err := tx.ExecContext(ctx, `INSERT INTO task_labels(task_id,label_id) VALUES (?,?)`, relation.TaskID, relation.LabelID)
		if err != nil {
			return err
		}
	}
	for _, relation := range plan.dependencies {
		if !relation.create {
			continue
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO task_dependencies(task_id,prerequisite_task_id,created_by,created_at) VALUES (?,?,?,?)`, relation.taskID, relation.prerequisiteID, portableStringArg(relation.createdBy), relation.source.CreatedAt)
		if err != nil {
			return err
		}
	}
	for _, relation := range plan.links {
		if !relation.create {
			continue
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO task_links(source_task_id,target_task_id,link_type,created_at) VALUES (?,?,?,?)`, relation.sourceTaskID, relation.targetTaskID, relation.source.LinkType, relation.source.CreatedAt)
		if err != nil {
			return err
		}
	}
	for _, comment := range plan.comments {
		if !comment.create {
			continue
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO comments(id,task_id,actor_id,body,created_at,updated_at) VALUES (?,?,?,?,?,?)`, comment.id, comment.taskID, comment.actorID, comment.source.Body, comment.source.CreatedAt, comment.source.UpdatedAt)
		if err != nil {
			return err
		}
	}
	for _, event := range plan.events {
		var cursor int64
		if event.create {
			result, err := tx.ExecContext(ctx, `INSERT INTO events(id,type,actor_id,project_id,task_id,payload,created_at) VALUES (?,?,?,?,?,?,?)`, event.id, event.source.Type, portableStringArg(event.actorID), portableStringArg(event.projectID), portableStringArg(event.taskID), string(event.source.Payload), event.source.CreatedAt)
			if err != nil {
				return err
			}
			cursor, err = result.LastInsertId()
			if err != nil {
				return err
			}
		} else {
			if err := tx.QueryRowContext(ctx, `SELECT cursor FROM events WHERE id=?`, event.id).Scan(&cursor); err != nil {
				return err
			}
		}
		if event.source.Cursor > 0 {
			plan.eventCursorMap[event.source.Cursor] = cursor
			if event.source.Cursor != cursor {
				portableAddRemap(&plan.report, "event", strconv.FormatInt(event.source.Cursor, 10), strconv.FormatInt(cursor, 10), "cursor", "event cursors are local to the destination SQLite instance")
				plan.report.Warnings = append(plan.report.Warnings, fmt.Sprintf("event %s cursor %d was remapped to destination cursor %d", event.source.ID, event.source.Cursor, cursor))
			}
		}
	}
	for _, work := range plan.work {
		if !work.create {
			continue
		}
		refs, err := portableMarshalRefs(work.source.CheckpointRefs)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO task_agent_work(task_id,operation_id,actor_id,state,phase,summary,next_action,checkpoint_refs,checkpoint_completed,checkpoint_total,started_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, work.taskID, work.source.OperationID, work.actorID, work.source.State, work.source.Phase, work.source.Summary, work.source.NextAction, refs, nullableIntArg(work.source.CheckpointCompleted), nullableIntArg(work.source.CheckpointTotal), work.source.StartedAt, work.source.UpdatedAt)
		if err != nil {
			return err
		}
	}
	for _, history := range plan.history {
		if !history.create {
			continue
		}
		refs, err := portableMarshalRefs(history.source.CheckpointRefs)
		if err != nil {
			return err
		}
		var progress any
		if history.source.ProgressEventCursor != nil {
			if mapped, ok := plan.eventCursorMap[*history.source.ProgressEventCursor]; ok {
				progress = mapped
			} else {
				portableAddRemap(&plan.report, "agent_work_history", strconv.FormatInt(*history.source.ProgressEventCursor, 10), "", "progress_event_cursor", "referenced event cursor was not imported")
				plan.report.Warnings = append(plan.report.Warnings, fmt.Sprintf("agent work history %s references event cursor %d that was not imported; reference cleared", history.source.ID, *history.source.ProgressEventCursor))
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO task_agent_work_history(id,task_id,operation_id,actor_id,state,phase,summary,next_action,checkpoint_refs,checkpoint_completed,checkpoint_total,started_at,created_at,generated_comment_id,progress_event_cursor) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, history.id, history.taskID, history.source.OperationID, history.actorID, history.source.State, history.source.Phase, history.source.Summary, history.source.NextAction, refs, nullableIntArg(history.source.CheckpointCompleted), nullableIntArg(history.source.CheckpointTotal), history.source.StartedAt, history.source.CreatedAt, portableStringArg(history.generatedCommentID), progress)
		if err != nil {
			return err
		}
	}
	if err := bumpPortableTaskCollectionRevisions(ctx, tx, plan); err != nil {
		return err
	}
	// CreateProject initializes a counter. Imported task numbers may be sparse,
	// so advance each affected project's counter without ever decreasing it.
	for _, project := range plan.projects {
		var next int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(number),0)+1 FROM tasks WHERE project_id=?`, project.id).Scan(&next); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO project_counters(project_id,next_number) VALUES (?,?) ON CONFLICT(project_id) DO UPDATE SET next_number=CASE WHEN excluded.next_number>project_counters.next_number THEN excluded.next_number ELSE project_counters.next_number END`, project.id, next); err != nil {
			return err
		}
	}
	return nil
}
