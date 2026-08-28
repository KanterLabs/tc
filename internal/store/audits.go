package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// AuditRun is a durable board-audit execution. Findings are fetched through
// the bounded, paginated finding collection so run metadata stays small.
type AuditRun struct {
	ID           string         `json:"id"`
	ProjectID    string         `json:"project_id"`
	ActorID      string         `json:"actor_id"`
	Scope        string         `json:"scope"`
	Status       string         `json:"status"`
	StartedAt    string         `json:"started_at"`
	FinalizedAt  *string        `json:"finalized_at,omitempty"`
	CreatedAt    string         `json:"created_at"`
	UpdatedAt    string         `json:"updated_at"`
	FindingCount int            `json:"finding_count"`
	Findings     []AuditFinding `json:"findings"`
}

// AuditFinding is an immutable observation of one task at a point in an
// audit. ChangedSinceAudit is derived on every read from the current task
// version and source column ID; it is never persisted, so the audit remains a
// true snapshot even as normal task work continues.
type AuditFinding struct {
	ID                          string   `json:"id"`
	AuditID                     string   `json:"audit_id"`
	TaskID                      string   `json:"task_id"`
	CapturedVersion             int64    `json:"captured_version"`
	SourceColumn                string   `json:"source_column"`
	Verdict                     string   `json:"verdict"`
	ProposedSemanticDestination *string  `json:"proposed_semantic_destination,omitempty"`
	Confidence                  float64  `json:"confidence"`
	Reason                      string   `json:"reason"`
	EvidenceRefs                []string `json:"evidence_refs"`
	ReviewState                 string   `json:"review_state"`
	Version                     int64    `json:"version"`
	ChangedSinceAudit           bool     `json:"changed_since_audit"`
	CreatedAt                   string   `json:"created_at"`
	UpdatedAt                   string   `json:"updated_at"`
	// SourceColumnID is a source-compatibility alias for callers that use the
	// task table's column_id terminology. The wire field remains source_column.
	SourceColumnID string `json:"-"`
}

// AuditRunInput is the create shape for an audit run. Scope is an opaque,
// bounded label (for example, "board" or "open_tasks") so deployments can
// evolve audit selection semantics without a schema migration.
type AuditRunInput struct {
	Scope string
	// Status may be queued when an audit request is accepted for asynchronous
	// processing. Empty retains the synchronous running default.
	Status string
}

// AuditFindingInput is the append shape for one immutable finding. Source
// column is the exact opaque columns.id captured by the auditor.
type AuditFindingInput struct {
	TaskID          string
	CapturedVersion int64
	SourceColumn    string
	// SourceColumnID is accepted as an alias for SourceColumn by direct store
	// callers and the HTTP adapter. It is not separately persisted.
	SourceColumnID              string
	Verdict                     string
	ProposedSemanticDestination *string
	Confidence                  float64
	Reason                      string
	EvidenceRefs                []string
	ReviewState                 string
}

// AuditFindingReviewInput contains the only mutable finding fields. The
// snapshot (task, captured version, source column, verdict, confidence,
// reason, and evidence) remains immutable after append.
type AuditFindingReviewInput struct {
	ReviewState                    string
	ProposedSemanticDestination    *string
	ProposedSemanticDestinationSet bool
}

type auditTaskSnapshot struct {
	Version       int64
	ColumnID      string
	ColumnName    string
	SemanticState string
}

const (
	maxAuditScope          = 200
	maxAuditSourceColumn   = 200
	maxAuditReason         = 2000
	maxAuditEvidenceRefs   = 100
	maxAuditEvidenceRef    = 512
	maxAuditEvidenceJSON   = 16384
	maxAuditReviewState    = 32
	maxAuditRunFindings    = 10000
	maxAuditRunsPerProject = 1000
)

var auditSafeRefPattern = regexp.MustCompile(`^[A-Za-z0-9/][A-Za-z0-9._:/+~-]*$`)

func validAuditReviewState(value string) bool {
	switch value {
	case "pending", "approved", "dismissed":
		return true
	default:
		return false
	}
}

func validateAuditRunInput(input AuditRunInput) (AuditRunInput, error) {
	input.Scope = strings.TrimSpace(input.Scope)
	if input.Scope == "" {
		return AuditRunInput{}, invalid("audit scope is required", nil)
	}
	if utf8.RuneCountInString(input.Scope) > maxAuditScope || strings.IndexFunc(input.Scope, unicode.IsControl) >= 0 {
		return AuditRunInput{}, invalid("audit scope must be between 1 and 200 safe characters", nil)
	}
	input.Status = strings.TrimSpace(input.Status)
	if input.Status == "" {
		input.Status = "running"
	}
	if input.Status != "queued" && input.Status != "running" {
		return AuditRunInput{}, invalid("audit status must be queued or running when creating a run", nil)
	}
	return input, nil
}

func validateAuditFindingInput(input AuditFindingInput) (AuditFindingInput, error) {
	input.TaskID = strings.TrimSpace(input.TaskID)
	if input.TaskID == "" {
		return AuditFindingInput{}, invalid("task_id is required", nil)
	}
	if input.CapturedVersion <= 0 {
		return AuditFindingInput{}, invalid("captured_version must be a positive integer", nil)
	}
	if strings.TrimSpace(input.SourceColumn) == "" {
		input.SourceColumn = strings.TrimSpace(input.SourceColumnID)
	}
	input.SourceColumn = strings.TrimSpace(input.SourceColumn)
	if input.SourceColumn == "" {
		return AuditFindingInput{}, invalid("source_column is required", nil)
	}
	if utf8.RuneCountInString(input.SourceColumn) > maxAuditSourceColumn || strings.IndexFunc(input.SourceColumn, unicode.IsControl) >= 0 {
		return AuditFindingInput{}, invalid("source_column must be between 1 and 200 characters", nil)
	}
	if input.SourceColumnID != "" && strings.TrimSpace(input.SourceColumnID) != input.SourceColumn {
		return AuditFindingInput{}, invalid("source_column and source_column_id must match", nil)
	}
	input.SourceColumnID = input.SourceColumn

	switch input.Verdict {
	case "correct", "needs_attention", "move_proposed":
	default:
		return AuditFindingInput{}, invalid("verdict must be correct, needs_attention, or move_proposed", nil)
	}
	if input.ProposedSemanticDestination != nil {
		value := strings.ToLower(strings.TrimSpace(*input.ProposedSemanticDestination))
		if !validState(value) {
			return AuditFindingInput{}, invalid("proposed_semantic_destination is invalid", nil)
		}
		input.ProposedSemanticDestination = &value
	}
	if input.Verdict == "move_proposed" && input.ProposedSemanticDestination == nil {
		return AuditFindingInput{}, invalid("proposed_semantic_destination is required for move_proposed findings", nil)
	}
	if input.Verdict != "move_proposed" && input.ProposedSemanticDestination != nil {
		return AuditFindingInput{}, invalid("proposed_semantic_destination is only valid for move_proposed findings", nil)
	}
	if math.IsNaN(input.Confidence) || math.IsInf(input.Confidence, 0) || input.Confidence < 0 || input.Confidence > 1 {
		return AuditFindingInput{}, invalid("confidence must be between 0 and 1", nil)
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if input.Reason == "" {
		return AuditFindingInput{}, invalid("reason is required", nil)
	}
	if utf8.RuneCountInString(input.Reason) > maxAuditReason || strings.IndexFunc(input.Reason, unicode.IsControl) >= 0 {
		return AuditFindingInput{}, invalid("reason must be between 1 and 2000 characters", nil)
	}
	refs, err := normalizeAuditEvidenceRefs(input.EvidenceRefs)
	if err != nil {
		return AuditFindingInput{}, err
	}
	input.EvidenceRefs = refs
	input.ReviewState = strings.TrimSpace(input.ReviewState)
	if input.ReviewState == "" {
		input.ReviewState = "pending"
	}
	if utf8.RuneCountInString(input.ReviewState) > maxAuditReviewState || input.ReviewState != "pending" {
		return AuditFindingInput{}, invalid("review_state must be pending when appending a finding", nil)
	}
	return input, nil
}

func normalizeAuditEvidenceRefs(refs []string) ([]string, error) {
	if refs == nil {
		return []string{}, nil
	}
	if len(refs) > maxAuditEvidenceRefs {
		return nil, invalid("evidence_refs must contain at most 100 items", nil)
	}
	result := make([]string, 0, len(refs))
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if utf8.RuneCountInString(ref) == 0 || utf8.RuneCountInString(ref) > maxAuditEvidenceRef || !auditSafeRefPattern.MatchString(ref) {
			return nil, invalid("evidence_refs items must be safe references of at most 512 characters", nil)
		}
		lower := strings.ToLower(ref)
		if strings.HasPrefix(lower, "javascript:") || strings.HasPrefix(lower, "data:") {
			return nil, invalid("evidence_refs cannot contain executable or data URLs", nil)
		}
		if _, ok := seen[ref]; ok {
			return nil, invalid("evidence_refs must not contain duplicate items", nil)
		}
		seen[ref] = struct{}{}
		result = append(result, ref)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxAuditEvidenceJSON {
		return nil, invalid("evidence_refs payload is too large", nil)
	}
	return result, nil
}

func auditRunFromRow(scanner interface{ Scan(...any) error }) (AuditRun, error) {
	var run AuditRun
	var finalized sql.NullString
	if err := scanner.Scan(&run.ID, &run.ProjectID, &run.ActorID, &run.Scope, &run.Status, &run.StartedAt, &finalized, &run.CreatedAt, &run.UpdatedAt, &run.FindingCount); err != nil {
		return AuditRun{}, err
	}
	run.FinalizedAt = nullableString(finalized)
	run.Findings = []AuditFinding{}
	return run, nil
}

const auditRunColumns = `ar.id, ar.project_id, ar.actor_id, ar.scope, ar.status,
	ar.started_at, ar.finalized_at, ar.created_at, ar.updated_at,
	(SELECT COUNT(1) FROM audit_findings afc WHERE afc.audit_id = ar.id)`

func auditFindingFromRow(scanner interface{ Scan(...any) error }) (AuditFinding, error) {
	var finding AuditFinding
	var destination sql.NullString
	var refsJSON string
	if err := scanner.Scan(&finding.ID, &finding.AuditID, &finding.TaskID, &finding.CapturedVersion, &finding.SourceColumn, &finding.Verdict, &destination, &finding.Confidence, &finding.Reason, &refsJSON, &finding.ReviewState, &finding.Version, &finding.CreatedAt, &finding.UpdatedAt); err != nil {
		return AuditFinding{}, err
	}
	if destination.Valid {
		finding.ProposedSemanticDestination = &destination.String
	}
	if err := json.Unmarshal([]byte(refsJSON), &finding.EvidenceRefs); err != nil {
		return AuditFinding{}, fmt.Errorf("decode audit evidence_refs: %w", err)
	}
	if finding.EvidenceRefs == nil {
		finding.EvidenceRefs = []string{}
	}
	finding.SourceColumnID = finding.SourceColumn
	return finding, nil
}

const auditFindingColumns = `af.id, af.audit_id, af.task_id, af.captured_version,
	af.source_column, af.verdict, af.proposed_semantic_destination, af.confidence,
	af.reason, af.evidence_refs, af.review_state, af.version, af.created_at, af.updated_at`

// CreateAuditRun starts an audit without emitting an event or changing any
// task-side state. The actor and project are checked before insertion so a
// dangling audit cannot be created by a direct store caller.
func (s *Store) CreateAuditRun(ctx context.Context, projectID, actorID string, input AuditRunInput) (AuditRun, error) {
	validated, err := validateAuditRunInput(input)
	if err != nil {
		return AuditRun{}, err
	}
	if _, err := s.GetProject(ctx, projectID); err != nil {
		return AuditRun{}, err
	}
	if _, err := s.GetActor(ctx, actorID); err != nil {
		return AuditRun{}, err
	}
	id, timestamp := newID(), now()
	if err := s.withTx(ctx, func(tx *sql.Tx) error {
		// Acquire the writer lock before counting so concurrent creators cannot
		// cross the retained-run ceiling. Refuse rather than silently delete
		// historical audits; cleanup remains an explicit operator/user action.
		if _, err := tx.ExecContext(ctx, `UPDATE projects SET updated_at=updated_at WHERE id=?`, projectID); err != nil {
			return err
		}
		var retained int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM audit_runs WHERE project_id=?`, projectID).Scan(&retained); err != nil {
			return err
		}
		if retained >= maxAuditRunsPerProject {
			return invalid("project has reached the retained audit run limit", map[string]any{"limit": maxAuditRunsPerProject})
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO audit_runs(id, project_id, actor_id, scope, status, started_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, id, projectID, actorID, validated.Scope, validated.Status, timestamp, timestamp, timestamp)
		return err
	}); err != nil {
		return AuditRun{}, err
	}
	return s.getAuditRunSummary(ctx, id)
}

// GetAuditRun returns bounded run metadata. Consumers fetch findings through
// ListAuditFindings so a detail request can never expand to an unbounded body.
func (s *Store) GetAuditRun(ctx context.Context, id string) (AuditRun, error) {
	return s.getAuditRunSummary(ctx, id)
}

func (s *Store) getAuditRunSummary(ctx context.Context, id string) (AuditRun, error) {
	row := s.DB.QueryRowContext(ctx, `SELECT `+auditRunColumns+` FROM audit_runs ar WHERE ar.id=?`, id)
	run, err := auditRunFromRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return AuditRun{}, notFound("audit run not found")
	}
	return run, err
}

// ListAuditRuns returns one bounded page for a project. Use
// ListAuditRunsForProjects for a token-scoped global collection.
func (s *Store) ListAuditRuns(ctx context.Context, projectID string, limit, offset int) ([]AuditRun, bool, error) {
	return s.listAuditRuns(ctx, []string{projectID}, false, limit, offset)
}

// ListAuditRunsForProjects lists runs across an explicit project ceiling. A
// nil projectIDs slice means unscoped/all projects; a non-nil empty slice
// intentionally matches no rows.
func (s *Store) ListAuditRunsForProjects(ctx context.Context, projectIDs []string, limit, offset int) ([]AuditRun, bool, error) {
	return s.listAuditRuns(ctx, projectIDs, true, limit, offset)
}

// ListAudits is a compatibility alias for callers using the shorter resource
// name. It retains the same project-scoped pagination semantics.
func (s *Store) ListAudits(ctx context.Context, projectID string, limit, offset int) ([]AuditRun, bool, error) {
	return s.ListAuditRuns(ctx, projectID, limit, offset)
}

func (s *Store) listAuditRuns(ctx context.Context, projectIDs []string, allowAll bool, limit, offset int) ([]AuditRun, bool, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	query := `SELECT ` + auditRunColumns + ` FROM audit_runs ar`
	args := make([]any, 0, len(projectIDs)+2)
	if !allowAll || projectIDs != nil {
		if len(projectIDs) == 0 {
			query += ` WHERE 1=0`
		} else {
			placeholders := make([]string, len(projectIDs))
			for i, projectID := range projectIDs {
				placeholders[i] = "?"
				args = append(args, projectID)
			}
			query += ` WHERE ar.project_id IN (` + strings.Join(placeholders, ",") + `)`
		}
	}
	query += ` ORDER BY ar.started_at DESC, ar.id DESC LIMIT ? OFFSET ?`
	args = append(args, limit+1, offset)
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	runs := make([]AuditRun, 0, limit)
	for rows.Next() {
		run, err := auditRunFromRow(rows)
		if err != nil {
			return nil, false, err
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(runs) > limit
	if more {
		runs = runs[:limit]
	}
	return runs, more, nil
}

// AppendAuditFinding appends one immutable finding while the run is queued or
// running.
// Repeating the exact finding for the same task is idempotent; changing any
// snapshot or review field for an existing task returns a conflict. The task
// itself is never updated, and no event/comment/claim row is written.
func (s *Store) AppendAuditFinding(ctx context.Context, auditID string, input AuditFindingInput) (AuditFinding, error) {
	validated, err := validateAuditFindingInput(input)
	if err != nil {
		return AuditFinding{}, err
	}
	var findingID string
	var existing bool
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		// This guarded no-op UPDATE acquires SQLite's writer lock before checking
		// status, preventing a concurrent finalize from accepting a late finding.
		result, err := tx.ExecContext(ctx, `UPDATE audit_runs SET updated_at=updated_at WHERE id=? AND status IN ('queued', 'running')`, auditID)
		if err != nil {
			return err
		}
		if changed, rowsErr := result.RowsAffected(); rowsErr != nil {
			return rowsErr
		} else if changed == 0 {
			var status string
			err = tx.QueryRowContext(ctx, `SELECT status FROM audit_runs WHERE id=?`, auditID).Scan(&status)
			if errors.Is(err, sql.ErrNoRows) {
				return notFound("audit run not found")
			}
			if err != nil {
				return err
			}
			return conflict("audit run is finalized", nil)
		}

		var existingFinding AuditFinding
		existingFinding, err = auditFindingFromRow(tx.QueryRowContext(ctx, `SELECT `+auditFindingColumns+` FROM audit_findings af WHERE af.audit_id=? AND af.task_id=?`, auditID, validated.TaskID))
		if err == nil {
			if !auditFindingMatchesInput(existingFinding, validated) {
				return conflict("audit finding is immutable", map[string]any{"finding_id": existingFinding.ID})
			}
			findingID, existing = existingFinding.ID, true
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		var projectID string
		if err := tx.QueryRowContext(ctx, `SELECT project_id FROM audit_runs WHERE id=?`, auditID).Scan(&projectID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return notFound("audit run not found")
			}
			return err
		}
		var snapshot auditTaskSnapshot
		err = tx.QueryRowContext(ctx, `SELECT t.version, t.column_id, c.name, c.semantic_state FROM tasks t JOIN columns c ON c.id=t.column_id WHERE t.id=? AND t.project_id=? AND t.deleted_at IS NULL`, validated.TaskID, projectID).Scan(&snapshot.Version, &snapshot.ColumnID, &snapshot.ColumnName, &snapshot.SemanticState)
		if errors.Is(err, sql.ErrNoRows) {
			return notFound("task not found in audit project")
		}
		if err != nil {
			return err
		}
		if validated.CapturedVersion > snapshot.Version {
			return invalid("captured_version cannot be newer than the task", nil)
		}
		// Source columns are opaque IDs. A finding may legitimately be stale
		// because the task moved after capture, but the captured ID must have
		// belonged to this project when appended.
		if !auditSourceColumnMatches(validated.SourceColumn, snapshot) {
			var count int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM columns WHERE project_id=? AND id=?`, projectID, validated.SourceColumn).Scan(&count); err != nil {
				return err
			}
			if count == 0 {
				return invalid("source_column must be a column ID in the audit project", nil)
			}
		}

		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM audit_findings WHERE audit_id=?`, auditID).Scan(&count); err != nil {
			return err
		}
		if count >= maxAuditRunFindings {
			return invalid("audit run contains too many findings", nil)
		}
		refsJSON, err := json.Marshal(validated.EvidenceRefs)
		if err != nil {
			return err
		}
		findingID = newID()
		timestamp := now()
		_, err = tx.ExecContext(ctx, `INSERT INTO audit_findings(id, audit_id, task_id, captured_version, source_column, verdict, proposed_semantic_destination, confidence, reason, evidence_refs, review_state, version, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`, findingID, auditID, validated.TaskID, validated.CapturedVersion, validated.SourceColumn, validated.Verdict, nullableAuditDestinationArg(validated.ProposedSemanticDestination), validated.Confidence, validated.Reason, string(refsJSON), validated.ReviewState, timestamp, timestamp)
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				return conflict("audit finding already exists", nil)
			}
			return err
		}
		return nil
	})
	if err != nil {
		return AuditFinding{}, err
	}
	if existing {
		return s.getAuditFinding(ctx, auditID, findingID)
	}
	return s.getAuditFinding(ctx, auditID, findingID)
}

func nullableAuditDestinationArg(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func auditFindingMatchesInput(finding AuditFinding, input AuditFindingInput) bool {
	if finding.TaskID != input.TaskID || finding.CapturedVersion != input.CapturedVersion || finding.SourceColumn != input.SourceColumn || finding.Verdict != input.Verdict || finding.Confidence != input.Confidence || finding.Reason != input.Reason || finding.ReviewState != input.ReviewState {
		return false
	}
	if (finding.ProposedSemanticDestination == nil) != (input.ProposedSemanticDestination == nil) {
		return false
	}
	if finding.ProposedSemanticDestination != nil && *finding.ProposedSemanticDestination != *input.ProposedSemanticDestination {
		return false
	}
	if len(finding.EvidenceRefs) != len(input.EvidenceRefs) {
		return false
	}
	for i := range finding.EvidenceRefs {
		if finding.EvidenceRefs[i] != input.EvidenceRefs[i] {
			return false
		}
	}
	return true
}

func (s *Store) getAuditFinding(ctx context.Context, auditID, findingID string) (AuditFinding, error) {
	row := s.DB.QueryRowContext(ctx, `SELECT `+auditFindingColumns+` FROM audit_findings af WHERE af.audit_id=? AND af.id=?`, auditID, findingID)
	finding, err := auditFindingFromRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return AuditFinding{}, notFound("audit finding not found")
	}
	if err != nil {
		return AuditFinding{}, err
	}
	if err := s.populateAuditFindingDrift(ctx, &finding); err != nil {
		return AuditFinding{}, err
	}
	return finding, nil
}

// GetAuditFinding fetches a finding by its opaque ID and calculates drift at
// read time. The associated run/project are intentionally available to HTTP
// callers through GetAuditFindingProject so a token ceiling can be enforced
// before exposing the finding.
func (s *Store) GetAuditFinding(ctx context.Context, findingID string) (AuditFinding, error) {
	row := s.DB.QueryRowContext(ctx, `SELECT `+auditFindingColumns+` FROM audit_findings af WHERE af.id=?`, findingID)
	finding, err := auditFindingFromRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return AuditFinding{}, notFound("audit finding not found")
	}
	if err != nil {
		return AuditFinding{}, err
	}
	if err := s.populateAuditFindingDrift(ctx, &finding); err != nil {
		return AuditFinding{}, err
	}
	return finding, nil
}

// GetAuditFindingProject returns the project owning a finding's run. It is a
// narrow authorization helper and does not expose task metadata.
func (s *Store) GetAuditFindingProject(ctx context.Context, findingID string) (string, error) {
	var projectID string
	err := s.DB.QueryRowContext(ctx, `SELECT ar.project_id FROM audit_findings af JOIN audit_runs ar ON ar.id=af.audit_id WHERE af.id=?`, findingID).Scan(&projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", notFound("audit finding not found")
	}
	return projectID, err
}

func validateAuditFindingReviewInput(input AuditFindingReviewInput) (AuditFindingReviewInput, error) {
	input.ReviewState = strings.TrimSpace(input.ReviewState)
	if !validAuditReviewState(input.ReviewState) {
		return AuditFindingReviewInput{}, invalid("review_state must be pending, approved, or dismissed", nil)
	}
	if input.ProposedSemanticDestination != nil {
		// Direct store callers do not have JSON omission metadata. A non-nil
		// pointer therefore means the destination was intentionally supplied.
		input.ProposedSemanticDestinationSet = true
	}
	if input.ProposedSemanticDestinationSet {
		if input.ProposedSemanticDestination != nil {
			value := strings.ToLower(strings.TrimSpace(*input.ProposedSemanticDestination))
			if !validState(value) {
				return AuditFindingReviewInput{}, invalid("proposed_semantic_destination is invalid", nil)
			}
			input.ProposedSemanticDestination = &value
		}
	}
	return input, nil
}

// UpdateAuditFindingReview changes only review_state and, when explicitly
// supplied, the proposed destination. It uses the finding version as a
// strong optimistic-concurrency validator and does not alter the captured
// task snapshot or any task-side table.
func (s *Store) UpdateAuditFindingReview(ctx context.Context, findingID string, input AuditFindingReviewInput, expectedVersion int64, _ ...string) (AuditFinding, error) {
	if expectedVersion <= 0 {
		return AuditFinding{}, ErrPrecondition
	}
	validated, err := validateAuditFindingReviewInput(input)
	if err != nil {
		return AuditFinding{}, err
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		var currentDestination sql.NullString
		var currentVersion int64
		if err := tx.QueryRowContext(ctx, `SELECT version, proposed_semantic_destination FROM audit_findings WHERE id=?`, findingID).Scan(&currentVersion, &currentDestination); errors.Is(err, sql.ErrNoRows) {
			return notFound("audit finding not found")
		} else if err != nil {
			return err
		}
		destination := any(nil)
		if validated.ProposedSemanticDestinationSet {
			destination = nullableAuditDestinationArg(validated.ProposedSemanticDestination)
		} else if currentDestination.Valid {
			destination = currentDestination.String
		}
		query := `UPDATE audit_findings SET review_state=?, proposed_semantic_destination=?, version=version+1, updated_at=? WHERE id=? AND version=?`
		if validated.ReviewState == "approved" {
			// Approval is a selection boundary for later reconciliation. The agent
			// must first finish the run, and the task snapshot must still match.
			// Guard both facts in the same writer statement so a queued/in-flight
			// or stale finding can never become newly approved.
			query += ` AND EXISTS (
				SELECT 1 FROM audit_runs ar
				WHERE ar.id=audit_findings.audit_id
					AND ar.status IN ('complete', 'partial')
			) AND EXISTS (
				SELECT 1 FROM tasks t
				WHERE t.id=audit_findings.task_id
					AND t.deleted_at IS NULL
					AND t.version=audit_findings.captured_version
					AND t.column_id=audit_findings.source_column
			)`
		}
		result, err := tx.ExecContext(ctx, query, validated.ReviewState, destination, now(), findingID, expectedVersion)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 0 {
			return nil
		}
		latest, err := auditFindingFromRow(tx.QueryRowContext(ctx, `SELECT `+auditFindingColumns+` FROM audit_findings af WHERE af.id=?`, findingID))
		if errors.Is(err, sql.ErrNoRows) {
			return notFound("audit finding not found")
		}
		if err != nil {
			return err
		}
		if latest.Version == expectedVersion && validated.ReviewState == "approved" {
			var runStatus string
			if err := tx.QueryRowContext(ctx, `SELECT status FROM audit_runs WHERE id=?`, latest.AuditID).Scan(&runStatus); err != nil {
				return err
			}
			if runStatus != "complete" && runStatus != "partial" {
				return conflict("audit run must be complete or partial before approval", map[string]any{"current": latest})
			}
			latest.ChangedSinceAudit = true
			return conflict("audit finding task has changed since capture", map[string]any{"current": latest})
		}
		return conflict("audit finding has changed", map[string]any{"current": latest})
	})
	if err != nil {
		return AuditFinding{}, err
	}
	return s.GetAuditFinding(ctx, findingID)
}

// ReviewAuditFinding is a compatibility alias for callers that use the
// action-oriented name.
func (s *Store) ReviewAuditFinding(ctx context.Context, findingID string, input AuditFindingReviewInput, expectedVersion int64, actorID ...string) (AuditFinding, error) {
	return s.UpdateAuditFindingReview(ctx, findingID, input, expectedVersion, actorID...)
}

// ListAuditFindings returns one cursor page and whether another row follows.
func (s *Store) ListAuditFindings(ctx context.Context, auditID string, limit, offset int) ([]AuditFinding, bool, error) {
	if _, err := s.getAuditRunSummary(ctx, auditID); err != nil {
		return nil, false, err
	}
	return s.listAuditFindings(ctx, auditID, offset, limit, true)
}

func (s *Store) listAuditFindings(ctx context.Context, auditID string, offset, limit int, sentinel bool) ([]AuditFinding, bool, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	fetch := limit
	if sentinel {
		fetch++
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT `+auditFindingColumns+` FROM audit_findings af WHERE af.audit_id=? ORDER BY af.created_at, af.id LIMIT ? OFFSET ?`, auditID, fetch, offset)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	findings := make([]AuditFinding, 0, limit)
	for rows.Next() {
		finding, err := auditFindingFromRow(rows)
		if err != nil {
			return nil, false, err
		}
		if err := s.populateAuditFindingDrift(ctx, &finding); err != nil {
			return nil, false, err
		}
		findings = append(findings, finding)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := sentinel && len(findings) > limit
	if more {
		findings = findings[:limit]
	}
	return findings, more, nil
}

func (s *Store) populateAuditFindingDrift(ctx context.Context, finding *AuditFinding) error {
	var snapshot auditTaskSnapshot
	err := s.DB.QueryRowContext(ctx, `SELECT t.version, t.column_id, c.name, c.semantic_state FROM tasks t JOIN columns c ON c.id=t.column_id WHERE t.id=?`, finding.TaskID).Scan(&snapshot.Version, &snapshot.ColumnID, &snapshot.ColumnName, &snapshot.SemanticState)
	if errors.Is(err, sql.ErrNoRows) {
		finding.ChangedSinceAudit = true
		return nil
	}
	if err != nil {
		return err
	}
	finding.ChangedSinceAudit = snapshot.Version != finding.CapturedVersion || !auditSourceColumnMatches(finding.SourceColumn, snapshot)
	return nil
}

func auditSourceColumnMatches(source string, snapshot auditTaskSnapshot) bool {
	return source == snapshot.ColumnID
}

func validAuditTerminalStatus(value string) bool {
	switch value {
	case "complete", "partial", "failed":
		return true
	default:
		return false
	}
}

// FinalizeAuditRun closes a run with one of the terminal statuses. The
// variadic form accepts both the current (id, status, actorID) call shape and
// the older (id, actorID) shape, defaulting the latter to complete. It is
// idempotent only when a repeated finalize requests the same terminal status.
func (s *Store) FinalizeAuditRun(ctx context.Context, id string, args ...string) (AuditRun, error) {
	requestedStatus := "complete"
	if len(args) > 0 && validAuditTerminalStatus(strings.TrimSpace(args[0])) {
		requestedStatus = strings.TrimSpace(args[0])
	}
	if !validAuditTerminalStatus(requestedStatus) {
		return AuditRun{}, invalid("audit status must be complete, partial, or failed", nil)
	}
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		timestamp := now()
		result, err := tx.ExecContext(ctx, `UPDATE audit_runs SET status=?, finalized_at=?, updated_at=? WHERE id=? AND status IN ('queued', 'running')`, requestedStatus, timestamp, timestamp, id)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 0 {
			return nil
		}
		var currentStatus string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM audit_runs WHERE id=?`, id).Scan(&currentStatus); errors.Is(err, sql.ErrNoRows) {
			return notFound("audit run not found")
		} else if err != nil {
			return err
		}
		if currentStatus != requestedStatus {
			return conflict("audit run has already been finalized with a different status", nil)
		}
		return nil
	})
	if err != nil {
		return AuditRun{}, err
	}
	return s.getAuditRunSummary(ctx, id)
}

// FinalizeAudit is a compatibility alias for resource-oriented callers.
func (s *Store) FinalizeAudit(ctx context.Context, id string, actorID ...string) (AuditRun, error) {
	return s.FinalizeAuditRun(ctx, id, actorID...)
}
