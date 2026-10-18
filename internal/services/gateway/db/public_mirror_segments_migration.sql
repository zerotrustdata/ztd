-- One-time upgrade of the original SQLite proof projection. Index zero
-- retains the actual signed legacy manifest until the source sends segment 1.
BEGIN IMMEDIATE;
ALTER TABLE proof_manifest_entries RENAME TO proof_manifest_entries_v1;
CREATE TABLE proof_manifest_entries (
    source_id TEXT NOT NULL,
    segment_index INTEGER NOT NULL DEFAULT 0,
    ordinal INTEGER NOT NULL,
    artifact_id TEXT NOT NULL REFERENCES proof_artifacts(artifact_id),
    entry_json TEXT NOT NULL,
    PRIMARY KEY (source_id, segment_index, ordinal)
);
INSERT INTO proof_manifest_entries (source_id, segment_index, ordinal, artifact_id, entry_json)
SELECT source_id, 0, ordinal, artifact_id, entry_json FROM proof_manifest_entries_v1;
DROP TABLE proof_manifest_entries_v1;
CREATE INDEX idx_public_mirror_manifest_entries_artifact ON proof_manifest_entries(artifact_id);
CREATE INDEX idx_public_mirror_manifest_entries_source_artifact ON proof_manifest_entries(source_id, artifact_id);
COMMIT;
