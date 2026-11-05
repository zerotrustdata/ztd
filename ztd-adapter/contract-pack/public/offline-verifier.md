# Public Proof Offline Verification

Follow the [canonical offline proof verification procedure](../../../docs/architecture/public_spectator.md#offline-proof-verification). The public body layout now selects only run bundles and scenario fixtures. Historical private catalog entries have no body path in the bucket, and the current segment verifier requires all active entries. Obtain those historical files separately before using this procedure. Newly digest-only audit databases need the pending pointer-rooted verifier; this procedure cannot authenticate them. Every path below is relative to the feed origin and needs no credentials.

1. `pointer.json`: the signed pointer. It names `proof_segment_head`, `proof_catalog_sha256`, and `proof_root_sha256`.
2. Every `proof-segments/<first_index>.jsonl.gz` chunk, where `first_index` is 1, 1001, 2001, and so on up to `proof_segment_head`. Decompress the chunks and join them in ascending `first_index` order into one JSONL file:

   ```sh
   head=$(jq -r .proof_segment_head pointer.json)
   for ((i = 1; i <= head; i += 1000)); do gunzip -c "proof-segments/$i.jsonl.gz"; done > proof-segments.jsonl
   ```

3. `proof-catalog/<proof_catalog_sha256>.json.gz`, decompressed. It lists the active artifacts. Download run bundles from `runs/<sha256>.jsonl.gz` and scenario input/gold fixtures from `fixtures/<sha256>`. Save each file under its bare SHA-256 in one directory, such as `./proofs`, together with any separately delivered historical artifacts. There is no generic public body route in the bucket.

Offline, run `ztd public verify-proof --segments proof-segments.jsonl --artifacts ./proofs --pointer pointer.json`. The verifier recomputes the signed segment chain from the zero root, checks each Ed25519 signature under that segment's `signing_public_key` (SHA-256 of the key must equal `signing_key_id`), applies withdrawals, and verifies active artifact sizes and hashes. With `--pointer`, the signed pointer must verify and name the chain's source, head segment index, and head root. Pass `--public-key` or `--key-registry trusted-keys.json` to require a key supplied separately. The default command checks the published signatures and hash chain; it does not establish an out-of-band publisher identity.

The `proof-head.json` object contains the latest signing header, including `signing_public_key` when the mirror has it, without artifact entries. A head with a nonzero `segment_index` carries that segment's root and signature; verification requires the signed log. The head's root identifies the advertised log position. It does not independently establish freshness.

For a legacy source with segment index zero, reconstruct its full manifest from the proof head metadata and catalog entries in response order, then use the legacy ordered manifest-root and Ed25519 verification procedure. Verify signed feed fixtures by recomputing each record hash, recomputing the batch content hash from the ordered protocol fields, and verifying the Ed25519 signature over the decoded content hash. Missing active artifacts, unknown fields, hash or signature mismatches, traversal paths, and symlinks invalidate the package.
