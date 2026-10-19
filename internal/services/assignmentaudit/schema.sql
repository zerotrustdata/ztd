-- Assignment audit database schema.
-- Canonical schema for the plaintext per-assignment audit database built by
-- internal/services/assignmentaudit. Embedded into database.go via
-- `//go:embed schema.sql` and applied once to a fresh file. This file is the
-- SINGLE source of truth for the schema - do not duplicate it elsewhere.
--
-- The database holds public feed live-event records for one assignment,
-- exactly as the Gateway stored them. It has no encryption, no vault header,
-- and no internal hash chain: the Gateway's signed proof segment authenticates
-- the whole file by its SHA-256.

-- The one assignment this database covers.
CREATE TABLE assignment (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    source_id TEXT NOT NULL,
    campaign_id TEXT NOT NULL,
    campaign_revision TEXT NOT NULL,
    run_id TEXT NOT NULL,
    assignment_id TEXT NOT NULL
);

-- One public feed record per row, keyed by its feed sequence.
CREATE TABLE events (
    sequence INTEGER PRIMARY KEY,
    record_type TEXT NOT NULL,
    record_hash TEXT NOT NULL,
    record_bytes TEXT NOT NULL
);
