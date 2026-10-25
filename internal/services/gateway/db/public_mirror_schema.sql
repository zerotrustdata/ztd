-- Public Mirror SQLite Schema
-- Canonical schema for the Gateway-owned public spectator mirror store
-- (`.ztd/public-mirror/mirror.db`). Embedded into `public_mirror_store.go` via
-- `//go:embed db/public_mirror_schema.sql` and applied on open by
-- `OpenPublicMirrorStore`. This file is the SINGLE source of truth for the
-- mirror schema - do not duplicate it elsewhere.
--
-- Every row holds public, already-signed data. Proof artifact bytes live in
-- content-addressed files under `public-mirror/proofs/<sha256>`; this store
-- holds only their identity and size.

-- Singleton mirror-wide state.
CREATE TABLE IF NOT EXISTS mirror_state (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    active_source_id TEXT NOT NULL DEFAULT ''
);

-- Per-source summary of the accepted signed batch chain.
CREATE TABLE IF NOT EXISTS sources (
    source_id TEXT PRIMARY KEY,
    high_water_sequence INTEGER NOT NULL,
    feed_chain_hash TEXT NOT NULL,
    batch_count INTEGER NOT NULL,
    retained_from_sequence INTEGER NOT NULL,
    retained_previous_batch_hash TEXT NOT NULL,
    freshness TEXT NOT NULL,
    last_accepted_at TEXT NOT NULL DEFAULT '',
    retained_batch_count INTEGER NOT NULL DEFAULT 0,
    retained_record_count INTEGER NOT NULL DEFAULT 0,
    retained_record_bytes INTEGER NOT NULL DEFAULT 0
);

-- Retained signed batches. Record hashes are reconstructed from records.
CREATE TABLE IF NOT EXISTS batches (
    source_id TEXT NOT NULL REFERENCES sources(source_id),
    first_sequence INTEGER NOT NULL,
    last_sequence INTEGER NOT NULL,
    record_count INTEGER NOT NULL DEFAULT 0,
    byte_size INTEGER NOT NULL DEFAULT 0,
    protocol_version TEXT NOT NULL,
    schema_version TEXT NOT NULL,
    previous_batch_hash TEXT NOT NULL,
    generated_at TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    signing_key_id TEXT NOT NULL,
    signature TEXT NOT NULL,
    PRIMARY KEY (source_id, first_sequence)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_public_mirror_batches_last ON batches(source_id, last_sequence);

-- Retained public feed records.
CREATE TABLE IF NOT EXISTS records (
    source_id TEXT NOT NULL REFERENCES sources(source_id),
    sequence INTEGER NOT NULL,
    record_type TEXT NOT NULL,
    record_hash TEXT NOT NULL,
    record_bytes TEXT NOT NULL,
    PRIMARY KEY (source_id, sequence)
);

-- Assignment index over retained event records: one row per event record,
-- naming the run and assignment its body carries. Rows are written and pruned
-- with their records in the same transaction, so they never outlive them.
CREATE TABLE IF NOT EXISTS assignment_event_records (
    source_id TEXT NOT NULL,
    sequence INTEGER NOT NULL,
    run_id TEXT NOT NULL,
    assignment_id TEXT NOT NULL,
    PRIMARY KEY (source_id, sequence)
);

CREATE INDEX IF NOT EXISTS idx_public_mirror_assignment_event_records_assignment_sequence ON assignment_event_records(source_id, run_id, assignment_id, sequence);

-- Current records outlive replay-history retention. Their original signed
-- batches remain while referenced, preserving independently verifiable bytes.
CREATE TABLE IF NOT EXISTS snapshot_batches (
    source_id TEXT NOT NULL REFERENCES sources(source_id),
    first_sequence INTEGER NOT NULL,
    batch_json TEXT NOT NULL,
    PRIMARY KEY (source_id, first_sequence)
);

CREATE TABLE IF NOT EXISTS snapshot_records (
    source_id TEXT NOT NULL,
    record_key TEXT NOT NULL,
    dataset_id TEXT NOT NULL,
    sequence INTEGER NOT NULL,
    first_sequence INTEGER NOT NULL,
    is_event INTEGER NOT NULL,
    record_type TEXT NOT NULL,
    record_hash TEXT NOT NULL,
    record_bytes TEXT NOT NULL,
    PRIMARY KEY (source_id, record_key),
    FOREIGN KEY (source_id, first_sequence) REFERENCES snapshot_batches(source_id, first_sequence)
);
CREATE INDEX IF NOT EXISTS idx_public_mirror_snapshot_batch ON snapshot_records(source_id, first_sequence);
CREATE INDEX IF NOT EXISTS idx_public_mirror_snapshot_sequence_dataset ON snapshot_records(source_id, sequence, dataset_id);
CREATE INDEX IF NOT EXISTS idx_public_mirror_snapshot_events_covering ON snapshot_records(source_id, is_event, sequence, first_sequence, record_key);

CREATE TABLE IF NOT EXISTS snapshot_heads (
    source_id TEXT PRIMARY KEY REFERENCES sources(source_id),
    high_water_sequence INTEGER NOT NULL
);

-- Source signing keys, keyed "<source_id>:<key_id>".
CREATE TABLE IF NOT EXISTS key_registry (
    registry_id TEXT PRIMARY KEY,
    public_key BLOB NOT NULL
);

CREATE TABLE IF NOT EXISTS revoked_keys (
    registry_id TEXT PRIMARY KEY,
    revoked_at TEXT NOT NULL
);

-- Campaign datasets hidden from anonymous reads without touching the chain.
CREATE TABLE IF NOT EXISTS withdrawn_datasets (
    source_id TEXT NOT NULL REFERENCES sources(source_id),
    dataset_id TEXT NOT NULL,
    PRIMARY KEY (source_id, dataset_id)
);

-- Content-addressed proof artifacts stored at public-mirror/proofs/<artifact_id>.
CREATE TABLE IF NOT EXISTS proof_artifacts (
    artifact_id TEXT PRIMARY KEY,
    byte_size INTEGER NOT NULL,
    stored_at TEXT NOT NULL
);

-- Signed proof manifest per source. manifest_json carries every header field;
-- its artifacts list is held row by row in proof_manifest_entries.
CREATE TABLE IF NOT EXISTS proof_manifests (
    source_id TEXT PRIMARY KEY,
    manifest_json TEXT NOT NULL,
    proof_root_sha256 TEXT NOT NULL,
    artifact_count INTEGER NOT NULL,
    total_byte_size INTEGER NOT NULL,
    generated_at TEXT NOT NULL
);

-- Signed chained proof manifest segments per source.
CREATE TABLE IF NOT EXISTS proof_manifest_segments (
    source_id TEXT NOT NULL,
    segment_index INTEGER NOT NULL,
    previous_root TEXT NOT NULL,
    proof_root_sha256 TEXT NOT NULL,
    signing_key_id TEXT NOT NULL,
    signature TEXT NOT NULL,
    segment_json TEXT NOT NULL,
    generated_at TEXT NOT NULL,
    PRIMARY KEY (source_id, segment_index)
);

-- Manifest entries keyed by segment and ordinal within segment.
CREATE TABLE IF NOT EXISTS proof_manifest_entries (
    source_id TEXT NOT NULL,
    segment_index INTEGER NOT NULL DEFAULT 0,
    ordinal INTEGER NOT NULL,
    artifact_id TEXT NOT NULL REFERENCES proof_artifacts(artifact_id),
    entry_json TEXT NOT NULL,
    PRIMARY KEY (source_id, segment_index, ordinal)
);

CREATE INDEX IF NOT EXISTS idx_public_mirror_manifest_entries_artifact_position ON proof_manifest_entries(artifact_id, source_id, segment_index, ordinal);
CREATE INDEX IF NOT EXISTS idx_public_mirror_manifest_entries_source_artifact_position ON proof_manifest_entries(source_id, artifact_id, segment_index, ordinal);

-- Withdrawn artifact IDs recorded by signed withdrawal segments.
CREATE TABLE IF NOT EXISTS proof_withdrawn_artifacts (
    source_id TEXT NOT NULL,
    artifact_id TEXT NOT NULL,
    segment_index INTEGER NOT NULL,
    withdrawn_at TEXT NOT NULL,
    PRIMARY KEY (source_id, artifact_id)
);
