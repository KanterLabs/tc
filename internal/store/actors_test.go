package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"roadmap/internal/db"
)

func TestEnsureCloudflareActorReconcilesAdministrator(t *testing.T) {
	database, err := db.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)

	actor, err := data.EnsureCloudflareActor(context.Background(), "owner@example.com", "Owner", "owner@example.com")
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if !actor.Admin {
		t.Fatal("configured owner was not made administrator")
	}

	actor, err = data.EnsureCloudflareActor(context.Background(), "owner@example.com", "Owner", "other@example.com")
	if err != nil {
		t.Fatalf("reconcile removed owner: %v", err)
	}
	if actor.Admin {
		t.Fatal("old configured owner remained administrator")
	}

	other, err := data.EnsureCloudflareActor(context.Background(), "other@example.com", "Other", "other@example.com")
	if err != nil {
		t.Fatalf("create new owner: %v", err)
	}
	if !other.Admin {
		t.Fatal("new configured owner was not made administrator")
	}
}

func TestEnsureCloudflareActorRejectsAgentEmailCollision(t *testing.T) {
	database, err := db.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	if _, err := data.CreateActor(context.Background(), Actor{Kind: "agent", Name: "agent", Email: stringPtrForTest("owner@example.com")}, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("agent email error = %v, want invalid", err)
	}
}

func stringPtrForTest(value string) *string { return &value }

func TestCreateAgentPersistsDescriptionAndValidatesLength(t *testing.T) {
	database, err := db.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	description := "Handles the release handoff."
	actor, err := data.CreateAgent(context.Background(), Actor{Kind: "agent", Name: "release", Description: description}, "", "")
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if actor.Description != description {
		t.Fatalf("description = %q, want %q", actor.Description, description)
	}
	if _, err := data.CreateAgent(context.Background(), Actor{Kind: "agent", Name: "long", Description: strings.Repeat("x", 10001)}, "", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("long description error = %v, want invalid", err)
	}
}

func TestListActorsIncludesAgentTokenMetadataWithoutHumanTokens(t *testing.T) {
	database, err := db.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)

	human, err := data.CreateActor(context.Background(), Actor{Kind: "human", Name: "Owner", Email: stringPtrForTest("owner@example.com")}, "")
	if err != nil {
		t.Fatalf("create human: %v", err)
	}
	agent, err := data.CreateAgent(context.Background(), Actor{Kind: "agent", Name: "Build agent"}, human.ID, "")
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	created, _, err := data.CreateTokenBy(context.Background(), agent.ID, human.ID, "CI token", []string{"tasks:read"}, nil, nil)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}

	actors, err := data.ListActors(context.Background(), "", 100, 0)
	if err != nil {
		t.Fatalf("list actors: %v", err)
	}
	var gotHuman, gotAgent *Actor
	for i := range actors {
		switch actors[i].ID {
		case human.ID:
			gotHuman = &actors[i]
		case agent.ID:
			gotAgent = &actors[i]
		}
	}
	if gotHuman == nil || gotAgent == nil {
		t.Fatalf("list actors omitted expected rows: human=%v agent=%v", gotHuman != nil, gotAgent != nil)
	}
	if gotHuman.Tokens != nil {
		t.Fatalf("human tokens = %#v, want omitted", gotHuman.Tokens)
	}
	if len(gotAgent.Tokens) != 1 || gotAgent.Tokens[0].ID != created.ID {
		t.Fatalf("agent tokens = %#v, want token %q", gotAgent.Tokens, created.ID)
	}
}
