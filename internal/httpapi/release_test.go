package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestHealthAndAPIRootExposeConfiguredRevision(t *testing.T) {
	server, _ := testServer(t, "disabled")
	const revision = "0123456789abcdef0123456789abcdef01234567"
	server.Cfg.ReleaseSHA = revision

	for _, testCase := range []struct {
		target string
		field  string
		value  string
	}{{"/healthz", "service", "helm"}, {"/api/v1", "name", "helm"}} {
		target := testCase.target
		response := request(t, server, http.MethodGet, target, nil, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, body=%s", target, response.Code, response.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode GET %s: %v", target, err)
		}
		if payload["revision"] != revision {
			t.Fatalf("GET %s revision = %#v", target, payload["revision"])
		}
		if payload[testCase.field] != testCase.value {
			t.Fatalf("GET %s %s = %#v, want %q", target, testCase.field, payload[testCase.field], testCase.value)
		}
		if got := response.Header().Get("X-Roadmap-Revision"); got != revision {
			t.Fatalf("GET %s X-Roadmap-Revision = %q", target, got)
		}
	}
}

func TestHealthOmitsUnknownRevision(t *testing.T) {
	server, _ := testServer(t, "disabled")
	response := request(t, server, http.MethodGet, "/healthz", nil, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /healthz status = %d, body=%s", response.Code, response.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if _, exists := payload["revision"]; exists {
		t.Fatalf("health should omit an unknown revision: %s", response.Body.String())
	}
}
