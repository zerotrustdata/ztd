---
doc_id: connect_operator_to_gateway
title: Connect Operator to Gateway
audience: platform operators and deployment engineers
status: current
version: v1.0.0
last_updated: 2026-10-12
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

Connecting a new Operator follows four steps:

1. Start the Gateway.
2. Enroll the first Gateway owner if not already bootstrapped.
3. Start the Operator with the Gateway endpoint. The Operator submits a platform enrollment request and waits for approval.
4. Approve the request from an enrolled owner CLI. The Operator receives its issued credentials and connects to the pub/sub channel automatically.

The reference Gateway and Operator are subcommands in the same static `ztd` binary. See [Build Operator](build_operator.md) for build details and [Operator Architecture](../architecture/operator.md) for service design.

---

## Prerequisites

**Gateway host requirements:**

- A running `ztd` Gateway, started with `./ztd gw start`.
- TCP ports 8080 and 8443 reachable from the Operator host. Port 8080 serves the Operator bootstrap websocket (`/ws/operator/bootstrap`) for enrollment. Port 8443 serves authenticated APIs and the mTLS WebSocket pub/sub connection for active Operators.
- A certificate identity that matches the hostname the Operator uses to reach it. The default certificate mode detects hostnames and IP addresses at Gateway startup.
- An enrolled owner CLI identity to approve Operator enrollment requests.

**Operator host requirements:**

- The `ztd` binary and a writable launch directory. The process creates its `.ztd/` runtime tree below that directory for vault keys and local state. Operator credentials are never written there; they exist only in process memory.
- Outbound access to the Gateway on ports 8080 (bootstrap websocket) and 8443 (mTLS pub/sub).
- A working directory for command execution. Use `--working-dir` to select this directory.

No inbound port is required on the Operator host.

### Endpoint Requirements

Use a bare hostname with `ztd operator start --endpoint`, without an `http://` or `https://` scheme and without a port suffix. The worker uses Gateway port 8080 for enrollment and port 8443 for mTLS WebSocket pub/sub channels carrying commands, heartbeats, results, and receipts. The Operator makes no HTTP requests to the Gateway. Command verification uses the admission snapshot already bound into the envelope; receipt state roots come from the Operator's local store.

The Gateway HTTP and HTTPS listeners bind `127.0.0.1` by default. Start the Gateway with `make up-public` or `make gw-public` (or `./ztd gw start --listen-host 0.0.0.0`) before connecting Operators from other hosts.

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

On the Gateway host or on an owner workstation with network access:

```bash
./ztd auth enroll user --endpoint <gateway-host>
```

Enrollment creates the first user and CLI session on an unbootstrapped Gateway, installs the Gateway root CA into the operating system trust store, and runs the browser passkey ceremony. Use `--no-system-trust` if an administrator has installed the root CA separately. Use `--headless` for an mTLS-only CLI identity.

### 3. Start the Operator

On the Operator host, start the worker in the foreground:

```bash
./ztd operator start --endpoint <gateway-host>
```

For a same-host deployment:

```bash
./ztd operator start --endpoint localhost
```

The Operator executes:
1. Generates Operator and CLI ECDSA P-256 keys, certificate signing requests (CSRs), and a request token in memory.
2. Opens a websocket to `ws://<gateway-host>:8080/ws/operator/bootstrap` and sends the enrollment request.
3. Prints the request ID and approval command, enters `pending_approval`, and holds the socket open.

### 4. Inspect and Approve the Request

From the enrolled owner CLI:

```bash
./ztd auth enroll pending --endpoint <gateway-host>
```

Compare the component name, hostname, system fingerprint, and key fingerprints with the Operator's console output, then approve:

```bash
./ztd auth enroll approve <request-id> --endpoint <gateway-host> --yes
```

### 5. Wait for the Connection

After approval, the Gateway sends the decision over the held socket. The Operator signs the completion transcript with both private keys and receives its credentials, trust bundle, session ID, and runtime limits into memory. The Operator establishes the mTLS pub/sub connection and subscribes to its command channel:

```text
cmd:<operator-id>:<operator-session-id>
```

A successful connection logs `Channel established - Ready to receive`. The Operator sends an immediate heartbeat and continues at its configured interval (default: 30 seconds).

---

## Pin the Gateway CA

To pin the Gateway CA explicitly:

```bash
./ztd operator start \
  --endpoint <gateway-host> \
  --trust-bundle /path/to/ztdg-ca-bundle.pem
```

With `--trust-bundle`, the Operator enrolls over `wss://` on Gateway HTTPS port 8443, verifying the Gateway certificate against this CA before sending its request.

---

## Deploy the Binary to a Remote Host

Copy the current binary to a local path:

```bash
./ztd operator cp /tmp/ztd
```

Copy the binary via SCP:

```bash
./ztd operator scp user@192.0.2.10:/opt/ztd
```

Deploy and start workers over SSH:

```bash
./ztd operator deploy --hosts user@192.0.2.10 --dest-dir /opt/ztd-operator --background --endpoint <gateway-host>
```

### Connect Many Operators

Deploy up to 5000 Operators per host with `--count`:

```bash
./ztd operator deploy --local --endpoint localhost \
  --dest-dir .local/tmp/operator-fleet --count 10 \
  --capabilities data --trust-bundle "$PWD/.ztd/pki/trust/ztdg-ca-bundle.pem" --background --approve
```

Add more workers without replacing existing instances:

```bash
./ztd operator deploy --local --endpoint localhost \
  --dest-dir .local/tmp/operator-fleet --start-index 11 --count 100 \
  --capabilities data --trust-bundle "$PWD/.ztd/pki/trust/ztdg-ca-bundle.pem" --background --approve
```

---

## Verify and Operate the Connection

### Check Gateway Health

```bash
./ztd gw status
```

### List Connected Operators

```bash
./ztd operator list --endpoint <gateway-host>
```

Displays operator ID, type, hostname, session ID, and status (`active` or `stale`).

### Bind the CLI to Operators and Run Commands

Bind CLI session to target Operators:

```bash
./ztd operator bind <operator-session-id> [<operator-session-id>...]
./ztd operator bind list
./ztd operator bind unbind
```

Execute a governed shell command on active Operators in parallel:

```bash
./ztd operator run <operator-session-id> --cmd "uname -a"
./ztd operator run --all-active --concurrency 64 --cmd "uname -a"
```

### Stop the Operator

```bash
./ztd operator stop <operator-session-id> --reason "maintenance"
```

To stop all local workers running under the current user:

```bash
./ztd operator stop
```

---

## Troubleshooting

### Enrollment Waits for Gateway Owner

A new Gateway holds the bootstrap socket until an owner exists. Run `./ztd auth enroll user --endpoint <gateway-host>` to bootstrap the owner; the waiting Operator continues automatically.

### Bootstrap Websocket Does Not Connect

Confirm port 8080 is reachable. `connection refused` from a remote host means the Gateway is not running or listens on loopback only; restart it with `make up-public` or `make gw-public` (`--listen-host 0.0.0.0`).

### Pub/Sub Connection Fails After Enrollment

Verify port 8443 is reachable, the endpoint contains no scheme or port suffix, and the hostname matches the Gateway certificate SAN.

---

## Security Properties

- **Owner-approved enrollment**: Platform certificates require explicit owner approval via authenticated CLI or Console.
- **Proof of key possession**: The Operator signs the enrollment completion transcript with both private keys before certificate issuance.
- **Memory-only identity**: Operator private keys, certificates, and session credentials live only in process memory and never touch disk.
- **Outbound-only worker**: Operators initiate all connections and expose zero inbound ports.
- **Fail-closed verification**: L4 Warden re-verifies transactions, hashes, nonces, state roots, and posture proofs locally before L5 execution.

---

## Next Steps

- [Build Operator](build_operator.md) — Build binaries, review `.ztd/` storage, and configure capabilities.
- [Operator Architecture](../architecture/operator.md) — Operator lifecycle, execution boundary, and audit stores.
- [Build Gateway](build_gateway.md) — Gateway configuration, PKI, and management.
- [Connect Applications to Gateway](connect_apps_to_gateway.md) — Connect client SDKs and agent tools.
