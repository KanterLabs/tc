package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"roadmap/internal/db"
)

func TestResolveTaskReferenceAcceptsOpaqueIDAndCaseInsensitiveKey(t *testing.T) {
	database, err := db.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	data := New(database)
	project, err := data.CreateProject(ctx, ProjectInput{Key: stringPtrForTest("OPS"), Name: stringPtrForTest("Operations")}, "")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	task, err := data.CreateTask(ctx, project.ID, TaskInput{Title: stringPtrForTest("Ship API")}, "")
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	byID, err := data.ResolveTaskReference(ctx, task.ID)
	if err != nil {
		t.Fatalf("resolve opaque task ID: %v", err)
	}
	if byID.ID != task.ID || byID.Key != "OPS-1" {
		t.Fatalf("resolve by ID = %+v, want ID %q and key OPS-1", byID, task.ID)
	}
	byKey, err := data.ResolveTaskReference(ctx, "ops-1")
	if err != nil {
		t.Fatalf("resolve task key: %v", err)
	}
	if byKey.ID != task.ID {
		t.Fatalf("resolve by key ID = %q, want %q", byKey.ID, task.ID)
	}
	if _, err := data.ResolveTaskReference(ctx, "OPS-999"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown task key error = %v, want ErrNotFound", err)
	}
}

func TestNormalizeTaskClaimDurationEnforcesContract(t *testing.T) {
	tests := []struct {
		name    string
		input   time.Duration
		want    time.Duration
		wantErr bool
	}{
		{name: "default", input: 0, want: DefaultTaskClaimDuration},
		{name: "minimum", input: MinTaskClaimDuration, want: MinTaskClaimDuration},
		{name: "maximum", input: MaxTaskClaimDuration, want: MaxTaskClaimDuration},
		{name: "below minimum", input: MinTaskClaimDuration - time.Second, wantErr: true},
		{name: "above maximum", input: MaxTaskClaimDuration + time.Second, wantErr: true},
		{name: "negative", input: -time.Second, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NormalizeTaskClaimDuration(test.input)
			if test.wantErr {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("error = %v, want ErrInvalid", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalize duration: %v", err)
			}
			if got != test.want {
				t.Fatalf("duration = %s, want %s", got, test.want)
			}
		})
	}
}
