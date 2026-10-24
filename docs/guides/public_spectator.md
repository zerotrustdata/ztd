---
doc_id: public_spectator
title: Public Spectator Operations Guide
audience: platform operators, release managers, publication reviewers
status: current
version: v1.3.0
last_updated: 2026-10-23
owners:
  - docs/guides/public_spectator.md
  - internal/cli/cmd/public/
  - internal/services/gateway/public_spectator_runtime.go
  - internal/services/gateway/public_mirror.go
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

This guide covers the Gateway-owned anonymous public mirror and evaluation explorer. It is separate from the passkey-authenticated owner-local observe frontend connected with `./ztd gw connect <origin>`; see [Generator-Neutral Builder Guide](./build_observe_frontend.md) and [Build a ztd-Compatible Frontend](./build_frontend.md).

## Runtime boundaries

The Gateway-owned public spectator runs inside the Gateway process when started with `--public-spectator` (default). One in-process `PublicSpectatorRuntime` owns:

- **Private ingest** (`--public-spectator-private-listen`, default `127.0.0.1:8081`): authenticated publisher ingest, proof ingest, replacement-key registration.
- **Public read/SSE** (`--public-spectator-public-listen`, default `127.0.0.1:8082`): anonymous bootstrap, snapshot, history, SSE, proof catalog/manifest, content-addressed proof downloads.
- **Evaluation explorer** (`--eval-explorer-listen`, default `127.0.0.1:5173`): dedicated explorer listener that reads the local public listener API. The public listener also serves the explorer assets (`GET` and `HEAD`), so the public origin hosts the explorer.

Publisher configuration, signing key, ingest token, and outbox state live under `.ztd/public-feed/`. The publisher's signed proof log and artifacts live under `.ztd/public-proofs/`, with its delivery checkpoint in `.ztd/proof-mirror-sync.json`. Together these form one feed generation. If generation state exists without publisher configuration, startup fails closed with `ErrPublicFeedOrphanedGeneration` instead of adopting that state. The mirror database and content-addressed proof artifacts live under `.ztd/public-mirror/`. Listeners bind loopback by default. Only CIDRs passed to `--public-spectator-trusted-proxy-cidr` are trusted to supply `CF-Connecting-IP`.

Restarting the Gateway preserves mirror state and publisher outbox state.

Campaign publication from the host CLI uses mTLS with the host CLI certificate and `POST /api/v1/public-feed/batches`.

For an existing run, use verified catch-up after the verification report is persisted. Set the shell variable `RUN_ID` to the ID of the run to publish, then run:

```bash
./ztd eval runs publish "$RUN_ID"
```

During catch-up, the publication coordinator checks the Gateway-owned dataset with one server-filtered mirror history request; a withdrawn dataset reads as absent. If the dataset is absent while host publication idempotency still lists records, it clears stale state and republishes the canonical run. Use `--force` after a Gateway mirror volume wipe. It clears host publication idempotency and republishes all projections.

To republish every run that is not archived, run:

```bash
./ztd eval runs publish --all
```

`--all` clears each run's host publication idempotency first, as `--force` does, so runs already published are published again. A run a live process holds is skipped, because that process publishes it. A run that fails to publish does not stop the others; the command lists each failure and exits with an error.

The mirror is a visibility boundary, not a Policy Decision Point or Policy Execution Point. The executing Operator retains authoritative local execution evidence in its Local-First Events Store.

## Start a fresh source chain

Use an explicit source transition when archiving current public datasets to start an independent publication generation. Set the shell variable `NEW_SOURCE_ID` to the new public source pseudonym before running:

```bash
./ztd gw stop
./ztd public source transition --source-id "$NEW_SOURCE_ID" --yes
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

Verify the anonymous allowlist on the public listener. Set the shell variable `SOURCE_ID` to the public source pseudonym of the current publication generation before running the source-scoped requests:

```bash
curl -fsS http://127.0.0.1:8082/bootstrap
curl -fsS "http://127.0.0.1:8082/history?source=${SOURCE_ID}&cursor=0&limit=20&kind=catalog_snapshot"
curl -fsS "http://127.0.0.1:8082/snapshot?source=${SOURCE_ID}"
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

Confirm that mutation requests (`POST`) to `/ingest`, `/keys/register`, `/proof-ingest`, and any other non-read path return 404 on the public origin. The public origin serves only the anonymous read allowlist and explorer assets. A `GET` to a path outside that allowlist returns the explorer `index.html` (HTTP 200), not an API response, so Gateway management, MCP, A2A, approval, and audit paths are not reachable there.

Scan public responses and proof bytes for credentials, private endpoints, machine paths, SPIFFE identities, raw prompts, or model outputs before publication.

## Manual record publish (advanced)

`ztd public publish`, `ztd public push`, `ztd public status`, `ztd public repair-outbox`, and `ztd public restore` manage host publisher state and Gateway mirror reconciliation. Set the shell variable `RECORDS_FILE` to the JSONL file of records to publish:

```bash
./ztd public publish "$RECORDS_FILE"
./ztd public push
./ztd public status
./ztd public restore --queue
```

`public publish` durably appends records before transmission. `public push` retries outbox delivery and pushes the proof package. When publication is Gateway-owned (no host publisher configuration and a healthy Gateway), `public push` prints that the Gateway-owned public mirror is already synchronized and does nothing. `public restore --queue` republishes completed, passing verified runs from the campaign queue to the mirror.

## Links out

- [Public Spectator Architecture and Threat Model](../architecture/public_spectator.md) — Mirror threat model and outbox mechanics.
- [Unified Stack Guide](unified_stack.md) — Host-native evaluation stack.
- [Generator-Neutral Builder Guide](build_observe_frontend.md) — Observe frontend builder specification.
- [Cloudflare Tunnel Integration](cloudflare_tunnel.md) — Cloudflare tunnel configuration.
