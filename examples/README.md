# ztd Examples

Runnable examples, client configuration templates, and reference shapes for integrating with ztd. This is the home for integration examples; complete fictional business sandboxes live under [demos](../demos/README.md), and evaluation campaigns live under [`eval/`](../eval/README.md).

## Index

| Directory | Kind | Description | Run |
| --- | --- | --- | --- |
| [`external-console/`](external-console/README.md) | Browser app | Static SPA that talks directly to a local Gateway (WebAuthn, SSE, approvals) | `npx --yes http-server examples/external-console -a 127.0.0.1 -p 3003 -c-1` |
| [`governance-envelope/`](governance-envelope/main.go) | Go program | Builds, signs, serializes, and parses a `GovernanceEnvelope` | `go run ./examples/governance-envelope/` |
| [`workload-identity/`](workload-identity/main.go) | Go program | Generates, matches, extracts, and parses ztd SPIFFE workload identities | `go run ./examples/workload-identity/` |
| [`mcp-client-configs/`](mcp-client-configs/) | JSON templates | MCP client configurations for governed stdio and agent launch | Copy and edit |
| [`eval/`](eval/) | JSON template | Minimal init-campaign queue shape | Reference only |

Run the Go examples from the repository root: the governance envelope example imports the repository's internal transaction-hash implementation.

## Governance Envelope

The governance envelope example demonstrates these operations:

1. Generates ephemeral Ed25519 keys for an L2 consensus signer and an operator, then derives a mock CLI certificate fingerprint.
2. Constructs a representative `GovernanceEnvelope` with identity, intent, state, application context, L1 metadata, one L2 vote, and a CLI/mTLS L3 proof.
3. Marshals a `CommandRequested` protobuf message into the envelope's `payload` bytes.
4. Computes the transaction hash from the hash-bound envelope fields with `governance.GenerateMessageID`.
5. Signs `<transaction_hash>|true` for the L2 vote, signs the transaction hash for the L3 CLI proof, and assigns the hash to both `id` and `transaction_hash`.
6. Serializes the envelope with protojson, parses it back, and unmarshals the command payload.

The program uses generated keys, mock identity values, and a mock certificate fingerprint; it does not submit the envelope to a gateway or run the five-layer verification sequence. The canonical envelope schema is `protocol/proto/ztd/common/v1/common.proto`, and the transaction-hash implementation is `internal/governance/envelope.go`.

## MCP Client Configurations

The `mcp-client-configs/` directory contains two templates:

| File | Transport | Use case |
| --- | --- | --- |
| `ztd_stdio_mcp_config.json` | Stdio subprocess | Start `ztd mcp stdio`, which proxies requests to a running gateway over mTLS and applies L1 through L5 governance |
| `ztd_agent_mcp_config.json` | Stdio subprocess plus native-tool exclusions | Show the temporary JSON shape used when `ztd mcp agent run` launches Claude or Codex, where `--app claude` selects the agent's owner-approved application identity; the command also applies strict MCP and native-tool-disabling launch flags |

The Go types that produce the stdio configuration are in `internal/services/mcp/config.go`. Agent-specific configuration writers and launch arguments are in `internal/cli/cmd/mcp/`; Goose, Gemini, and Devin use different configuration formats or tool-disabling mechanisms from the agent JSON template.

Use `ztd mcp agent list` to list supported agents. `ztd mcp agent show <agent>` validates a supported agent name and prints the stdio configuration. Identity keys are sealed under the keystore master key, so no template carries key paths; `ztd mcp stdio` unseals the identity in process. `ztd mcp agent run <agent>` starts the gateway when necessary, enrolls the CLI and agent identities, configures the agent to use `ztd mcp stdio`, and launches the agent with L1 through L5 governance. Third-party MCP servers are governed through Gateway downstream egress.

See the [MCP protocol documentation](../protocol/docs/mcp.md) for transport behavior, authentication, and governance details.

## Workload Identity

The workload identity example demonstrates these operations in the `ztd.local` trust domain:

- Generates operator, CLI, application, hub, user, and gateway peer SPIFFE IDs.
- Matches complete identities and CLI sessions, and recognizes application and user SAN identities.
- Extracts CLI session IDs, CLI and user-SAN user IDs, operator session IDs, and gateway IDs.
- Produces parsed `url.URL` values for operator, CLI, application, user, hub, and gateway peer identities.

See the [protocol overview](../protocol/README.md#workload-identities) for the identity formats.

## Eval

`eval/init-campaign-queue.example.json` is a minimal queue shape reference. The full checked-in evaluation data layout is documented in [`eval/README.md`](../eval/README.md).
