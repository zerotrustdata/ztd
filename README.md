# ztd

A zero-trust execution layer for AI agents and the infrastructure they touch.

ztd lets AI systems propose actions without giving them direct control over real systems. The model requests an action. The machine that owns the workload decides.

[![License](https://img.shields.io/badge/license-BSL%201.1-blue.svg)](LICENSE)
[![CI](https://github.com/zerotrustdata/ztd/actions/workflows/build-and-test.yml/badge.svg)](https://github.com/zerotrustdata/ztd/actions/workflows/build-and-test.yml)

The current version is **v1.0.2**. See the [release notes](docs/release_notes/v1.0.x/v1.0.2.md) for changes and validation results.

## What ztd does

ztd sits between an AI client and the systems it might affect. It converts actions into governed, signed, auditable transactions. An agent does not connect directly to the host.

The core model:

- AI systems express intent as typed actions.
- A gateway screens and authorizes that intent.
- A local operator verifies the intent on the target host.
- Execution runs under explicit policy, and evidence is recorded at the execution boundary.

This provides:

- No direct host control by an LLM.
- Typed actions instead of freeform shell access.
- Policy gates before mutation.
- Signed receipts and audit trails for executed actions.

## Why this exists

Many AI agent stacks combine four functions in one process:

- reasoning
- authorization
- execution
- audit

ztd separates them:

- the model proposes
- the gateway governs
- the operator executes locally
- evidence is recorded at the execution boundary

ztd applies to AI-assisted infrastructure, operations, compliance, tooling, and distributed systems where the AI must not hold unrestricted authority.

## The short version

ztd is a control plane for AI-driven operations.

A client or agent submits a typed intent. The gateway validates policy, applies human or quorum-based approvals when required, and sends the action through an outbound-only connection to a local operator. The operator independently verifies the request, executes only the approved action, and writes its own signed evidence.

ztd is a governance boundary, not a sandbox.

## Try it

Two entry points are available.

### 1. Run the platform locally (recommended)

The default workflow runs natively on the host:

```bash
git clone https://github.com/zerotrustdata/ztd.git
cd ztd
bash scripts/linux-setup.sh --build-only   # macOS: bash scripts/macos-setup.sh --build-only
make quickstart
```

The setup script checks for Git, Make, curl, and Go 1.27.2, then builds `./ztd`. `make quickstart` starts a Gateway on localhost, signs in, and runs the first governed action. On Linux without a Secret Service keyring or ssh-agent, pass a provisioned master key with `QUICKSTART_ARGS='--master-key-file /abs/path/master.key'`. Rerunning it is safe.

The Gateway-only track requires Git, Go 1.27.2, and Make. Fresh clones use the committed Console and Evaluation Explorer embeds. Node.js 22.22.2+, 24.15+, or 26+ and npm are required only to rebuild those frontends. This track has no Ollama or model SDK requirement.

For setup details, see the [Getting Started guide](docs/guides/getting_started.md#native-host-build).

To run the Gateway with its embedded Operator carrying the Data, Inference, and Observer capabilities, use `make gw`. Pass the Ollama endpoint for the Inference capability through `GW_ARGS`. Set `MODEL_STORAGE_ROOT` to the local model store to add the Provenance capability:

```bash
make gw GW_ARGS='--inference-ollama-endpoint http://<ollama-host>:11434'
make status
```

`make status` shows the Gateway and connected Operators. Stop all services with `make down`. Run `./ztd gw start --help` and `./ztd operator start --help` to list every flag.

Sign in as the owner and approve workloads:

```bash
./ztd login web
./ztd auth enroll pending
./ztd auth enroll approve <request-id> --yes
```

On a headless machine, use `./ztd login`. This creates a CLI-only mTLS identity. It does not register a passkey, install OS trust, or enable browser console sign-in.

Sign in at `https://localhost:8443/console/` with a passkey.

### 2. Explore the protocol and evidence model

The repository includes a protocol layer and example integrations in Go.

```bash
go get github.com/zerotrustdata/ztd@v1.0.2
```

## What is in the repo

This repository includes the main platform and supporting components:

- `cmd/` — CLI entrypoints
- `internal/` — gateway, operator, storage, governance, and runtime services
- `protocol/` — protobuf contracts and canonical models
- `console/` — browser console frontend
- `eval/` — evaluation and compliance harnesses
- `docs/` — architecture, guides, and reference material

## Contributing

For local setup, test selection, and pull request guidelines, see [Contributing](.github/CONTRIBUTING.md).

Contribution routes:

- Run the project locally and report issues.
- Run an evaluation or deployment workflow and document any unclear steps.
- Review the docs and architecture for gaps.
- Build examples for real use cases.
- Propose security or UX patterns.

For feedback and bug report format, see [Feedback and bug reports](EARLY_TESTERS.md).

## License

Business Source License 1.1 ([LICENSE](LICENSE)), licensed by Lateralus Labs, LLC. You may copy, modify, redistribute, and make non-production use of ztd. Production use requires a commercial license from Lateralus Labs. Each version converts to Apache 2.0 on the earlier of 2030-08-18 and four years after that version's public release. Third-party components are listed in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

## More information

For deeper detail, start here:

- [docs/architecture/overview.md](docs/architecture/overview.md)
- [docs/guides/getting_started.md](docs/guides/getting_started.md)
- [docs/architecture/agents.md](docs/architecture/agents.md)
- [docs/architecture/evals.md](docs/architecture/evals.md)
