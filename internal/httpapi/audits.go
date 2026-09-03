package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/KanterLabs/helm/internal/auth"
	"github.com/KanterLabs/helm/internal/store"
)

func validHTTPAuditTerminalStatus(value string) bool {
	switch value {
	case "complete", "partial", "failed":
		return true
	default:
		return false
	}
}

func requireAuditReadScope(w http.ResponseWriter, identity auth.Identity) bool {
	// Findings contain task-derived reasons and evidence. Reuse the existing
	// task scope boundary so tokens cannot gain task context through audits.
	if identity.HasScope("tasks:read") {
		return true
	}
	sDummyWriteError(w, http.StatusForbidden, "insufficient_scope", "token lacks required scope", map[string]any{"required_scope": "tasks:read"})
	return false
}

func requireAuditWriteScope(w http.ResponseWriter, identity auth.Identity) bool {
	if identity.HasScope("tasks:write") {
		return true
	}
	sDummyWriteError(w, http.StatusForbidden, "insufficient_scope", "token lacks required scope", map[string]any{"required_scope": "tasks:write"})
	return false
}

// audits handles the collection surface. projectRoute is true for
// /projects/{project}/audits and false for the optional global /audits
// collection. The parent router can call this method directly.
func (s *Server) audits(w http.ResponseWriter, r *http.Request, identity auth.Identity, projectReference string, projectRoute bool) {
	if r.Method == http.MethodPost {
		if !projectRoute {
			s.writeError(w, http.StatusBadRequest, "invalid_request", "a project is required to create an audit run", nil)
			return
		}
		if !requireAuditWriteScope(w, identity) {
			return
		}
	} else if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
		return
	} else if !requireAuditReadScope(w, identity) {
		return
	}

	projectID := ""
	if projectRoute {
		project, err := s.Store.GetProject(r.Context(), projectReference)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		if !identity.CanProject(project.ID) {
			s.writeError(w, http.StatusForbidden, "forbidden", "token is not scoped to this project", nil)
			return
		}
		projectID = project.ID
	} else if reference, err := parseOptionalIdentifier(r, "project"); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	} else if reference != "" {
		project, err := s.Store.GetProject(r.Context(), reference)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		if !identity.CanProject(project.ID) {
			s.writeError(w, http.StatusForbidden, "forbidden", "token is not scoped to this project", nil)
			return
		}
		projectID = project.ID
	} else if identity.IsToken && identity.Token.ProjectsScoped {
		// A global aggregate is safe only when the store receives the token's
		// complete allow-list. This also permits a useful union for scoped agents
		// without exposing runs from another project.
		projectIDs := scopedProjectIDs(identity)
		limit, offset, err := parsePagination(r, 50)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
			return
		}
		runs, more, err := s.Store.ListAuditRunsForProjects(r.Context(), projectIDs, limit, offset)
		if err != nil {
			s.writeInternal(w, err)
			return
		}
		next := ""
		if more {
			next = encodeCursor(offset + len(runs))
		}
		s.writeCollection(w, runs, next)
		return
	}
	if r.Method == http.MethodPost && s.idempotencyReplay(w, r, identity) {
		return
	}

	if r.Method == http.MethodGet {
		limit, offset, err := parsePagination(r, 50)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
			return
		}
		var projectIDs []string
		if projectID != "" {
			projectIDs = []string{projectID}
		}
		runs, more, err := s.Store.ListAuditRunsForProjects(r.Context(), projectIDs, limit, offset)
		if err != nil {
			s.writeInternal(w, err)
			return
		}
		next := ""
		if more {
			next = encodeCursor(offset + len(runs))
		}
		s.writeCollection(w, runs, next)
		return
	}

	// projectID is resolved and authorized above before body validation, so a
	// malformed create cannot disclose whether an inaccessible project exists.
	var payload auditRunRequest
	fields, err := decodeJSONObject(r, &payload)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_json", "request body is invalid", nil)
		return
	}
	if err := requireJSONFields(fields, "scope"); err != nil {
		s.writeStoreError(w, err)
		return
	}
	if err := rejectJSONNull(fields, "scope", "status"); err != nil {
		s.writeStoreError(w, err)
		return
	}
	if payload.Scope == nil {
		s.writeStoreError(w, &store.Error{Kind: store.ErrInvalid, Message: "scope is required"})
		return
	}
	input := store.AuditRunInput{Scope: *payload.Scope}
	if payload.Status != nil {
		input.Status = *payload.Status
	}
	s.mutationRateOnly(w, r, identity, func() (int, []byte, string, error) {
		run, err := s.Store.CreateAuditRun(r.Context(), projectID, identity.Actor.ID, input)
		if err != nil {
			return 0, nil, "", err
		}
		location := "/api/v1/audits/" + run.ID
		w.Header().Set("Location", location)
		body, err := json.Marshal(run)
		if err != nil {
			return 0, nil, "", err
		}
		return http.StatusCreated, body, "", nil
	})
}

type auditRunRequest struct {
	Scope  *string `json:"scope"`
	Status *string `json:"status"`
}

// audit handles GET /audits/{audit}. Finalization is kept in auditFinalize so
// the parent router can map the explicit /finalize action without ambiguity.
func (s *Server) audit(w http.ResponseWriter, r *http.Request, identity auth.Identity, auditID string) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
		return
	}
	if !requireAuditReadScope(w, identity) {
		return
	}
	run, err := s.Store.GetAuditRun(r.Context(), auditID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if !identity.CanProject(run.ProjectID) {
		s.writeError(w, http.StatusForbidden, "forbidden", "token is not scoped to this project", nil)
		return
	}
	s.writeJSON(w, http.StatusOK, run)
}

// auditFindings handles the bounded, paginated finding collection for a run.
func (s *Server) auditFindings(w http.ResponseWriter, r *http.Request, identity auth.Identity, auditID string) {
	switch r.Method {
	case http.MethodGet:
		if !requireAuditReadScope(w, identity) {
			return
		}
		run, err := s.Store.GetAuditRun(r.Context(), auditID)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		if !identity.CanProject(run.ProjectID) {
			s.writeError(w, http.StatusForbidden, "forbidden", "token is not scoped to this project", nil)
			return
		}
		limit, offset, err := parsePagination(r, 50)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
			return
		}
		findings, more, err := s.Store.ListAuditFindings(r.Context(), auditID, limit, offset)
		if err != nil {
			s.writeInternal(w, err)
			return
		}
		next := ""
		if more {
			next = encodeCursor(offset + len(findings))
		}
		s.writeCollection(w, findings, next)
	case http.MethodPost:
		if !requireAuditWriteScope(w, identity) {
			return
		}
		run, err := s.Store.GetAuditRun(r.Context(), auditID)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		if !identity.CanProject(run.ProjectID) {
			s.writeError(w, http.StatusForbidden, "forbidden", "token is not scoped to this project", nil)
			return
		}
		if s.idempotencyReplay(w, r, identity) {
			return
		}
		input, err := decodeAuditFindingInput(r)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		s.mutation(w, r, identity, func() (int, []byte, string, error) {
			finding, err := s.Store.AppendAuditFinding(r.Context(), auditID, input)
			if err != nil {
				return 0, nil, "", err
			}
			w.Header().Set("Location", "/api/v1/audits/"+auditID+"/findings/"+finding.ID)
			body, err := marshalAuditFindingForIdentity(identity, finding)
			if err != nil {
				return 0, nil, "", err
			}
			return http.StatusCreated, body, auditFindingETag(finding), nil
		})
	default:
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
	}
}

func decodeAuditFindingInput(r *http.Request) (store.AuditFindingInput, error) {
	var payload auditFindingRequest
	fields, err := decodeJSONObject(r, &payload)
	if err != nil {
		return store.AuditFindingInput{}, &store.Error{Kind: store.ErrInvalid, Message: "audit finding request body is invalid"}
	}
	if err := requireJSONFields(fields, "task_id", "captured_version", "verdict", "confidence", "reason"); err != nil {
		return store.AuditFindingInput{}, err
	}
	if !jsonFieldPresent(fields, "source_column") && !jsonFieldPresent(fields, "source_column_id") {
		return store.AuditFindingInput{}, &store.Error{Kind: store.ErrInvalid, Message: "source_column is required"}
	}
	if err := rejectJSONNull(fields, "task_id", "captured_version", "source_column", "source_column_id", "verdict", "confidence", "reason", "evidence_refs", "review_state"); err != nil {
		return store.AuditFindingInput{}, err
	}
	if payload.TaskID == nil || strings.TrimSpace(*payload.TaskID) == "" {
		return store.AuditFindingInput{}, &store.Error{Kind: store.ErrInvalid, Message: "task_id must not be empty"}
	}
	if payload.SourceColumn == nil && payload.SourceColumnID == nil {
		return store.AuditFindingInput{}, &store.Error{Kind: store.ErrInvalid, Message: "source_column is required"}
	}
	if payload.SourceColumn != nil && payload.SourceColumnID != nil && strings.TrimSpace(*payload.SourceColumn) != strings.TrimSpace(*payload.SourceColumnID) {
		return store.AuditFindingInput{}, &store.Error{Kind: store.ErrInvalid, Message: "source_column and source_column_id must match"}
	}
	if payload.CapturedVersion == nil {
		return store.AuditFindingInput{}, &store.Error{Kind: store.ErrInvalid, Message: "captured_version is required"}
	}
	if payload.Verdict == nil {
		return store.AuditFindingInput{}, &store.Error{Kind: store.ErrInvalid, Message: "verdict is required"}
	}
	if payload.Confidence == nil {
		return store.AuditFindingInput{}, &store.Error{Kind: store.ErrInvalid, Message: "confidence is required"}
	}
	if payload.Reason == nil {
		return store.AuditFindingInput{}, &store.Error{Kind: store.ErrInvalid, Message: "reason is required"}
	}
	if raw, present := fields["evidence_refs"]; present {
		if err := validateIdentifierArray(raw, "evidence_refs", false); err != nil {
			return store.AuditFindingInput{}, &store.Error{Kind: store.ErrInvalid, Message: "evidence_refs must be an array of safe references"}
		}
	}
	input := store.AuditFindingInput{
		TaskID:                      *payload.TaskID,
		CapturedVersion:             *payload.CapturedVersion,
		Verdict:                     *payload.Verdict,
		Confidence:                  *payload.Confidence,
		Reason:                      *payload.Reason,
		EvidenceRefs:                payload.EvidenceRefs,
		ProposedSemanticDestination: payload.ProposedSemanticDestination,
		ReviewState:                 "pending",
	}
	if payload.SourceColumn != nil {
		input.SourceColumn = *payload.SourceColumn
	}
	if payload.SourceColumnID != nil {
		input.SourceColumnID = *payload.SourceColumnID
	}
	if payload.ReviewState != nil {
		input.ReviewState = *payload.ReviewState
	}
	return input, nil
}

type auditFindingRequest struct {
	TaskID                      *string  `json:"task_id"`
	CapturedVersion             *int64   `json:"captured_version"`
	SourceColumn                *string  `json:"source_column"`
	SourceColumnID              *string  `json:"source_column_id"`
	Verdict                     *string  `json:"verdict"`
	ProposedSemanticDestination *string  `json:"proposed_semantic_destination"`
	Confidence                  *float64 `json:"confidence"`
	Reason                      *string  `json:"reason"`
	EvidenceRefs                []string `json:"evidence_refs"`
	ReviewState                 *string  `json:"review_state"`
}

// auditFinalize handles POST /audits/{audit}/finalize. The only caller-owned
// value is the terminal outcome; all timestamps and lifecycle transitions are
// server-owned.
func (s *Server) auditFinalize(w http.ResponseWriter, r *http.Request, identity auth.Identity, auditID string) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
		return
	}
	if !requireAuditWriteScope(w, identity) {
		return
	}
	run, err := s.Store.GetAuditRun(r.Context(), auditID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if !identity.CanProject(run.ProjectID) {
		s.writeError(w, http.StatusForbidden, "forbidden", "token is not scoped to this project", nil)
		return
	}
	if s.idempotencyReplay(w, r, identity) {
		return
	}
	var payload auditFinalizeRequest
	fields, err := decodeJSONObject(r, &payload)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_json", "request body is invalid", nil)
		return
	}
	if err := requireJSONFields(fields, "status"); err != nil {
		s.writeStoreError(w, err)
		return
	}
	if err := rejectJSONNull(fields, "status"); err != nil {
		s.writeStoreError(w, err)
		return
	}
	if payload.Status == nil || !validHTTPAuditTerminalStatus(strings.TrimSpace(*payload.Status)) {
		s.writeStoreError(w, &store.Error{Kind: store.ErrInvalid, Message: "status must be complete, partial, or failed"})
		return
	}
	s.mutationRateOnly(w, r, identity, func() (int, []byte, string, error) {
		finalized, err := s.Store.FinalizeAuditRun(r.Context(), auditID, strings.TrimSpace(*payload.Status), identity.Actor.ID)
		if err != nil {
			return 0, nil, "", err
		}
		body, err := json.Marshal(finalized)
		if err != nil {
			return 0, nil, "", err
		}
		return http.StatusOK, body, "", nil
	})
}

type auditFinalizeRequest struct {
	Status *string `json:"status"`
}

// auditFinding handles PATCH /audit-findings/{finding}. Only the finding's
// review state and optional proposed destination are mutable.
func (s *Server) auditFinding(w http.ResponseWriter, r *http.Request, identity auth.Identity, findingID string) {
	if r.Method != http.MethodPatch {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
		return
	}
	if !requireAuditWriteScope(w, identity) {
		return
	}
	expectedVersion, err := parseIfMatch(r)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	projectID, err := s.Store.GetAuditFindingProject(r.Context(), findingID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if !identity.CanProject(projectID) {
		s.writeError(w, http.StatusForbidden, "forbidden", "token is not scoped to this project", nil)
		return
	}
	if s.idempotencyReplay(w, r, identity) {
		return
	}
	input, err := decodeAuditFindingReviewInput(r)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	s.mutation(w, r, identity, func() (int, []byte, string, error) {
		updated, err := s.Store.UpdateAuditFindingReview(r.Context(), findingID, input, expectedVersion, identity.Actor.ID)
		if err != nil {
			return 0, nil, "", err
		}
		body, err := marshalAuditFindingForIdentity(identity, updated)
		if err != nil {
			return 0, nil, "", err
		}
		return http.StatusOK, body, auditFindingETag(updated), nil
	})
}

type auditFindingReviewRequest struct {
	ReviewState                 *string `json:"review_state"`
	ProposedSemanticDestination *string `json:"proposed_semantic_destination"`
}

func decodeAuditFindingReviewInput(r *http.Request) (store.AuditFindingReviewInput, error) {
	var payload auditFindingReviewRequest
	fields, err := decodeJSONObject(r, &payload)
	if err != nil {
		return store.AuditFindingReviewInput{}, &store.Error{Kind: store.ErrInvalid, Message: "audit finding review request body is invalid"}
	}
	if err := requireJSONFields(fields, "review_state"); err != nil {
		return store.AuditFindingReviewInput{}, err
	}
	if err := rejectJSONNull(fields, "review_state"); err != nil {
		return store.AuditFindingReviewInput{}, err
	}
	if payload.ReviewState == nil || strings.TrimSpace(*payload.ReviewState) == "" {
		return store.AuditFindingReviewInput{}, &store.Error{Kind: store.ErrInvalid, Message: "review_state is required"}
	}
	return store.AuditFindingReviewInput{
		ReviewState:                    *payload.ReviewState,
		ProposedSemanticDestination:    payload.ProposedSemanticDestination,
		ProposedSemanticDestinationSet: jsonFieldPresent(fields, "proposed_semantic_destination"),
	}, nil
}

func auditFindingETag(finding store.AuditFinding) string {
	return `"v` + strconv.FormatInt(finding.Version, 10) + `"`
}

func marshalAuditFindingForIdentity(identity auth.Identity, finding store.AuditFinding) ([]byte, error) {
	if !identity.IsToken || identity.HasScope("tasks:read") {
		return json.Marshal(finding)
	}
	return json.Marshal(map[string]any{
		"id":      finding.ID,
		"version": finding.Version,
	})
}
