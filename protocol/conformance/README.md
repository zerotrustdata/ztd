# Protocol Conformance Vectors

Shared test vectors that pin the canonical transaction hash for governance
envelopes. Any implementation of the hash must reproduce these digests
exactly.

## Files

| File | Consumer | What it pins |
| --- | --- | --- |
| `hash_vectors.json` | `internal/governance/envelope_hash_parity_test.go` (`TestHashParity_VectorFile`) | Expected SHA-256 digests of `GenerateMessageID` for the standard envelope field set, including nested intent, Unicode, empty payload and intent, omitted optional fields, timestamp normalization, and the bound-context fields (`bound_*` vectors) |
| `hash_vectors_v2.json` | `internal/governance/envelope_hash_parity_test.go` (`TestHashParity_V2VectorFile`) | Expected digests for protocol version 2 envelopes |

Each vector lists the envelope fields and the expected hex digest. Go tests
decode `payload_b64` to `[]byte` before hashing.

The JSON files in `protocol/constants/` and `protocol/models/` are the source
of truth for constants and model shapes; their Go mirrors are verified by the
contract tests in `internal/constants/*_test.go`.

## Running

```bash
# From the repository root
./ztd test unit --pkg ./internal/governance --run TestHashParity
```

The vectors also run as part of `./ztd test unit` and the CI unit-test job.
Change a vector only together with a deliberate, documented change to the
canonical hash specification in `protocol/docs/spec.md`.
