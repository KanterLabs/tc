package store

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type AuthToken struct {
	Token          Token
	Actor          Actor
	Scopes         map[string]bool
	Projects       map[string]bool
	ProjectsScoped bool
}

const tokenLastUsedThrottle = 5 * time.Minute

func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func (s *Store) CreateSession(ctx context.Context, actorID, raw string, expires time.Time) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO sessions(id, actor_id, token_hash, expires_at, created_at, last_seen_at) VALUES (?, ?, ?, ?, ?, ?)`, newID(), actorID, HashSecret(raw), expires.UTC().Format(time.RFC3339Nano), now(), now())
	return err
}

func (s *Store) ActorForSession(ctx context.Context, raw string) (Actor, error) {
	var actorID string
	var lastSeen sql.NullString
	err := s.DB.QueryRowContext(ctx, `SELECT actor_id, last_seen_at FROM sessions WHERE token_hash = ? AND expires_at > ?`, HashSecret(raw), now()).Scan(&actorID, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return Actor{}, ErrNotFound
	}
	if err != nil {
		return Actor{}, err
	}
	lastSeenCheck := time.Now().UTC()
	if authTimestampNeedsRefresh(lastSeen, lastSeenCheck) {
		seenAt := lastSeenCheck.Format(time.RFC3339Nano)
		if lastSeen.Valid {
			_, _ = s.DB.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ? WHERE token_hash = ? AND last_seen_at = ?`, seenAt, HashSecret(raw), lastSeen.String)
		} else {
			_, _ = s.DB.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ? WHERE token_hash = ? AND last_seen_at IS NULL`, seenAt, HashSecret(raw))
		}
	}
	return s.GetActor(ctx, actorID)
}

func (s *Store) DeleteSession(ctx context.Context, raw string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, HashSecret(raw))
	return err
}

func (s *Store) CreateToken(ctx context.Context, actorID, name string, scopes, projects []string, expires *time.Time) (Token, string, error) {
	return s.createToken(ctx, actorID, actorID, name, scopes, projects, expires)
}

// CreateTokenBy is used by administrator-facing handlers so the event actor
// is the administrator, while the resulting token remains owned by the agent.
func (s *Store) CreateTokenBy(ctx context.Context, actorID, createdBy, name string, scopes, projects []string, expires *time.Time) (Token, string, error) {
	return s.createToken(ctx, actorID, createdBy, name, scopes, projects, expires)
}

func (s *Store) createToken(ctx context.Context, actorID, createdBy, name string, scopes, projects []string, expires *time.Time) (Token, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 200 {
		return Token{}, "", invalid("token name must be between 1 and 200 characters", nil)
	}
	requestedScopes := uniqueStrings(scopes)
	if len(requestedScopes) == 0 {
		return Token{}, "", invalid("at least one token scope is required", nil)
	}
	scopes = normalizeScopes(scopes)
	if len(requestedScopes) != len(scopes) {
		return Token{}, "", invalid("token contains an unsupported scope", nil)
	}
	projects, err := s.resolveProjectIDs(ctx, projects)
	if err != nil {
		return Token{}, "", err
	}
	rawToken, err := randomToken()
	if err != nil {
		return Token{}, "", err
	}
	raw := "rmap_" + rawToken
	scopeJSON, _ := json.Marshal(scopes)
	projectJSON, _ := json.Marshal(projects)
	created := now()
	var expiresValue any
	var expiresString *string
	if expires != nil {
		v := expires.UTC().Format(time.RFC3339Nano)
		expiresValue, expiresString = v, &v
	}
	tokenID := newID()
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO tokens(id, actor_id, name, token_hash, scopes, project_ids, expires_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, tokenID, actorID, name, HashSecret(raw), string(scopeJSON), string(projectJSON), expiresValue, created); err != nil {
			return err
		}
		_, err := insertEvent(ctx, tx, "token.created", createdBy, "", "", map[string]any{"token_id": tokenID, "actor_id": actorID})
		return err
	})
	if err != nil {
		return Token{}, "", err
	}
	var result Token
	row := s.DB.QueryRowContext(ctx, `SELECT id, actor_id, name, scopes, project_ids, expires_at, created_at, last_used_at FROM tokens WHERE token_hash = ?`, HashSecret(raw))
	if err := scanToken(row, &result); err != nil {
		return Token{}, "", err
	}
	// Keep this assignment explicit even if a driver normalizes a timestamp.
	result.ExpiresAt = expiresString
	return result, raw, nil
}

func (s *Store) resolveProjectIDs(ctx context.Context, references []string) ([]string, error) {
	values := uniqueStrings(references)
	result := make([]string, 0, len(values))
	for _, reference := range values {
		var id string
		if err := s.DB.QueryRowContext(ctx, `SELECT id FROM projects WHERE id=? OR slug=? OR key=? LIMIT 1`, reference, strings.ToLower(reference), strings.ToUpper(reference)).Scan(&id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, invalid("project not found: "+reference, nil)
			}
			return nil, err
		}
		result = append(result, id)
	}
	return result, nil
}

func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := cryptorand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func normalizeScopes(scopes []string) []string {
	allowed := map[string]bool{
		"projects:read": true, "projects:write": true, "tasks:read": true, "tasks:write": true,
		"tasks:claim": true, "events:read": true,
	}
	seen := make(map[string]bool)
	result := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope != "" && allowed[scope] && !seen[scope] {
			seen[scope] = true
			result = append(result, scope)
		}
	}
	return result
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func scanToken(scanner interface{ Scan(...any) error }, token *Token) error {
	var scopes, projects string
	var expires, lastUsed sql.NullString
	if err := scanner.Scan(&token.ID, &token.ActorID, &token.Name, &scopes, &projects, &expires, &token.CreatedAt, &lastUsed); err != nil {
		return err
	}
	token.AgentID = token.ActorID
	_ = json.Unmarshal([]byte(scopes), &token.Scopes)
	_ = json.Unmarshal([]byte(projects), &token.ProjectIDs)
	if token.Scopes == nil {
		token.Scopes = []string{}
	}
	if token.ProjectIDs == nil {
		token.ProjectIDs = []string{}
	}
	token.ExpiresAt, token.LastUsedAt = nullableString(expires), nullableString(lastUsed)
	return nil
}

func (s *Store) LookupToken(ctx context.Context, raw string) (AuthToken, error) {
	var token Token
	var actorID string
	var scopes, projects string
	var expires, lastUsed sql.NullString
	row := s.DB.QueryRowContext(ctx, `SELECT t.id, t.actor_id, t.name, t.scopes, t.project_ids, t.expires_at, t.created_at, t.last_used_at FROM tokens t WHERE t.token_hash = ?`, HashSecret(raw))
	if err := row.Scan(&token.ID, &actorID, &token.Name, &scopes, &projects, &expires, &token.CreatedAt, &lastUsed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AuthToken{}, ErrNotFound
		}
		return AuthToken{}, err
	}
	token.ActorID = actorID
	token.AgentID = actorID
	if expires.Valid {
		expiry, err := time.Parse(time.RFC3339Nano, expires.String)
		if err != nil || !expiry.After(time.Now().UTC()) {
			return AuthToken{}, ErrNotFound
		}
		token.ExpiresAt = &expires.String
	}
	token.LastUsedAt = nullableString(lastUsed)
	if err := json.Unmarshal([]byte(scopes), &token.Scopes); err != nil {
		return AuthToken{}, ErrNotFound
	}
	if err := json.Unmarshal([]byte(projects), &token.ProjectIDs); err != nil {
		return AuthToken{}, ErrNotFound
	}
	if token.Scopes == nil {
		token.Scopes = []string{}
	}
	if token.ProjectIDs == nil {
		token.ProjectIDs = []string{}
	}
	actor, err := s.GetActor(ctx, actorID)
	if err != nil {
		return AuthToken{}, err
	}
	if actor.DisabledAt != nil {
		return AuthToken{}, ErrForbidden
	}
	lastUsedCheck := time.Now().UTC()
	if authTimestampNeedsRefresh(lastUsed, lastUsedCheck) {
		usedAt := lastUsedCheck.Format(time.RFC3339Nano)
		// Match the value read above so concurrent stale lookups cannot each
		// turn one authentication into a write. Metadata update failures are
		// intentionally ignored: the token was already authenticated, and a
		// later lookup can refresh last_used_at.
		if lastUsed.Valid {
			_, _ = s.DB.ExecContext(ctx, `UPDATE tokens SET last_used_at = ? WHERE id = ? AND last_used_at = ?`, usedAt, token.ID, lastUsed.String)
		} else {
			_, _ = s.DB.ExecContext(ctx, `UPDATE tokens SET last_used_at = ? WHERE id = ? AND last_used_at IS NULL`, usedAt, token.ID)
		}
	}
	result := AuthToken{Token: token, Actor: actor, Scopes: make(map[string]bool), Projects: make(map[string]bool), ProjectsScoped: len(actor.ProjectIDs) > 0 || len(token.ProjectIDs) > 0}
	for _, scope := range token.Scopes {
		result.Scopes[scope] = true
	}
	if len(actor.ProjectIDs) > 0 {
		allowedByActor := make(map[string]bool, len(actor.ProjectIDs))
		for _, project := range actor.ProjectIDs {
			allowedByActor[project] = true
		}
		for _, project := range token.ProjectIDs {
			if allowedByActor[project] {
				result.Projects[project] = true
			}
		}
		if len(token.ProjectIDs) == 0 {
			for project := range allowedByActor {
				result.Projects[project] = true
			}
		}
	} else {
		for _, project := range token.ProjectIDs {
			result.Projects[project] = true
		}
	}
	return result, nil
}

func authTimestampNeedsRefresh(lastUsed sql.NullString, at time.Time) bool {
	if !lastUsed.Valid {
		return true
	}
	usedAt, err := time.Parse(time.RFC3339Nano, lastUsed.String)
	return err == nil && at.Sub(usedAt) > tokenLastUsedThrottle
}

func (s *Store) ListTokens(ctx context.Context, actorID string) ([]Token, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, actor_id, name, scopes, project_ids, expires_at, created_at, last_used_at FROM tokens WHERE actor_id = ? ORDER BY created_at DESC`, actorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Token, 0)
	for rows.Next() {
		var token Token
		if err := scanToken(rows, &token); err != nil {
			return nil, err
		}
		result = append(result, token)
	}
	return result, rows.Err()
}

func (s *Store) DeleteToken(ctx context.Context, tokenID string) error {
	return s.DeleteTokenBy(ctx, tokenID, "")
}

func (s *Store) DeleteTokenBy(ctx context.Context, tokenID, actorID string) error {
	var owner string
	if err := s.DB.QueryRowContext(ctx, `SELECT actor_id FROM tokens WHERE id=?`, tokenID).Scan(&owner); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if actorID == "" {
		actorID = owner
	}
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `DELETE FROM tokens WHERE id=?`, tokenID)
		if err != nil {
			return err
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			return ErrNotFound
		}
		_, err = insertEvent(ctx, tx, "token.deleted", actorID, "", "", map[string]any{"token_id": tokenID, "actor_id": owner})
		return err
	})
	return err
}
