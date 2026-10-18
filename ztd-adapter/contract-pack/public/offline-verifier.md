# Public Proof Offline Verification

Follow the [canonical offline proof verification procedure](../../../docs/architecture/public_spectator.md#offline-proof-verification). Before disconnecting, export the mirror's signed `/proof-segments` response as JSONL and download its active content-addressed artifact files. Pin the source's public key through a trusted channel, or pin a key registry for a log spanning key rotations.

Offline, run `ztd public verify-proof --segments proof-segments.jsonl --artifacts ./proofs --public-key <trusted-ed25519-key-hex>` (or use `--key-registry trusted-keys.json`). The verifier checks the signed segment chain from the zero root, applies withdrawals, and verifies active artifact sizes and hashes. A manifest with a nonzero `segment_index` is a derived view whose signature belongs to that segment; verification requires the signed log. Freshness requires an independently trusted latest root.

Legacy full manifests with segment index zero use the legacy ordered manifest-root and Ed25519 verification procedure. Verify signed feed fixtures by recomputing each record hash, recomputing the batch content hash from the ordered protocol fields, and verifying the Ed25519 signature over the decoded content hash. Missing active artifacts, unknown fields, hash or signature mismatches, traversal paths, and symlinks invalidate the package.
