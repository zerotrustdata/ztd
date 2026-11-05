# Public Evidence Offline Verification

Follow the [canonical offline verification procedure](../../../docs/architecture/public_spectator.md#offline-proof-verification). Public evidence is authenticated through a trusted Ed25519 signing key, the signed pointer, its read model, and the settled run bundle named by the selected run's evaluation summary. The browser checks object digests but does not authenticate the pointer signature.

Download `pointer.json`, the `read-model/<read_model_sha256>.json.gz` object it names, and the selected run's `runs/<assignment_bundle_sha256>.jsonl.gz`. Keep the bundle's gzip bytes unchanged: its digest covers the stored file. The read model digest covers decoded JSON; the CLI accepts its gzip object or decoded bytes. Obtain trusted keys separately as a JSON mapping of key ID (or source ID plus colon plus key ID) to hex Ed25519 public key. A registry can contain old and rotated keys; a self-published key alone is never trusted in this mode.

```bash
ztd public verify-proof --pointer pointer.json --read-model read-model.json.gz --bundle run.jsonl.gz --run "$RUN_ID" --key-registry trusted-keys.json
```

The verifier checks the pointer signature and source/sequence/feed-hash/withdrawal identity, hashes the read model and gzip bundle, and then checks each exact record_bytes hash, ordered sequence, run identity, and assignment uniqueness. An absent or withdrawn run has no usable bundle binding in a current read model.

Scenario fixtures remain public at `fixtures/<sha256>`. Full agent traces and assignment audit databases are delivered under agreement. Place any delivered files under their bare SHA-256 in a directory and add `--artifacts ./delivered` to verify each against the authenticated assignment evidence bindings. Missing private files are not required. Unbound files, changed bytes, directories, and symlinks fail verification.

For an assignment audit database, use the same trust inputs:

```bash
ztd public verify-assignment --db assignment.db --pointer pointer.json --read-model read-model.json.gz --bundle run.jsonl.gz --run "$RUN_ID" --key-registry trusted-keys.json
```

This additionally requires an assignment_audit_slice digest binding, verifies the database's live-event record hashes, and matches its source, run, and assignment identity. Database verification does not independently verify every governed execution receipt. The database is plaintext SQLite and needs no vault key.

The pointer authenticates a captured state; it does not prove that a capture is the latest state. A previously downloaded pointer and bundle can still verify after withdrawal. Remote mirrors without a source signing key serve unsigned pointers and cannot supply this trust root. Bounded history remains public for signed feed-chain checks, subject to retention and withdrawals. The internal proof log is no longer an anonymous endpoint or bucket projection. The separate `verify-proof --segments` mode remains for an explicitly obtained internal log and its active files.
