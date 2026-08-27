package store

import (
	"context"
	"testing"

	"roadmap/internal/db"
)

func TestIdempotencyRecordPersistsResponseLocation(t *testing.T) {
	database, err := db.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	data := New(database)
	if _, err := data.CreateActor(context.Background(), Actor{ID: "actor-1", Kind: "agent", Name: "idempotency actor"}, ""); err != nil {
		t.Fatalf("create actor: %v", err)
	}
	want := IdempotencyRecord{
		Status:           201,
		ResponseBody:     []byte(`{"id":"project-1"}`),
		ETag:             `"v1"`,
		ResponseLocation: "/api/v1/projects/project-1",
	}
	if err := data.SaveIdempotency(context.Background(), "actor-1", "create-1", "POST", "/api/v1/projects", "hash-1", want); err != nil {
		t.Fatalf("save idempotency record: %v", err)
	}

	got, found, err := data.GetIdempotency(context.Background(), "actor-1", "create-1", "POST", "/api/v1/projects", "hash-1")
	if err != nil {
		t.Fatalf("get idempotency record: %v", err)
	}
	if !found {
		t.Fatal("saved idempotency record was not found")
	}
	if got.Status != want.Status || string(got.ResponseBody) != string(want.ResponseBody) || got.ETag != want.ETag || got.ResponseLocation != want.ResponseLocation {
		t.Fatalf("idempotency record = %#v, want %#v", got, want)
	}
}
