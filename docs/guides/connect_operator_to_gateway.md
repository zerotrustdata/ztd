---
doc_id: connect_operator_to_gateway
title: Connect Operator to Gateway
audience: platform operators and deployment engineers
status: current
version: v1.5.0
last_updated: 2026-10-29
owners:
  - docs/guides/connect_operator_to_gateway.md
  - internal/cli/cmd/operator
  - internal/cli/cmd/gw
related:
  - docs/guides/build_operator.md
  - docs/guides/unified_stack.md
  - docs/architecture/operator.md
  - docs/architecture/auth.md
  - docs/architecture/network.md
when_to_read: Enrolling an Operator, connecting it over outbound mTLS to a Gateway, and operating single or fleet Operator deployments.
do_not_use_for:
  - Building the Operator binary (docs/guides/build_operator.md)
  - Full Operator internal architecture (docs/architecture/operator.md)
  - Single-host evaluation stack topology (docs/guides/unified_stack.md)
---

# Connect Operator to Gateway

## Overview

The ztd Operator is the Policy Execution Point (PEP) for the runtime environment visible to its process. It opens an outbound mTLS WebSocket connection to the Gateway, subscribes to its identity- and session-scoped command channel, verifies governed transactions, executes approved actions in its local runtime, and publishes signed receipts and heartbeats. The Operator exposes no inbound application listener.

Connecting a new Operator follows five steps:

1. Start the Gateway.
2. Enroll the first Gateway owner if not already bootstrapped.
3. Start the Operator with the Gateway endpoint. The Operator submits a platform enrollment request and waits for approval.
4. Approve the request from an enrolled owner CLI.
5. The Operator receives its issued credentials and connects to the pub/sub channel automatically.

The reference Gateway and Operator are subcommands in the same static `ztd` binary. See [Build Operator](build_operator.md) for build details and [Operator Architecture](../architecture/operator.md) for service design.

---

## Prerequisites

**Gateway host requirements:**

- A running `ztd` Gateway, started with `./ztd gw start`.
- TCP ports 8080 and 8443 (the defaults) reachable from the Operator host; see [Endpoint Requirements](#endpoint-requirements) for the bind host and shifted ports. Port 8080 serves the Operator bootstrap websocket (`/ws/operator/bootstrap`) for enrollment. Port 8443 serves authenticated APIs and the mTLS WebSocket pub/sub connection for active Operators.
- A certificate identity that matches the hostname the Operator uses to reach it. The default certificate mode detects hostnames and IP addresses at Gateway startup.
- An enrolled owner CLI identity to approve Operator enrollment requests.

**Operator host requirements:**

- The `ztd` binary and a writable working directory. The Operator creates its `.ztd/` runtime tree (`vault/`, `secrets/`, `data/ztd.db`, and `ledger/`) under `--working-dir`, or under the launch directory when the flag is omitted. Enrollment keys, certificates, and session credentials are held only in process memory.
- Outbound access to the Gateway on ports 8080 (bootstrap websocket) and 8443 (mTLS pub/sub).
- A working directory of its own. `--working-dir` selects both where commands run and where the runtime tree lives. Operator start refuses a directory that already holds a Gateway runtime.

No inbound port is required on the Operator host.

### Endpoint Requirements

Use a hostname with `ztd operator start --endpoint`, without an `http://` or `https://` scheme. The Operator uses Gateway port 8080 for enrollment and port 8443 for mTLS WebSocket pub/sub channels carrying commands, heartbeats, results, and receipts. A `host:port` endpoint sets the enrollment port; set the pub/sub port with `--gateway-https-port`. The Operator uses WebSocket connections for bootstrap and active pub/sub; it does not call the Gateway's ordinary HTTP APIs. Command verification uses the admission snapshot already bound into the envelope; receipt state roots come from the Operator's local store.

The Gateway HTTP and HTTPS listeners bind `127.0.0.1` by default. Start the Gateway with `make up-public` or `make gw-public` (or `./ztd gw start --listen-host 0.0.0.0`) before connecting Operators from other hosts. If a default port is busy, background `./ztd gw start` moves both listeners to the next free pair and prints the bound ports; give the Operator `--endpoint <gateway-host>:<http-port> --port <https-port>` with those ports.

---

## Connect a New Operator

### 1. Start the Gateway

On the Gateway host, start the service:

```bash
./ztd gw start
```

Use `--follow` (`-f`) to run in the foreground, or `./ztd gw logs -f` to follow background logs. The default posture is `doctrine`.

Verify the Gateway process is running:

```bash
./ztd gw status
```

### 2. Enroll the Gateway Owner

On the Gateway host or on an owner workstation with network access, set the shell variable `GATEWAY_HOST` to the Gateway hostname (for example, `export GATEWAY_HOST=gw.example.internal`), then run:

```bash
./ztd auth enroll user --endpoint "$GATEWAY_HOST"
```

Enrollment creates the first user and CLI session on an unbootstrapped Gateway, installs the Gateway root CA into the operating system trust store, and runs the browser passkey ceremony. Use `--no-system-trust` if an administrator has installed the root CA separately. Use `--headless` for an mTLS-only CLI identity.

### 3. Start the Operator

On the Operator host, set `GATEWAY_HOST` to the Gateway hostname as in step 2, then start the Operator in the foreground:

```bash
./ztd operator start --endpoint "$GATEWAY_HOST"
```

For a same-host deployment, give the Operator its own working directory so it does not share the Gateway's `.ztd/`:

```bash
./ztd operator start --endpoint localhost --working-dir ~/.ztd-operator
```

The first start of a new Operator runtime prints a recovery key once and waits for you to type it back. That key is the only offline copy of this operator's master key. Lose it and the OS key store, and this operator cannot be restored. `operator deploy` from a terminal on this machine shows the key before it starts a local worker. A remote deploy has no terminal on the far host, so pass `--accept-unrecoverable` or run `operator start` in a terminal on that host first. Install a saved key with `./ztd operator recover`. See [Recovery key](../architecture/encryption.md#recovery-key).

The Operator executes:
1. Generates Operator and CLI ECDSA P-256 keys, certificate signing requests (CSRs), and a request token in memory.
2. Opens a websocket to `ws://<gateway-host>:8080/ws/operator/bootstrap` and sends the enrollment request.
3. Prints the request ID and approval command (`Approve on the Gateway host with:` followed by `./ztd auth enroll approve <request-id>` on its own line), signs the completion transcript with both private keys as proof of possession, enters `pending_approval`, and holds the socket open.

### 4. Inspect and Approve the Request

From the enrolled owner CLI, with `GATEWAY_HOST` still set from step 2:

```bash
./ztd auth enroll pending --endpoint "$GATEWAY_HOST"
```

Compare the hostname and the Operator and CLI key fingerprints with the values the Operator logs on submission (`operator_fingerprint` and `cli_fingerprint`). Then set the shell variable `REQUEST_ID` to the request ID the Operator printed in step 3, and approve:

```bash
./ztd auth enroll approve "$REQUEST_ID" --endpoint "$GATEWAY_HOST" --yes
```

### 5. Wait for the Connection

After approval, the Gateway sends the decision over the held socket and issues the certificates against the completion proof the Operator already sent. The Operator receives its credentials, trust bundle, session ID, runtime limits, and L2 signer trust into memory. The Operator establishes the mTLS pub/sub connection and subscribes to its command channel:

```text
cmd:<operator_id>:<operator_session_id>
```

A successful connection logs `Channel established - Ready to receive`. The Operator sends an immediate heartbeat and continues at its configured interval (default: 30 seconds).

---

## Pin the Gateway CA

To pin the Gateway CA explicitly, with `GATEWAY_HOST` set as in step 2, copy the Gateway's `.ztd/pki/trust/ztdg-ca-bundle.pem` to this host and set `TRUST_BUNDLE` to the copied file's path:

```bash
./ztd operator start \
  --endpoint "$GATEWAY_HOST" \
  --trust-bundle "$TRUST_BUNDLE"
```

With `--trust-bundle`, the Operator enrolls over `wss://` on the Gateway HTTPS port (8443 by default), verifying the Gateway certificate against this CA before sending its request.

---

## Consensus Trust Changes

The Gateway delivers L2 signer trust to an Operator in its bootstrap bundle and again with each certificate renewal. A running Operator keeps the trust it last received. After `./ztd consensus enroll app` or `./ztd consensus enroll tribunal` binds a new consensus, restart every running Operator so it receives the current trust. Stop the Operator with `./ztd operator stop "$OPERATOR_SESSION_ID"` and start it again. A restart runs enrollment again and needs owner approval, as described under [Stop the Operator](#stop-the-operator).

---

## Deploy the Binary to a Remote Host

Copy the current binary to a local path:

```bash
./ztd operator cp /opt/ztd/
```

Copy the binary via SCP:

```bash
./ztd operator scp user@192.0.2.10:/opt/ztd
```

Deploy and start Operators over SSH, with `GATEWAY_HOST` set as in step 2:

```bash
./ztd operator deploy --hosts user@192.0.2.10 --dest-dir /opt/ztd-operator --background --endpoint "$GATEWAY_HOST"
```

### Connect Many Operators

Deploy several Operators in one run with `--count`; `--count` and `--start-index` must select Operators within 1 to 5000. With `--local` alone, the Operators land in `.ztd/operators/op-00001` onward under the project runtime tree:

```bash
./ztd operator deploy --local --endpoint localhost --count 10 \
  --capabilities data --trust-bundle "$PWD/.ztd/pki/trust/ztdg-ca-bundle.pem" --background --approve
```

Add more Operators without replacing existing instances:

```bash
./ztd operator deploy --local --endpoint localhost --start-index 11 --count 100 \
  --capabilities data --trust-bundle "$PWD/.ztd/pki/trust/ztdg-ca-bundle.pem" --background --approve
```

---

## Verify and Operate the Connection

### Check Gateway Health

```bash
./ztd gw status
```

### List Connected Operators

With `GATEWAY_HOST` set as in step 2:

```bash
./ztd operator list --endpoint "$GATEWAY_HOST"
```

Displays operator ID, type, capabilities, hostname, session ID, and status.

### Bind the CLI to Operators and Run Commands

Set the shell variable `OPERATOR_SESSION_ID` to the session ID of a target Operator from `./ztd operator list`, then bind the CLI session to it. Pass additional session IDs after the first to bind several Operators at once:

```bash
./ztd operator bind "$OPERATOR_SESSION_ID"
./ztd operator bind list
./ztd operator bind unbind
```

Execute a governed shell command on active Operators in parallel. The single-Operator form uses the `OPERATOR_SESSION_ID` set above:

```bash
./ztd operator run "$OPERATOR_SESSION_ID" --cmd "uname -a"
./ztd operator run --all-active --concurrency 64 --cmd "uname -a"
```

### Stop the Operator

Stop the Operator whose session ID is in `OPERATOR_SESSION_ID`:

```bash
./ztd operator stop "$OPERATOR_SESSION_ID" --reason "maintenance"
```

To stop all local Operators running under the current user (Linux, macOS, and Windows):

```bash
./ztd operator stop
```

This discovers `ztd operator start` processes owned by the current user and targets only those Operators; it does not stop the Gateway or unrelated `ztd` commands. It first requests governed shutdown where an active Gateway connection is available, then waits for exit and escalates to the platform process termination mechanism if needed. On Windows, both escalation signals terminate the process because Windows has no catchable `TERM` signal. Processes owned by another user, or that cannot be inspected due to permissions or bitness, are skipped. Local stop does not revoke enrollment; because Operator credentials are memory-only, the next start performs enrollment again and needs owner approval.

Enrolling again with the same system fingerprint or Actuator key supersedes the prior lease. The fingerprint covers the OS, architecture, CPU count, machine ID, hostname, working directory, account, and port. Capabilities are not part of it, so restarting an Operator with a capability added (for example `--capabilities inference,provenance,observer`) replaces its prior lease and takes over that lease's capabilities. The Gateway terminates the earlier Operator record and deactivates its session, so a role that must resolve to one Operator stays unambiguous.

A lease can only be superseded by an enrollment the Gateway can tie to it. If an Operator's Actuator key changed (for example its working directory was reset) and it was first enrolled before capabilities left the fingerprint, enrollment fails with `operator capability is already assigned: <capability> belongs to Operator <id>`. Retire that record once, then restart the Operator:

```bash
./ztd operator terminate <operator-id> --reason "actuator key rotated"
```

`terminate` takes the operator ID from `ztd gw status` or `ztd operator list`, not a session ID. It marks one of your remote Operator records terminated, so it can no longer authenticate and its capabilities are released. It does not stop a process, and the embedded Operator cannot be terminated.

---

## Troubleshooting

### Enrollment Waits for Gateway Owner

A new Gateway holds the bootstrap socket until an owner exists. Run `./ztd auth enroll user --endpoint <gateway-host>` to bootstrap the owner; the waiting Operator continues automatically.

### Bootstrap Websocket Does Not Connect

Confirm port 8080 is reachable. `connection refused` from a remote host means the Gateway is not running or listens on loopback only; restart it with `make up-public` or `make gw-public` (`--listen-host 0.0.0.0`).

### Pub/Sub Connection Fails After Enrollment

Verify port 8443 is reachable, the endpoint contains no URL scheme, and the hostname matches the Gateway certificate SAN.

### Operator Cannot Start from Its Model Store

Keep `--working-dir` outside `--model-storage-root`. Startup refuses a runtime `.ztd/` tree that is equal to or nested under the model storage root, so the provenance model store remains isolated from runtime state.

---

## Security Properties

- **Owner-approved enrollment**: Platform certificates require explicit owner approval via authenticated CLI or Console.
- **Proof of key possession**: The Operator signs the enrollment completion transcript with both private keys before certificate issuance.
- **Memory-only identity**: Operator enrollment private keys, certificates, and session credentials live only in process memory. The Actuator signing key is persisted encrypted under `.ztd/secrets/`.
- **Bounded identity lifetime**: Operator leaf certificates are valid for at most 7 days (`constants.MaxWorkloadLeafCertTTL`). The Operator renews its leaf two thirds into its lifetime. Renewal failures are retried until the leaf expires. A Gateway refusal is final, and the Operator enrolls again with owner approval once the leaf expires.
- **Outbound-only Operator**: Operators initiate all connections and expose zero inbound ports.
- **Fail-closed verification**: L4 Warden re-verifies transactions, hashes, nonces, state roots, and posture proofs locally before L5 execution.

---

## Next Steps

- [Build Operator](build_operator.md) — Build binaries, review `.ztd/` storage, and configure capabilities.
- [Operator Architecture](../architecture/operator.md) — Operator lifecycle, execution boundary, and Local-First Events Store.
- [Build Gateway](build_gateway.md) — Gateway configuration, PKI, and management.
- [Connect Applications to Gateway](connect_apps_to_gateway.md) — Connect client SDKs and agent tools.
