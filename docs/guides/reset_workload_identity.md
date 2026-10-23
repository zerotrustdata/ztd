---
doc_id: reset_workload_identity
title: Reset Workload Identity
audience: platform operators, administrators, developers
status: current
version: v1.3.0
last_updated: 2026-10-23
owners:
  - docs/guides/reset_workload_identity.md
  - internal/cli/cmd/operator
  - internal/cli/cmd/gw
  - internal/cli/identityreset
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

`./ztd gw clean` and `./ztd gw reset` stop running ztd services, remove ztd root CA anchors from the OS trust store, and rename the `.ztd/` runtime directory to `.ztd-<MMDDHHMM>`. The next Gateway start generates a fresh Certificate Authority (CA). CLI credentials become invalid. Before cleaning, `gw clean` lists recorded local Operator working directories that hold a cached Gateway trust bundle and require `ztd operator reset-identity`.

`make clean` removes `bin/`, `.ztd-test-tmp/`, `.ztd-harness-*/`, test and coverage outputs, and the Go build cache. It does not remove `.ztd/` runtime directories.

## Reset a Stale Operator Identity

To reset an affected Operator working directory explicitly:

```bash
./ztd operator reset-identity --working-dir ~/.ollama/ztd/inference --yes
```

Omit `--yes` to review the actions interactively before confirmation.

The command:
1. Stops local Operators running from that directory.
2. Removes CLI session credentials, the CLI certificate and private key, and the cached Gateway trust bundle.
3. Preserves SQLite databases, vault keys, model files, local configuration, and logs.

Operator process discovery for reset is supported on Linux, macOS, and Windows. It targets the current user's inspectable Operator processes in the specified working directory; processes owned by another user or inaccessible to the current process are skipped. If a matching process cannot be stopped, the reset fails before removing credentials. Gateway runtime directories and symlinked identity paths are refused. An explicit reset clears local state only; it does not communicate revocation to a remote Gateway.

## Re-enroll and Start the Operator

After resetting local identities, sign in to the new Gateway instance. The Gateway MUST already be running (`./ztd gw start`):

```bash
./ztd login
```

Bare `login` creates a CLI-only identity that cannot authenticate to the Console. Run `./ztd login web` instead to install the Gateway root CA and register a passkey for the Console.

Restart the Operator with its required capabilities and working directory. Its enrollment request appears once it starts:

```bash
./ztd operator start --endpoint localhost --capabilities inference --working-dir ~/.ollama/ztd/inference
```

From another terminal, list the pending request and approve it by request ID with `./ztd auth enroll approve <request-id> --yes`:

```bash
./ztd auth enroll pending
```

Verify connection status:

```bash
./ztd operator list
```

## Trust for Remote Operators

For a remote Operator connecting to a newly reset Gateway:

1. Obtain the new Gateway CA bundle, `.ztd/pki/trust/ztdg-ca-bundle.pem` in the Gateway runtime directory, and copy it to the Operator host. Verify it through a channel you trust; `--trust-bundle` checks only that the file parses as PEM certificates and does not verify a fingerprint.
2. Start the Operator with `--trust-bundle /path/to/ztdg-ca-bundle.pem`. The explicit bundle is read, never persisted, and overrides the bundle delivered at enrollment.

Remote working directories are never modified or reset by the local Gateway.

## Links out

- [Connect Operator to Gateway](connect_operator_to_gateway.md) — Operator enrollment and connection workflow.
- [Unified Stack Guide](unified_stack.md) — Single-host execution topology.
- [Authentication Architecture](../architecture/auth.md) — Workload identity and PKI hierarchy.
- [Operator Architecture](../architecture/operator.md) — Operator memory model and local storage.
