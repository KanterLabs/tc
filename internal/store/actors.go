package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

const actorSelect = `SELECT id, kind, name, email, admin, disabled_at, created_at, updated_at, description FROM actors`

func (s *Store) GetActor(ctx context.Context, id string) (Actor, error) {
	row := s.DB.QueryRowContext(ctx, actorSelect+` WHERE id = ?`, id)
	a, err := actorFromRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Actor{}, notFound("actor not found")
	}
	if err == nil {
		err = s.enrichActor(ctx, &a)
	}
	return a, err
}

func (s *Store) FindActorByEmail(ctx context.Context, email string) (Actor, error) {
	row := s.DB.QueryRowContext(ctx, actorSelect+` WHERE lower(email) = lower(?)`, strings.TrimSpace(email))
	a, err := actorFromRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Actor{}, ErrNotFound
	}
	if err == nil {
		err = s.enrichActor(ctx, &a)
	}
	return a, err
}

func (s *Store) CountHumanActors(ctx context.Context) (int, error) {
	var count int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM actors WHERE kind = 'human' AND id <> 'actor-disabled-mode'`).Scan(&count)
	return count, err
}

func (s *Store) CreateActor(ctx context.Context, actor Actor, passwordHash string) (Actor, error) {
	if actor.ID == "" {
		actor.ID = newID()
	}
	if actor.Kind != "human" && actor.Kind != "agent" {
		return Actor{}, invalid("kind must be human or agent", nil)
	}
	name := strings.TrimSpace(actor.Name)
	if err := validateActorName(name); err != nil {
		return Actor{}, err
	}
	if actor.Kind == "agent" && strings.TrimSpace(actor.EmailValue()) != "" {
		return Actor{}, invalid("agent email is not supported", nil)
	}
	description := strings.TrimSpace(actor.Description)
	if utf8.RuneCountInString(description) > 10000 {
		return Actor{}, invalid("description must be 10000 characters or fewer", nil)
	}
	created := now()
	_, err := s.DB.ExecContext(ctx, `INSERT INTO actors(id, kind, name, email, password_hash, admin, disabled_at, created_at, updated_at, description) VALUES (?, ?, ?, NULLIF(?, ''), ?, ?, NULLIF(?, ''), ?, ?, ?)`, actor.ID, actor.Kind, name, strings.TrimSpace(actor.EmailValue()), passwordHash, boolInt(actor.Admin), actor.DisabledValue(), created, created, description)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return Actor{}, &Error{Kind: ErrAlreadyExists, Message: "actor email already exists"}
		}
		return Actor{}, err
	}
	return s.GetActor(ctx, actor.ID)
}

// CreateAgent records the administrator responsible for provisioning an agent
// in the same transaction as the actor row.
func (s *Store) CreateAgent(ctx context.Context, actor Actor, createdBy, passwordHash string) (Actor, error) {
	if actor.Kind == "" {
		actor.Kind = "agent"
	}
	if actor.Kind != "agent" {
		return Actor{}, invalid("an agent name is required", nil)
	}
	name := strings.TrimSpace(actor.Name)
	if err := validateActorName(name); err != nil {
		return Actor{}, err
	}
	if strings.TrimSpace(actor.EmailValue()) != "" {
		return Actor{}, invalid("agent email is not supported", nil)
	}
	description := strings.TrimSpace(actor.Description)
	if utf8.RuneCountInString(description) > 10000 {
		return Actor{}, invalid("description must be 10000 characters or fewer", nil)
	}
	if actor.ID == "" {
		actor.ID = newID()
	}
	created := now()
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO actors(id, kind, name, email, password_hash, admin, created_at, updated_at, description) VALUES (?, 'agent', ?, NULLIF(?, ''), ?, 0, ?, ?, ?)`, actor.ID, name, strings.TrimSpace(actor.EmailValue()), passwordHash, created, created, description); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				return &Error{Kind: ErrAlreadyExists, Message: "actor email already exists"}
			}
			return err
		}
		_, err := insertEvent(ctx, tx, "agent.created", createdBy, "", "", map[string]any{"actor_id": actor.ID, "name": actor.Name})
		if err != nil {
			return err
		}
		return attachActorProjects(ctx, tx, actor.ID, actor.ProjectIDs)
	})
	if err != nil {
		return Actor{}, err
	}
	return s.GetActor(ctx, actor.ID)
}

// CreateFirstAdmin reserves the singleton setup row and creates the initial
// human in one transaction. The unique row makes two simultaneous first-run
// setup requests deterministic even when they arrive on different goroutines.
func (s *Store) CreateFirstAdmin(ctx context.Context, actor Actor, passwordHash string) (Actor, error) {
	if actor.Kind == "" {
		actor.Kind = "human"
	}
	if actor.Kind != "human" {
		return Actor{}, invalid("a human name is required", nil)
	}
	name := strings.TrimSpace(actor.Name)
	if err := validateActorName(name); err != nil {
		return Actor{}, err
	}
	description := strings.TrimSpace(actor.Description)
	if utf8.RuneCountInString(description) > 10000 {
		return Actor{}, invalid("description must be 10000 characters or fewer", nil)
	}
	if actor.ID == "" {
		actor.ID = newID()
	}
	created := now()
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO auth_setup(id, completed_at) VALUES (1, ?)`, created); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				return &Error{Kind: ErrConflict, Message: "authentication is already configured"}
			}
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO actors(id, kind, name, email, password_hash, admin, created_at, updated_at, description) VALUES (?, 'human', ?, NULLIF(?, ''), ?, 1, ?, ?, ?)`, actor.ID, name, strings.TrimSpace(actor.EmailValue()), passwordHash, created, created, description)
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				return &Error{Kind: ErrAlreadyExists, Message: "actor email already exists"}
			}
			return err
		}
		_, err = insertEvent(ctx, tx, "actor.created", actor.ID, "", "", map[string]any{"actor_id": actor.ID, "kind": actor.Kind})
		return err
	})
	if err != nil {
		return Actor{}, err
	}
	return s.GetActor(ctx, actor.ID)
}

// EmailValue and DisabledValue keep nullable fields out of SQL call sites.
func (a Actor) EmailValue() string {
	if a.Email == nil {
		return ""
	}
	return *a.Email
}
func (a Actor) DisabledValue() string {
	if a.DisabledAt == nil {
		return ""
	}
	return *a.DisabledAt
}

func (s *Store) EnsureCloudflareActor(ctx context.Context, email, name, adminEmail string) (Actor, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return Actor{}, invalid("Cloudflare identity email is missing", nil)
	}
	actor, err := s.FindActorByEmail(ctx, email)
	if err == nil {
		if actor.Kind != "human" {
			return Actor{}, &Error{Kind: ErrForbidden, Message: "Cloudflare identity is not a human actor"}
		}
		if actor.DisabledAt != nil {
			return Actor{}, &Error{Kind: ErrForbidden, Message: "actor is disabled"}
		}
		return s.reconcileCloudflareAdmin(ctx, actor, email, adminEmail)
	}
	if !errors.Is(err, ErrNotFound) {
		return Actor{}, err
	}
	if strings.TrimSpace(name) == "" {
		name = strings.Split(email, "@")[0]
	}
	admin := strings.EqualFold(email, strings.TrimSpace(adminEmail)) && strings.TrimSpace(adminEmail) != ""
	mail := email
	created, createErr := s.CreateActor(ctx, Actor{Kind: "human", Name: name, Email: &mail, Admin: admin}, "")
	if errors.Is(createErr, ErrAlreadyExists) {
		actor, findErr := s.FindActorByEmail(ctx, email)
		if findErr != nil {
			return Actor{}, findErr
		}
		if actor.Kind != "human" {
			return Actor{}, &Error{Kind: ErrForbidden, Message: "Cloudflare identity is not a human actor"}
		}
		if actor.DisabledAt != nil {
			return Actor{}, &Error{Kind: ErrForbidden, Message: "actor is disabled"}
		}
		return s.reconcileCloudflareAdmin(ctx, actor, email, adminEmail)
	}
	return created, createErr
}

func (s *Store) reconcileCloudflareAdmin(ctx context.Context, actor Actor, email, adminEmail string) (Actor, error) {
	wanted := strings.EqualFold(email, strings.TrimSpace(adminEmail)) && strings.TrimSpace(adminEmail) != ""
	if actor.Admin == wanted {
		return actor, nil
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE actors SET admin=?, updated_at=? WHERE id=?`, boolInt(wanted), now(), actor.ID); err != nil {
		return Actor{}, err
	}
	return s.GetActor(ctx, actor.ID)
}

func validateActorName(name string) error {
	if name == "" {
		return invalid("name is required", nil)
	}
	if utf8.RuneCountInString(name) > 200 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return invalid("name must be between 1 and 200 characters", nil)
	}
	return nil
}

func attachActorProjects(ctx context.Context, tx *sql.Tx, actorID string, references []string) error {
	for _, reference := range uniqueStrings(references) {
		var projectID string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM projects WHERE id=? OR slug=? OR key=? LIMIT 1`, reference, strings.ToLower(reference), strings.ToUpper(reference)).Scan(&projectID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return invalid("agent project not found: "+reference, nil)
			}
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO actor_projects(actor_id, project_id) VALUES (?, ?)`, actorID, projectID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) enrichActor(ctx context.Context, actor *Actor) error {
	rows, err := s.DB.QueryContext(ctx, `SELECT project_id FROM actor_projects WHERE actor_id=? ORDER BY project_id`, actor.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var projectID string
		if err := rows.Scan(&projectID); err != nil {
			return err
		}
		actor.ProjectIDs = append(actor.ProjectIDs, projectID)
	}
	return rows.Err()
}

func (s *Store) ListActors(ctx context.Context, kind string, limit, offset int) ([]Actor, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 200 {
		limit = 200
	}
	query := actorSelect
	args := make([]any, 0, 3)
	if kind != "" {
		query += ` WHERE kind = ?`
		args = append(args, kind)
	}
	query += ` ORDER BY lower(name), id LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Actor, 0)
	for rows.Next() {
		a, err := actorFromRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for i := range result {
		if err := s.enrichActor(ctx, &result[i]); err != nil {
			return nil, err
		}
		// Agent settings need token metadata so operators can revoke tokens
		// without exposing their one-time plaintext values. Human actor
		// responses intentionally omit this unrelated collection.
		if result[i].Kind == "agent" {
			tokens, err := s.ListTokens(ctx, result[i].ID)
			if err != nil {
				return nil, err
			}
			result[i].Tokens = tokens
		}
	}
	return result, nil
}

// ListActorsPage returns one cursor page and whether another row follows it.
// includeDisabled controls whether disabled actors are eligible for the page;
// agent collections pass this from their explicit disabled query filter.
func (s *Store) ListActorsPage(ctx context.Context, kind string, limit, offset int, includeDisabled bool) ([]Actor, bool, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	query := actorSelect
	args := make([]any, 0, 4)
	conditions := make([]string, 0, 2)
	if kind != "" {
		conditions = append(conditions, "kind = ?")
		args = append(args, kind)
	}
	if !includeDisabled {
		conditions = append(conditions, "disabled_at IS NULL")
	}
	if len(conditions) > 0 {
		query += ` WHERE ` + strings.Join(conditions, ` AND `)
	}
	query += ` ORDER BY lower(name), id LIMIT ? OFFSET ?`
	args = append(args, limit+1, offset)
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	result := make([]Actor, 0, limit)
	for rows.Next() {
		a, err := actorFromRow(rows)
		if err != nil {
			return nil, false, err
		}
		result = append(result, a)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(result) > limit
	if hasMore {
		result = result[:limit]
	}
	for i := range result {
		if err := s.enrichActor(ctx, &result[i]); err != nil {
			return nil, false, err
		}
		// Agent settings need token metadata so operators can revoke tokens
		// without exposing their one-time plaintext values. Human actor
		// responses intentionally omit this unrelated collection.
		if result[i].Kind == "agent" {
			tokens, err := s.ListTokens(ctx, result[i].ID)
			if err != nil {
				return nil, false, err
			}
			result[i].Tokens = tokens
		}
	}
	return result, hasMore, nil
}

func (s *Store) SetPassword(ctx context.Context, actorID, passwordHash string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE actors SET password_hash = ?, updated_at = ? WHERE id = ?`, passwordHash, now(), actorID)
	return err
}

func (s *Store) GetPasswordHash(ctx context.Context, email string) (Actor, string, error) {
	var hash sql.NullString
	row := s.DB.QueryRowContext(ctx, `SELECT id, kind, name, email, admin, disabled_at, created_at, updated_at, description, password_hash FROM actors WHERE lower(email) = lower(?)`, strings.TrimSpace(email))
	var actor Actor
	var actorEmail, disabled sql.NullString
	var admin int
	if err := row.Scan(&actor.ID, &actor.Kind, &actor.Name, &actorEmail, &admin, &disabled, &actor.CreatedAt, &actor.UpdatedAt, &actor.Description, &hash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Actor{}, "", ErrNotFound
		}
		return Actor{}, "", err
	}
	actor.Email, actor.DisabledAt, actor.Admin = nullableString(actorEmail), nullableString(disabled), boolValue(admin)
	if !hash.Valid {
		return actor, "", nil
	}
	return actor, hash.String, nil
}

func (s *Store) EnsureDisabledActor(ctx context.Context) (Actor, error) {
	const id = "actor-disabled-mode"
	actor, err := s.GetActor(ctx, id)
	if err == nil {
		return actor, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Actor{}, err
	}
	return s.CreateActor(ctx, Actor{ID: id, Kind: "human", Name: "Development user", Admin: true}, "")
}
