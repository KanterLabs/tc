package store

import (
	"context"
	"database/sql"
	"errors"
)

type IdempotencyRecord struct {
	Status           int
	ResponseBody     []byte
	ETag             string
	ResponseLocation string
}

func (s *Store) GetIdempotency(ctx context.Context, actorID, key, method, path, requestHash string) (IdempotencyRecord, bool, error) {
	var record IdempotencyRecord
	var body, etag, location string
	err := s.DB.QueryRowContext(ctx, `SELECT status, response_body, COALESCE(response_etag, ''), COALESCE(response_location, '') FROM idempotency_keys WHERE actor_id=? AND key=?`, actorID, key).Scan(&record.Status, &body, &etag, &location)
	if errors.Is(err, sql.ErrNoRows) {
		return IdempotencyRecord{}, false, nil
	}
	if err != nil {
		return IdempotencyRecord{}, false, err
	}
	var storedHash, storedMethod, storedPath string
	if err := s.DB.QueryRowContext(ctx, `SELECT request_hash, method, path FROM idempotency_keys WHERE actor_id=? AND key=?`, actorID, key).Scan(&storedHash, &storedMethod, &storedPath); err != nil {
		return IdempotencyRecord{}, false, err
	}
	if storedHash != requestHash || storedMethod != method || storedPath != path {
		return IdempotencyRecord{}, true, conflict("idempotency key was already used for a different request", nil)
	}
	record.ResponseBody, record.ETag, record.ResponseLocation = []byte(body), etag, location
	return record, true, nil
}

func (s *Store) SaveIdempotency(ctx context.Context, actorID, key, method, path, requestHash string, record IdempotencyRecord) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO idempotency_keys(actor_id, key, method, path, request_hash, status, response_body, response_etag, response_location, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?)`, actorID, key, method, path, requestHash, record.Status, string(record.ResponseBody), record.ETag, record.ResponseLocation, now())
	if err != nil {
		// A concurrent request may have completed the same key. It is safe to
		// let the caller re-read that canonical response.
		if _, exists, readErr := s.GetIdempotency(ctx, actorID, key, method, path, requestHash); readErr == nil && exists {
			return nil
		}
	}
	return err
}
