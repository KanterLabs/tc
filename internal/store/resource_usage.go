package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Agent mutations reserve a bounded amount of persistent storage. The charge
// includes a fixed allowance for event/idempotency/index overhead in addition
// to the request body, so a small request cannot amplify into an unbounded
// number of rows. These limits are intentionally code constants: changing the
// allowance is an operational policy change, not client-controlled input.
const (
	AgentMutationBudgetBytes   int64 = 64 << 20 // 64 MiB per actor lifetime
	AgentMutationOverheadBytes int64 = 8 << 10  // 8 KiB fixed per mutation
)

var ErrResourceLimit = errors.New("agent mutation resource budget exhausted")

// ReserveAgentMutation atomically charges one mutation to actorID. The
// actor's row is the accounting key, not a bearer token, so all tokens owned
// by one agent share one lifetime budget. A failed reservation leaves both the
// usage row and all domain tables unchanged.
func (s *Store) ReserveAgentMutation(ctx context.Context, actorID string, requestBodyBytes int) error {
	if strings.TrimSpace(actorID) == "" || requestBodyBytes < 0 {
		return ErrInvalid
	}
	charge := AgentMutationOverheadBytes + int64(requestBodyBytes)
	if charge < AgentMutationOverheadBytes || charge > AgentMutationBudgetBytes {
		return ErrResourceLimit
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			INSERT INTO actor_resource_usage(actor_id, reserved_bytes, updated_at)
			VALUES (?, ?, ?)
			ON CONFLICT(actor_id) DO UPDATE SET
				reserved_bytes = actor_resource_usage.reserved_bytes + excluded.reserved_bytes,
				updated_at = excluded.updated_at
			WHERE actor_resource_usage.reserved_bytes + excluded.reserved_bytes <= ?
		`, actorID, charge, now(), AgentMutationBudgetBytes)
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if rows != 1 {
			return ErrResourceLimit
		}
		return nil
	})
}

// AgentMutationUsage reports the persistent reservation for operator status
// checks. An actor with no reservations has used zero bytes.
func (s *Store) AgentMutationUsage(ctx context.Context, actorID string) (int64, error) {
	var used int64
	err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(reserved_bytes, 0) FROM actor_resource_usage WHERE actor_id=?`, actorID).Scan(&used)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return used, err
}

// ResetAgentMutationUsage clears one actor's lifetime reservation. This is an
// operator-only maintenance action; no HTTP route exposes it. The migration
// also includes the equivalent SQL for installations that use a DB console.
func (s *Store) ResetAgentMutationUsage(ctx context.Context, actorID string) error {
	if strings.TrimSpace(actorID) == "" {
		return ErrInvalid
	}
	_, err := s.DB.ExecContext(ctx, `DELETE FROM actor_resource_usage WHERE actor_id=?`, actorID)
	return err
}
