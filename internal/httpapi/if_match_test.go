package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"roadmap/internal/store"
)

func TestParseIfMatchAcceptsOnlyVersionETags(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  int64
	}{
		{name: "small version", value: `"v1"`, want: 1},
		{name: "large version", value: `"v9223372036854775807"`, want: 9223372036854775807},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPatch, "/api/v1/tasks/task-1", nil)
			req.Header.Set("If-Match", test.value)
			got, err := parseIfMatch(req)
			if err != nil {
				t.Fatalf("parseIfMatch(%q): %v", test.value, err)
			}
			if got != test.want {
				t.Fatalf("parseIfMatch(%q) = %d, want %d", test.value, got, test.want)
			}
		})
	}
}

func TestParseIfMatchRejectsMalformedVersionETags(t *testing.T) {
	for _, value := range []string{
		"3",
		"v3",
		`"3"`,
		`W/"v3"`,
		`"v0"`,
		`"v01"`,
		`"v-1"`,
		`"v9223372036854775808"`,
	} {
		t.Run(value, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPatch, "/api/v1/tasks/task-1", nil)
			req.Header.Set("If-Match", value)
			if _, err := parseIfMatch(req); !errors.Is(err, store.ErrInvalid) {
				t.Fatalf("parseIfMatch(%q) error = %v, want invalid input", value, err)
			}
		})
	}
}

func TestParseIfMatchMissingReturnsPreconditionError(t *testing.T) {
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/tasks/task-1", nil)
	if _, err := parseIfMatch(req); !errors.Is(err, store.ErrPrecondition) {
		t.Fatalf("parseIfMatch missing error = %v, want precondition required", err)
	}
}
