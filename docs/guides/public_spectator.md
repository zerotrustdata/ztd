---
doc_id: public_spectator
title: Public Spectator Operations Guide
audience: platform operators, release managers, publication reviewers
status: current
version: v1.0.0
last_updated: 2026-10-12
owners:
  - docs/guides/public_spectator.md
  - internal/cli/cmd/public/
  - internal/services/public/
related:
  - docs/architecture/public_spectator.md
  - docs/guides/unified_stack.md
  - docs/guides/cloudflare_tunnel.md
  - docs/guides/build_observe_frontend.md
when_to_read: Operating, configuring, verifying, or publishing the gateway-owned public spectator feed, public mirror, and evaluation explorer.
do_not_use_for:
  - Owner-authenticated observe frontend operations (docs/guides/build_observe_frontend.md)
  - Public spectator threat modeling and protocol specification (docs/architecture/public_spectator.md)
  - Private Gateway lifecycle and clustering (docs/guides/build_gateway.md)
---

# Public Spectator Operations Guide

This guide covers the Gateway-owned anonymous public mirror and evaluation explorer. It is separate from the passkey-authenticated owner-local observe frontend connected with `./ztd gw connect <origin>`; see [Generator-Neutral Builder Guide](./build_observe_frontend.md) and [Build a ztd-Compatible Frontend](./build_frontend.md#generator-neutral-observe-frontend).

## Runtime boundaries

The Gateway-owned public spectator runs inside the Gateway process when started with `--public-spectator` (default). One in-process `PublicSpectatorRuntime` owns:

- **Private ingest** (`--public-spectator-private-listen`, default `127.0.0.1:8081`): authenticated publisher ingest, proof ingest, replacement-key registration.
- **Public read/SSE** (`--public-spectator-public-listen`, default `127.0.0.1:8082`): anonymous bootstrap, snapshot, history, SSE, proof catalog/manifest, content-addressed proof downloads.
- **Evaluation explorer** (`--eval-explorer-listen`, default `127.0.0.1:5173`): embedded acceptance UI.

Durable feed, signing keys, and outbox state live in the Gateway runtime tree under `.ztd/public-feed/`. Listeners bind loopback by default. Only CIDRs passed to `--public-spectator-trusted-proxy-cidr` are trusted to supply `CF-Connecting-IP`.

Restarting the Gateway preserves mirror state and publisher outbox state.

Campaign publication from the host CLI uses owner mTLS and `POST /api/v1/public-feed/batches`.

For an existing run, use verified catch-up after the verification report is persisted:

```bash
./ztd eval runs publish <run-id>
```

The publication coordinator probes the Gateway-owned dataset during catch-up. If the dataset is absent while host publication idempotency still lists records, it clears stale state and republishes the canonical run. Use `--force` only when the mirror was wiped or is known to be missing records.

The mirror is a visibility boundary, not a Policy Decision Point or Policy Execution Point. The executing Operator retains authoritative local execution evidence in its local audit store.

## Start a fresh source chain

Use an explicit source transition when archiving current public datasets to start an independent publication generation:

```bash
./ztd gw stop
./ztd public source transition --source-id <new-public-source-pseudonym> --yes
./ztd gw start
```

The transition preserves the mirror store and archives current publisher configuration, key, token, outbox, snapshot, and local proofs under `.ztd/public-feed-archive/generations/<old-source-id>/`. The command creates a fresh source identity with a new signing key and ingest token. The first new batch starts at sequence 1 with the zero predecessor hash.

## Verify listener separation

The private listener accepts authenticated publisher operations. Requests without the ingest token fail authentication. The public listener does not register mutation routes, returning HTTP 404 for mutation attempts:

```bash
curl -o /dev/null -sS -w '%{http_code}\n' -X POST http://127.0.0.1:8082/ingest
curl -o /dev/null -sS -w '%{http_code}\n' -X POST http://127.0.0.1:8082/keys/register
curl -o /dev/null -sS -w '%{http_code}\n' -X POST http://127.0.0.1:8082/proof-ingest
```

Verify the anonymous allowlist on the public listener:

```bash
curl -fsS http://127.0.0.1:8082/bootstrap
curl -fsS 'http://127.0.0.1:8082/history?source=<public-source-pseudonym>&cursor=0&limit=20&kind=catalog_snapshot'
curl -fsS 'http://127.0.0.1:8082/snapshot?source=<public-source-pseudonym>'
curl -fsS http://127.0.0.1:8082/proof-catalog
curl -fsS http://127.0.0.1:8082/proof-manifest
```

Public clients omit credentials.

## Create the Cloudflare tunnel

The tunnel origin is the read-only public listener (`8082`), never the private listener (`8081`) and never the Gateway console (`8443`):

```bash
./ztd gw tunnel create --name opendevops-feed --hostname opendevops.ai --service http://127.0.0.1:8082
./ztd gw tunnel run
```

Start the Gateway with `--public-base-url https://opendevops.ai` so the embedded explorer points at the public origin.

## External acceptance

After starting the tunnel, verify HTTPS, CORS, bounded reads, rate limiting, replayable SSE, proof catalog, proof manifest, and content-addressed proof download from an external client.

Confirm `/ingest`, `/keys/register`, `/proof-ingest`, Gateway management, MCP, A2A, approval, and audit paths return 404 on the public origin.

Scan public responses and proof bytes for credentials, private endpoints, machine paths, SPIFFE identities, raw prompts, or model outputs before publication.

## Manual record publish (advanced)

`ztd public publish`, `ztd public push`, `ztd public status`, `ztd public repair-outbox`, and `ztd public restore` manage host publisher state and Gateway mirror reconciliation:

```bash
./ztd public publish <public-records.jsonl>
./ztd public push
./ztd public status
./ztd public restore --queue
```

`public publish` durably appends records before transmission. `public push` retries delivery without re-executing inference. `public restore` republishes completed runs from the queue to the mirror after a storage reset.

## Links out

- [Public Spectator Architecture and Threat Model](../architecture/public_spectator.md) — Mirror threat model and outbox mechanics.
- [Unified Stack Guide](unified_stack.md) — Host-native evaluation stack.
- [Generator-Neutral Builder Guide](build_observe_frontend.md) — Observe frontend builder specification.
- [Cloudflare Tunnel Integration](cloudflare_tunnel.md) — Cloudflare tunnel configuration.
