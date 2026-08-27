package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
