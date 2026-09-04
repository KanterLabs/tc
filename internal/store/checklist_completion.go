package store

import (
	"context"
	"database/sql"
)

// checklistCompletionStatus is computed inside the caller's write
// transaction. Keeping policy evaluation here makes every completion path
// use the same error envelope and warning metadata.
type checklistCompletionStatus struct {
	Policy    string
	OpenItems int
	Warning   bool
}

func checklistPolicyTx(ctx context.Context, tx *sql.Tx, projectID string) (string, error) {
	policy := "warn"
	if err := tx.QueryRowContext(ctx, `SELECT checklist_completion_policy FROM projects WHERE id=?`, projectID).Scan(&policy); err != nil {
		return "", err
	}
	if policy != "require" {
		policy = "warn"
	}
	return policy, nil
}

func checklistCompletionStatusForTaskTx(ctx context.Context, tx *sql.Tx, projectID, taskID string) (checklistCompletionStatus, error) {
	policy, err := checklistPolicyTx(ctx, tx, projectID)
	if err != nil {
		return checklistCompletionStatus{}, err
	}
	var openItems int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM task_checklist_items WHERE task_id=? AND completed=0`, taskID).Scan(&openItems); err != nil {
		return checklistCompletionStatus{}, err
	}
	return checklistCompletionStatus{
		Policy:    policy,
		OpenItems: openItems,
		Warning:   policy == "warn" && openItems > 0,
	}, nil
}

func checklistCompletionStatusForColumnTx(ctx context.Context, tx *sql.Tx, projectID, columnID string) (checklistCompletionStatus, error) {
	policy, err := checklistPolicyTx(ctx, tx, projectID)
	if err != nil {
		return checklistCompletionStatus{}, err
	}
	var openItems int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM task_checklist_items i JOIN tasks t ON t.id=i.task_id WHERE t.project_id=? AND t.column_id=? AND t.deleted_at IS NULL AND i.completed=0`, projectID, columnID).Scan(&openItems); err != nil {
		return checklistCompletionStatus{}, err
	}
	return checklistCompletionStatus{
		Policy:    policy,
		OpenItems: openItems,
		Warning:   policy == "warn" && openItems > 0,
	}, nil
}

func rejectIncompleteChecklist(status checklistCompletionStatus) error {
	if status.Policy != "require" || status.OpenItems == 0 {
		return nil
	}
	return &Error{
		Kind:    ErrChecklistIncomplete,
		Message: "complete all checklist items before completing the task",
		Details: map[string]any{"open_items": status.OpenItems},
	}
}

func addChecklistCompletionEventFields(payload map[string]any, status checklistCompletionStatus) {
	if status.OpenItems == 0 {
		return
	}
	payload["checklist_warning"] = status.Warning
	payload["open_checklist_items"] = status.OpenItems
	payload["checklist_completion_policy"] = status.Policy
}
