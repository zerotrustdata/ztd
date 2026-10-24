# ztd Protocol

The protocol directory contains the shared wire contracts, external constant registries, model schemas, workload identity helpers, generated language bindings, canonicalization vectors, and integration specifications for ztd-compatible clients and services.

## Packages

### Go

The Go protocol packages are part of the root module `github.com/zerotrustdata/ztd`, declared in the repository's `go.mod` with `go 1.27.3`. Import `github.com/zerotrustdata/ztd/protocol` for workload identity helpers or a generated package under `github.com/zerotrustdata/ztd/protocol/proto/ztd/.../v1` for protobuf messages and services.

### TypeScript

`protocol/node/` contains private generated TypeScript protobuf artifacts for repository consumers. It is not a published package. The generated modules use `@bufbuild/protobuf` and cover the common, compliance, eval, operator, pub/sub, and tribunal schemas.

## Sources of Truth

The protocol has several source types with distinct responsibilities:

- `proto/ztd/` defines canonical protobuf messages, enums, field options, and gRPC services.
- `internal/constants/` defines the Go protocol constants. The JSON registries in `constants/` are external protocol definitions and reference data; contract tests keep mirrored Go values aligned with them.
- `models/` defines JSON wire shapes used outside protobuf surfaces.
- `schemas/oscal/` embeds the authenticated NIST OSCAL 1.1.2 assessment-results schema and its provenance for offline validation.
- `vectors/` define shared canonicalization examples consumed by cross-language tests, and `conformance/hash_vectors.json` and `conformance/hash_vectors_v2.json` define transaction-hash examples consumed by the Go test `internal/governance/envelope_hash_parity_test.go`. Vectors are executable examples derived from another source of truth, not schemas or parallel message descriptors. Evaluation vectors with a `canonical_json` field exercise canonical protobuf JSON; `vectors/eval/agent_trace.json` pins the canonical JSON of the agent system-under-test trace (`AgentTrace`), which an external SUT produces; the `ScoredTurnRequest` and `ScoredTurnEvent` messages and their vector no longer exist; `vectors/eval/canonical_json_numbers.json` pins how that encoder writes numbers and escaped strings so independent implementations agree byte for byte (an integral float such as `0.0` is `0`).

## Directory Map

- `proto/`: Protobuf schemas and generated Go code.
- `constants/`: External JSON registries, doctrine definitions, compliance catalogs, and compliance path definitions.
- `models/`: JSON model schemas.
- `schemas/`: Authenticated third-party schemas and provenance used by protocol consumers.
- `node/`: Generated TypeScript protobuf modules and their generation configuration.
- `docs/`: The [protocol specification](docs/spec.md) (envelope and L1 to L3 proof wire contract), MCP, A2A, constants, and generated protobuf reference documentation.
- `conformance/`: Shared transaction-hash vectors (`hash_vectors.json`, `hash_vectors_v2.json`, consumed by `internal/governance/envelope_hash_parity_test.go`) and their conformance guide.
- `test-fixtures/`: Shared JSON fixtures consumed by Go and console tests, such as `operator-document.json`.
- `vectors/`: Cross-language receipt, persistence-attestation, compliance, and evaluation canonicalization vectors.

## Protocol Surfaces

### Protobuf

The protobuf module is `buf.build/ztd/platform`, configured in `proto/buf.yaml`.

- `ztd/common/v1/common.proto` defines governance envelopes and metadata, component identifiers, command intent, and platform enrollment messages.
- `ztd/operator/v1/operator.proto` defines the `OperatorService`, operator request and result payloads, execution and governance status enums, deterministic stage evidence, event receipts, commitment attestations, and persistence attestations.
- `ztd/pubsub/v1/pubsub.proto` defines the shared pub/sub message and event envelopes.
- `ztd/compliance/v1/compliance.proto` defines compliance catalogs, assessment scope and evidence, control assessments, trust policies, signed report bundles, verification results, analysis records, framework profiles.
- `ztd/eval/v1/eval.proto` defines eval-native campaign specs, provider-boundary observation, storage-side model provenance, bundle verification, publication metadata, and agent trace, conformance, and containment records.
- `ztd/tribunal/v1/tribunal.proto` defines the Tribunal intent request and result messages, stage and outcome enums, and member-call records.

Generated Go code lives beside each schema, generated TypeScript modules live under `node/src/gen/`, and generated Markdown references live under `docs/reference/api/`.

### Governance and receipts

`GovernanceEnvelope` carries identity, intent, replay-protection, state, and governance proof data for governed mutations. The [protocol specification](docs/spec.md) defines its wire contract and canonical transaction hash. The [governance architecture](../docs/architecture/governance.md) describes how the Gateway and Operator apply the pipeline.

The execution path uses five interlocked layers:

1. **L1 Doctrine** applies hard gates, forbidden-pattern matching, and MITRE threat detection.
2. **L2 Consensus** verifies K-of-N Ed25519 quorum signatures over `<transaction_hash>|<decision>` under `consensus` and `notary` postures, for state-changing actions only. Bootstrap actions such as platform enrollment are exempt.
3. **L3 Notary** verifies human authorization through WebAuthn or signed CLI proofs under `ratify` and `notary` postures, for mutations, excluding actions the registry marks `AuthorizedByDecision`.
4. **L4 Warden** performs pre-dispatch transaction-hash recompute, expiry, nonce reservation for replay protection, state-root, and posture-required L2 and L3 proof checks.
5. **L5 Actuator** dispatches the registered action handler, mints just-in-time capabilities, and produces signed receipts.

### Constants and models

The top-level JSON registries cover events, event dashboard classification, statuses, collections, API paths, authentication values, headers, key-value keys, channels, pub/sub values, intents, prompts, agents, platform settings and enrollment, senders, exit codes, field paths, document identifiers, network values, output formats, ports, timestamps, environment variables, compliance paths, and platform enrollment transcript vectors. `constants/doctrine/` contains L1 detection and allowlist registries. `constants/compliance/` contains digest-verified assertion, framework, and FedRAMP-to-NIST crosswalk catalogs exposed to Go through `constants/compliance/catalogs.go`.

The JSON schemas in `models/` cover persisted records, runtime and security configuration, governance data, filesystem and execution results, SSE payloads and wire messages, observe API read and producer models, observe event payloads, public spectator feed batches and proofs, platform enrollment, WebAuthn data, and per-agent definitions under `models/agents/`. `schemas/oscal/` provides the official NIST OSCAL 1.1.2 assessment-results schema with pinned SHA-256 and provenance metadata for offline, fail-closed validation. See the [constants reference](docs/constants.md) for registry conventions.

### Workload identities

The Go `protocol` package generates, matches, and extracts SPIFFE identities in the `ztd.local` trust domain:

- Operator: `spiffe://ztd.local/operator/<organization_id>/<operator_id>/<operator_session_id>`
- CLI: `spiffe://ztd.local/cli/<user_id>/<cli_session_id>`
- Application: `spiffe://ztd.local/app/<operator_id>`
- User: `spiffe://ztd.local/user/<user_id>`
- Hub: `spiffe://ztd.local/hub/operator-listen`
- Gateway peer: `spiffe://ztd.local/gateway/<gateway_id>`

Application identities use the generic `app/<operator_id>` form. See the [workload identity example](../examples/workload-identity/main.go) for generation, matching, extraction, and URL parsing.

### MCP and A2A

The [MCP specification](docs/mcp.md) defines governed tool, resource, and prompt integration. The [A2A specification](docs/a2a.md) defines typed agent-to-agent skill invocation. Reference JSON schemas and the upstream A2A protobuf are stored beside these specifications, while deployable MCP configuration templates live in `examples/mcp-client-configs/`.

## Generation

Run generation from the repository root:

- `make proto-generate` regenerates Go protobuf messages, TypeScript protobuf modules, and generated Markdown API references.
- `make proto-go` regenerates Go protobuf outputs and Markdown API references.
- `make proto-node` installs the Node generator when needed and regenerates TypeScript protobuf outputs.

The root `buf.gen.yaml` configures Go and Markdown generation. `node/buf.gen.yaml` configures TypeScript generation.

## Verification

Run the checks that correspond to the changed protocol surface:

- `make -C protocol test` runs the Go protocol tests with race detection.
- `make -C protocol vet` runs Go static analysis for the protocol package.
- `npm ci --prefix protocol/node && npm --prefix protocol/node run typecheck` verifies the generated TypeScript modules.

See the [conformance test guide](conformance/README.md) for the contracts covered by the cross-language suite.

## Examples

The [examples overview](../examples/README.md) describes the Go governance-envelope and workload-identity programs, and the MCP configuration templates.

## Versioning

The protocol packages share the repository version in `VERSION`. Release version bumps follow `INV-REL-VER-10` in the [release process](../docs/devs/release_process.md). `make version-sync` sets the `Version:` headers of the specifications in `protocol/docs/` to match `VERSION`, and `make version-sync-check` verifies them.

## Documentation Contributions

Follow the repository [documentation guide](../docs/devs/docs.md) when changing protocol prose, generated API references, schemas, registries, examples, or package documentation. Protocol changes require coordinated updates across every affected language binding, consumer, conformance test, and canonical reference.

## License

The protocol is licensed under the Business Source License 1.1 and converts to Apache License 2.0 on 2030-08-18. See `LICENSE`.

## Support

Open protocol questions and issue reports at https://github.com/zerotrustdata/ztd.
