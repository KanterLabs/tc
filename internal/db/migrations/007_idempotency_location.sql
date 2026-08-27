-- Persist the Location header emitted by successful create mutations so an
-- idempotent replay is byte-for-byte equivalent at the HTTP boundary.
ALTER TABLE idempotency_keys ADD COLUMN response_location TEXT;
