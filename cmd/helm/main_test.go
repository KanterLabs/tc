package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunMigrationInfo(t *testing.T) {
	var output bytes.Buffer
	if err := runMigrationInfo(&output); err != nil {
		t.Fatalf("runMigrationInfo: %v", err)
	}
	text := output.String()
	if !strings.Contains(text, "latest_schema_version=") || !strings.Contains(text, "migration_digest=") {
		t.Fatalf("migration-info output = %q", text)
	}
}

func TestRunSchemaPreflightRejectsMissingSource(t *testing.T) {
	var output bytes.Buffer
	err := runSchemaPreflight(context.Background(), filepath.Join(t.TempDir(), "missing.db"), &output)
	if err == nil {
		t.Fatal("runSchemaPreflight unexpectedly succeeded")
	}
	if output.Len() != 0 {
		t.Fatalf("failed preflight wrote output: %q", output.String())
	}
}

func TestCheckHealth(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr bool
	}{
		{name: "ok", status: http.StatusOK, body: `{"status":"ok"}`},
		{name: "http failure", status: http.StatusServiceUnavailable, body: `{"status":"ok"}`, wantErr: true},
		{name: "wrong health status", status: http.StatusOK, body: `{"status":"degraded"}`, wantErr: true},
		{name: "invalid JSON", status: http.StatusOK, body: `not-json`, wantErr: true},
		{name: "oversized body", status: http.StatusOK, body: `{"status":"ok","padding":"` + strings.Repeat("x", healthcheckMaxBody) + `"}`, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var method, path string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				method, path = r.Method, r.URL.Path
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()

			err := checkHealth(context.Background(), server.Client(), server.URL+"/healthz")
			if (err != nil) != test.wantErr {
				t.Fatalf("checkHealth() error = %v, wantErr=%v", err, test.wantErr)
			}
			if method != http.MethodGet || path != "/healthz" {
				t.Fatalf("request = %s %s, want GET /healthz", method, path)
			}
		})
	}
}
