package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/KanterLabs/helm/internal/auth"
	"github.com/KanterLabs/helm/internal/portable"
	"github.com/KanterLabs/helm/internal/store"
)

// exportPortable serves the versioned portability snapshot. The endpoint is
// intentionally separate from database backup/restore: it only returns live,
// user-facing records and never exposes credentials or deployment internals.
func (s *Server) exportPortable(w http.ResponseWriter, r *http.Request, identity auth.Identity, reference string, projectRoute bool) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
		return
	}
	if !requireScope(w, identity, "projects:read") || !requireScope(w, identity, "tasks:read") || !requireScope(w, identity, "events:read") {
		return
	}
	var projectIDs []string
	if projectRoute {
		project, err := s.Store.GetProject(r.Context(), reference)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		if !identity.CanProject(project.ID) {
			s.writeError(w, http.StatusForbidden, "forbidden", "token is not scoped to this project", nil)
			return
		}
		projectIDs = []string{project.ID}
	} else {
		projectReference, err := parseOptionalIdentifier(r, "project")
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
			return
		}
		if projectReference != "" {
			project, err := s.Store.GetProject(r.Context(), projectReference)
			if err != nil {
				s.writeStoreError(w, err)
				return
			}
			if !identity.CanProject(project.ID) {
				s.writeError(w, http.StatusForbidden, "forbidden", "token is not scoped to this project", nil)
				return
			}
			projectIDs = []string{project.ID}
		} else {
			projectIDs = scopedProjectIDs(identity)
		}
	}
	archive, err := s.Store.ExportPortable(r.Context(), projectIDs)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	body, err := json.Marshal(archive)
	if err != nil {
		s.writeInternal(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="helm-portable-v1.json"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Helm-Portable-Format", store.PortableFormat)
	w.Header().Set("X-Helm-Portable-Version", fmt.Sprintf("%d", store.PortableVersion))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

type portableImportEnvelope struct {
	Archive       json.RawMessage `json:"archive"`
	DryRun        *bool           `json:"dry_run"`
	Conflict      string          `json:"conflict"`
	TargetProject string          `json:"target_project"`
}

func portableWarningsDetails(warnings []string) any {
	if len(warnings) == 0 {
		return nil
	}
	return map[string]any{"warnings": warnings}
}

// attachPortableWarnings keeps adapter diagnostics attached to the structured
// store report even when import planning fails. Store errors carry a value
// copy of PortableImportReport, so appending only to the handler's result
// would otherwise lose Trello warnings on the error response.
func attachPortableWarnings(err error, warnings []string) error {
	if err == nil || len(warnings) == 0 {
		return err
	}
	var typed *store.Error
	if !errors.As(err, &typed) {
		return err
	}
	switch details := typed.Details.(type) {
	case store.PortableImportReport:
		details.Warnings = append(warnings, details.Warnings...)
		typed.Details = details
	case map[string]any:
		if existing, ok := details["warnings"].([]string); ok {
			details["warnings"] = append(warnings, existing...)
		} else {
			details["warnings"] = warnings
		}
	default:
		typed.Details = portableWarningsDetails(warnings)
	}
	return err
}

func appendPortableWarnings(report *store.PortableImportReport, warnings []string) {
	if report == nil || len(warnings) == 0 {
		return
	}
	report.Warnings = append(warnings, report.Warnings...)
}

func decodePortableImportBody(r *http.Request) (store.PortableArchive, *portableImportEnvelope, error) {
	body, err := requestBodyData(r)
	if err != nil {
		return store.PortableArchive{}, nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return store.PortableArchive{}, nil, fmt.Errorf("request body must be a JSON object")
	}
	if raw, wrapped := fields["archive"]; wrapped {
		allowed := map[string]struct{}{"archive": {}, "dry_run": {}, "conflict": {}, "target_project": {}}
		for key := range fields {
			if _, ok := allowed[key]; !ok {
				return store.PortableArchive{}, nil, fmt.Errorf("unknown import option %q", key)
			}
		}
		var envelope portableImportEnvelope
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&envelope); err != nil {
			return store.PortableArchive{}, nil, err
		}
		if len(raw) == 0 || string(raw) == "null" {
			return store.PortableArchive{}, nil, fmt.Errorf("archive is required")
		}
		var archive store.PortableArchive
		if err := decodePortableArchive(raw, &archive); err != nil {
			return store.PortableArchive{}, nil, err
		}
		return archive, &envelope, nil
	}
	var archive store.PortableArchive
	if err := decodePortableArchive(body, &archive); err != nil {
		return store.PortableArchive{}, nil, err
	}
	return archive, nil, nil
}

func decodePortableArchive(body []byte, archive *store.PortableArchive) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(archive); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("multiple JSON values")
	}
	return nil
}

func (s *Server) importPortable(w http.ResponseWriter, r *http.Request, identity auth.Identity, reference string, projectRoute bool) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
		return
	}
	if !requireScope(w, identity, "projects:write") || !requireScope(w, identity, "tasks:write") {
		return
	}
	targetProjectID := ""
	if projectRoute {
		project, err := s.Store.GetProject(r.Context(), reference)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		if !identity.CanProject(project.ID) {
			s.writeError(w, http.StatusForbidden, "forbidden", "token is not scoped to this project", nil)
			return
		}
		targetProjectID = project.ID
	}
	targetReference, err := parseOptionalIdentifier(r, "target_project")
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	if targetReference != "" {
		targetProject, lookupErr := s.Store.GetProject(r.Context(), targetReference)
		if lookupErr != nil {
			s.writeStoreError(w, lookupErr)
			return
		}
		if !identity.CanProject(targetProject.ID) {
			s.writeError(w, http.StatusForbidden, "forbidden", "token is not scoped to the target project", nil)
			return
		}
		if targetProjectID != "" && targetProject.ID != targetProjectID {
			s.writeError(w, http.StatusBadRequest, "invalid_request", "target_project conflicts with the project route", nil)
			return
		}
		targetProjectID = targetProject.ID
	}
	archive, envelope, err := decodePortableImportBody(r)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_json", "portable import body is invalid: "+err.Error(), nil)
		return
	}
	dryRun, err := parseOptionalBool(r, "dry_run")
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	conflict, err := parseOptionalEnum(r, "conflict", map[string]struct{}{"remap": {}, "fail": {}})
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	if envelope != nil {
		if envelope.DryRun != nil {
			dryRun = *envelope.DryRun
		}
		if envelope.Conflict != "" {
			if conflict != "" && conflict != envelope.Conflict {
				s.writeError(w, http.StatusBadRequest, "invalid_request", "conflict was supplied with different values in body and query", nil)
				return
			}
			conflict = envelope.Conflict
		}
		if envelope.TargetProject != "" {
			targetReference := strings.TrimSpace(envelope.TargetProject)
			if targetProjectID != "" {
				targetProject, lookupErr := s.Store.GetProject(r.Context(), targetReference)
				if lookupErr != nil {
					s.writeStoreError(w, lookupErr)
					return
				}
				if targetProject.ID != targetProjectID {
					s.writeError(w, http.StatusBadRequest, "invalid_request", "target_project conflicts with the project route", nil)
					return
				}
			} else {
				targetProjectID = targetReference
			}
		}
	}
	if !projectRoute && targetProjectID != "" {
		targetProject, err := s.Store.GetProject(r.Context(), targetProjectID)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		if !identity.CanProject(targetProject.ID) {
			s.writeError(w, http.StatusForbidden, "forbidden", "token is not scoped to the target project", nil)
			return
		}
		targetProjectID = targetProject.ID
	}
	if !projectRoute && identity.IsToken && identity.Token.ProjectsScoped && strings.TrimSpace(targetProjectID) == "" {
		s.writeError(w, http.StatusForbidden, "forbidden", "project-scoped tokens must select an allowed target project", nil)
		return
	}
	if conflict == "" {
		conflict = "remap"
	}
	options := store.PortableImportOptions{TargetProjectID: targetProjectID, Conflict: conflict, DryRun: dryRun, ActorID: identity.Actor.ID}
	if dryRun {
		// A caller may still supply an idempotency key for a preview. Cache that
		// read-only report so changing any import query option cannot replay the
		// prior operation under the same key. No persistent mutation budget is
		// charged because ImportPortable runs in validation-only mode.
		if strings.TrimSpace(r.Header.Get("Idempotency-Key")) == "" {
			report, err := s.Store.ImportPortable(r.Context(), archive, options)
			if err != nil {
				s.writeStoreError(w, err)
				return
			}
			s.writeJSON(w, http.StatusOK, report)
			return
		}
		s.mutationRateOnly(w, r, identity, func() (int, []byte, string, error) {
			report, err := s.Store.ImportPortable(r.Context(), archive, options)
			if err != nil {
				return 0, nil, "", err
			}
			body, err := json.Marshal(report)
			if err != nil {
				return 0, nil, "", err
			}
			return http.StatusOK, body, "", nil
		})
		return
	}
	s.mutation(w, r, identity, func() (int, []byte, string, error) {
		report, err := s.Store.ImportPortable(r.Context(), archive, options)
		if err != nil {
			return 0, nil, "", err
		}
		body, err := json.Marshal(report)
		if err != nil {
			return 0, nil, "", err
		}
		return http.StatusOK, body, "", nil
	})
}

// importTrello is a deliberately separate adapter. It translates a Trello
// board into the stable Helm portability envelope; the core store importer
// never contains Trello-specific field or API assumptions.
func (s *Server) importTrello(w http.ResponseWriter, r *http.Request, identity auth.Identity) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
		return
	}
	if !requireScope(w, identity, "projects:write") || !requireScope(w, identity, "tasks:write") {
		return
	}
	body, err := requestBodyData(r)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_json", "Trello body is required", nil)
		return
	}
	archive, warnings, err := portable.ConvertTrello(body)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), map[string]any{"warnings": warnings})
		return
	}
	dryRun, err := parseOptionalBool(r, "dry_run")
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), portableWarningsDetails(warnings))
		return
	}
	conflict, err := parseOptionalEnum(r, "conflict", map[string]struct{}{"remap": {}, "fail": {}})
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), portableWarningsDetails(warnings))
		return
	}
	if conflict == "" {
		conflict = "remap"
	}
	targetProjectID := ""
	targetReference, err := parseOptionalIdentifier(r, "target_project")
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), portableWarningsDetails(warnings))
		return
	}
	if targetReference != "" {
		targetProject, lookupErr := s.Store.GetProject(r.Context(), targetReference)
		if lookupErr != nil {
			s.writeStoreError(w, attachPortableWarnings(lookupErr, warnings))
			return
		}
		if !identity.CanProject(targetProject.ID) {
			s.writeError(w, http.StatusForbidden, "forbidden", "token is not scoped to the target project", portableWarningsDetails(warnings))
			return
		}
		targetProjectID = targetProject.ID
	}
	if identity.IsToken && identity.Token.ProjectsScoped && targetProjectID == "" {
		s.writeError(w, http.StatusForbidden, "forbidden", "project-scoped tokens must select an allowed target project", portableWarningsDetails(warnings))
		return
	}
	options := store.PortableImportOptions{TargetProjectID: targetProjectID, Conflict: conflict, DryRun: dryRun, ActorID: identity.Actor.ID}
	if dryRun {
		if strings.TrimSpace(r.Header.Get("Idempotency-Key")) == "" {
			report, err := s.Store.ImportPortable(r.Context(), archive, options)
			appendPortableWarnings(&report, warnings)
			if err != nil {
				s.writeStoreError(w, attachPortableWarnings(err, warnings))
				return
			}
			s.writeJSON(w, http.StatusOK, report)
			return
		}
		s.mutationRateOnly(w, r, identity, func() (int, []byte, string, error) {
			report, err := s.Store.ImportPortable(r.Context(), archive, options)
			appendPortableWarnings(&report, warnings)
			if err != nil {
				return 0, nil, "", attachPortableWarnings(err, warnings)
			}
			encoded, err := json.Marshal(report)
			if err != nil {
				return 0, nil, "", err
			}
			return http.StatusOK, encoded, "", nil
		})
		return
	}
	s.mutation(w, r, identity, func() (int, []byte, string, error) {
		report, err := s.Store.ImportPortable(r.Context(), archive, options)
		appendPortableWarnings(&report, warnings)
		if err != nil {
			return 0, nil, "", attachPortableWarnings(err, warnings)
		}
		encoded, err := json.Marshal(report)
		if err != nil {
			return 0, nil, "", err
		}
		return http.StatusOK, encoded, "", nil
	})
}
