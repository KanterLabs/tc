package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	TaskContextCandidateLimit = 500
	TaskContextItemLimit      = 24
	TaskContextTextBudget     = 24_000
	taskContextDescriptionMax = 1_200
)

type TaskContextProject struct {
	ID             string `json:"id"`
	Key            string `json:"key"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	OpenTaskCount  int    `json:"open_task_count"`
	CompletedCount int    `json:"completed_task_count"`
}

type TaskContextReference struct {
	ID                string   `json:"id"`
	Key               string   `json:"key"`
	Title             string   `json:"title"`
	Description       string   `json:"description"`
	Priority          string   `json:"priority"`
	SemanticState     string   `json:"semantic_state"`
	Labels            []string `json:"labels"`
	PrerequisiteCount int      `json:"prerequisite_count"`
	DependentCount    int      `json:"dependent_count"`
	CompletedAt       *string  `json:"completed_at"`
	UpdatedAt         string   `json:"updated_at"`
}

type TaskDraftContext struct {
	Project        TaskContextProject     `json:"project"`
	CompletedTasks []TaskContextReference `json:"completed_tasks"`
	OpenTasks      []TaskContextReference `json:"open_tasks"`
	CandidateCount int                    `json:"candidate_count"`
	Truncated      bool                   `json:"truncated"`
}

type taskContextCandidate struct {
	ref   TaskContextReference
	score int
}

// TaskDraftContext returns a bounded, deterministic context pack. Historical
// task text is data for the caller's model prompt; this method never executes
// it or crosses the requested project boundary.
func (s *Store) TaskDraftContext(ctx context.Context, projectID, roughQuery string) (TaskDraftContext, error) {
	project, err := s.GetProject(ctx, projectID)
	if err != nil {
		return TaskDraftContext{}, err
	}
	result := TaskDraftContext{
		Project: TaskContextProject{
			ID: project.ID, Key: project.Key, Name: project.Name,
			Description: project.Description, OpenTaskCount: project.OpenTaskCount,
			CompletedCount: project.CompletedTaskCount,
		},
		CompletedTasks: []TaskContextReference{},
		OpenTasks:      []TaskContextReference{},
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM tasks WHERE project_id=? AND deleted_at IS NULL`, project.ID).Scan(&result.CandidateCount); err != nil {
		return TaskDraftContext{}, err
	}
	rows, err := s.DB.QueryContext(ctx, `
		SELECT t.id, t.number, t.title, t.description, t.priority, c.semantic_state,
		       t.completed_at, t.updated_at,
		       COALESCE((SELECT group_concat(l.name, char(31)) FROM task_labels tl JOIN labels l ON l.id=tl.label_id WHERE tl.task_id=t.id), ''),
		       (SELECT COUNT(1) FROM task_dependencies td JOIN tasks p ON p.id=td.prerequisite_task_id WHERE td.task_id=t.id AND p.deleted_at IS NULL AND p.project_id=t.project_id),
		       (SELECT COUNT(1) FROM task_dependencies td JOIN tasks d ON d.id=td.task_id WHERE td.prerequisite_task_id=t.id AND d.deleted_at IS NULL AND d.project_id=t.project_id)
		FROM tasks t
		JOIN columns c ON c.id=t.column_id AND c.project_id=t.project_id
		WHERE t.project_id=? AND t.deleted_at IS NULL
		ORDER BY t.updated_at DESC, t.number DESC, t.id
		LIMIT ?`, project.ID, TaskContextCandidateLimit)
	if err != nil {
		return TaskDraftContext{}, err
	}
	defer rows.Close()
	tokens := taskContextTokens(roughQuery)
	candidates := make([]taskContextCandidate, 0, min(result.CandidateCount, TaskContextCandidateLimit))
	for index := 0; rows.Next(); index++ {
		var ref TaskContextReference
		var completedAt sql.NullString
		var labels string
		if err := rows.Scan(&ref.ID, &refKeyNumber{project.Key, &ref.Key}, &ref.Title, &ref.Description, &ref.Priority, &ref.SemanticState, &completedAt, &ref.UpdatedAt, &labels, &ref.PrerequisiteCount, &ref.DependentCount); err != nil {
			return TaskDraftContext{}, err
		}
		if completedAt.Valid {
			value := completedAt.String
			ref.CompletedAt = &value
		}
		if labels == "" {
			ref.Labels = []string{}
		} else {
			ref.Labels = strings.Split(labels, string(rune(31)))
			sort.Slice(ref.Labels, func(i, j int) bool { return strings.ToLower(ref.Labels[i]) < strings.ToLower(ref.Labels[j]) })
		}
		ref.Description = truncateUTF8(ref.Description, taskContextDescriptionMax)
		candidates = append(candidates, taskContextCandidate{ref: ref, score: taskContextScore(ref, tokens, index)})
	}
	if err := rows.Err(); err != nil {
		return TaskDraftContext{}, err
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		if candidates[i].ref.UpdatedAt != candidates[j].ref.UpdatedAt {
			return candidates[i].ref.UpdatedAt > candidates[j].ref.UpdatedAt
		}
		return candidates[i].ref.Key > candidates[j].ref.Key
	})
	remaining := TaskContextTextBudget - len(result.Project.Name) - len(result.Project.Description)
	for _, candidate := range candidates {
		if len(result.CompletedTasks)+len(result.OpenTasks) >= TaskContextItemLimit || remaining <= 0 {
			break
		}
		ref := candidate.ref
		cost := len(ref.Title) + len(ref.Description) + len(strings.Join(ref.Labels, ","))
		if cost > remaining {
			ref.Description = truncateUTF8(ref.Description, max(0, remaining-len(ref.Title)-len(strings.Join(ref.Labels, ","))))
			cost = len(ref.Title) + len(ref.Description) + len(strings.Join(ref.Labels, ","))
		}
		if cost > remaining {
			continue
		}
		remaining -= cost
		if ref.SemanticState == "completed" && ref.CompletedAt != nil {
			if len(result.CompletedTasks) < TaskContextItemLimit/2 {
				result.CompletedTasks = append(result.CompletedTasks, ref)
			}
		} else if len(result.OpenTasks) < TaskContextItemLimit/2 {
			result.OpenTasks = append(result.OpenTasks, ref)
		}
	}
	result.Truncated = result.CandidateCount > len(result.CompletedTasks)+len(result.OpenTasks)
	return result, nil
}

// refKeyNumber scans a task number and formats its project-local key without
// adding another database column to the bounded context query.
type refKeyNumber struct {
	projectKey string
	target     *string
}

func (r *refKeyNumber) Scan(value any) error {
	var number int64
	switch typed := value.(type) {
	case int64:
		number = typed
	case int:
		number = int64(typed)
	default:
		return fmt.Errorf("invalid task number type %T", value)
	}
	*r.target = fmt.Sprintf("%s-%d", r.projectKey, number)
	return nil
}

func taskContextTokens(value string) []string {
	parts := strings.FieldsFunc(strings.ToLower(value), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	seen := make(map[string]struct{})
	result := make([]string, 0, min(len(parts), 32))
	for _, part := range parts {
		if len(part) < 2 {
			continue
		}
		if _, exists := seen[part]; exists {
			continue
		}
		seen[part] = struct{}{}
		result = append(result, part)
		if len(result) == 32 {
			break
		}
	}
	return result
}

func taskContextScore(ref TaskContextReference, tokens []string, recencyIndex int) int {
	title := strings.ToLower(ref.Title)
	description := strings.ToLower(ref.Description)
	labels := strings.ToLower(strings.Join(ref.Labels, " "))
	score := max(0, 50-recencyIndex/10) + min(20, ref.PrerequisiteCount+ref.DependentCount)*2
	for _, token := range tokens {
		if strings.Contains(title, token) {
			score += 20
		}
		if strings.Contains(labels, token) {
			score += 10
		}
		if strings.Contains(description, token) {
			score += 4
		}
	}
	return score
}

func truncateUTF8(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
