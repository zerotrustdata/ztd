---
doc_id: reset_workload_identity
title: Reset Workload Identity
audience: platform operators, administrators, developers
status: current
version: v1.0.0
last_updated: 2026-10-12
owners:
  - docs/guides/reset_workload_identity.md
  - internal/cli/cmd/operator
  - internal/cli/cmd/gw
related:
  - docs/guides/connect_operator_to_gateway.md
  - docs/guides/unified_stack.md
  - docs/architecture/auth.md
  - docs/architecture/operator.md
when_to_read: Recovering Operator and CLI workload credentials after a Gateway CA or identity reset, or clearing stale local enrollment credentials.
do_not_use_for:
  - Operator initial bootstrap and connection (docs/guides/connect_operator_to_gateway.md)
  - Gateway reset and cleanup commands (docs/guides/build_gateway.md)
  - Authentication architecture and protocol details (docs/architecture/auth.md)
---

# Reset Workload Identity

## Overview

When the Gateway runtime is reset via `./ztd gw clean` or `./ztd gw reset`, the Gateway moves its `.ztd/` directory to `.ztd-<MMDDHHMM>` and generates a fresh Certificate Authority (CA) on restart. Existing Operator credentials and cached trust bundles belong to the previous CA and cannot authenticate to the new Gateway instance.

`make clean` removes only build artifacts and the Go build cache; it preserves Gateway state and workload identities.

## Reset a Stale Operator Identity

To reset an affected Operator working directory explicitly:

```bash
./ztd operator reset-identity --working-dir ~/.ztd-inference --yes
```

Omit `--yes` to review the actions interactively before confirmation.

The command:
1. Stops local Operator workers running from that directory.
2. Removes CLI session credentials, the CLI certificate and private key, and cached Gateway trust bundles.
3. Preserves SQLite databases, vault keys, model files, local configuration, and logs.

Worker process discovery for reset is supported on Linux. Gateway runtime directories and symlinked identity paths are refused. An explicit reset clears local state only; it does not communicate revocation to a remote Gateway.

## Re-enroll and Start the Operator

After resetting local identities, sign in to the new Gateway instance (`./ztd login web` registers a passkey for the Console):

```bash
./ztd login
```

Restart the Operator with its required capabilities and working directory. Its enrollment request appears once it starts:

```bash
./ztd operator start --endpoint localhost --capabilities inference --working-dir ~/.ztd-inference
```

From another terminal, approve the pending request:

```bash
./ztd auth enroll pending
./ztd auth enroll approve <id> --yes
```

Verify connection status:

```bash
./ztd operator list
```

## Trust for Remote Operators

A TLS failure during enrollment does not authorize replacing a CA. Workloads retain existing trust during enrollment. For a remote Operator connecting to a newly reset Gateway:
1. Obtain the new Gateway CA bundle (`.ztd/pki/trust/ztdg-ca-bundle.pem`) through an authenticated out-of-band channel.
2. Verify its SHA-256 fingerprint.
3. Start the Operator with `--trust-bundle /path/to/ztdg-ca-bundle.pem`.

Remote working directories are never modified or reset by the local Gateway.

## Links out

- [Connect Operator to Gateway](connect_operator_to_gateway.md) — Operator enrollment and connection workflow.
- [Unified Stack Guide](unified_stack.md) — Single-host execution topology.
- [Authentication Architecture](../architecture/auth.md) — Workload identity and PKI hierarchy.
- [Operator Architecture](../architecture/operator.md) — Operator memory model and local storage.
