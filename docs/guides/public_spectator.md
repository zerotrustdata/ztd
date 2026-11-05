---
doc_id: public_spectator
title: Public Spectator Operations Guide
audience: platform operators, release managers, publication reviewers
status: current
version: v1.5.0
last_updated: 2026-10-28
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
- **Public read** (`--public-spectator-public-listen`, default `127.0.0.1:8082`): anonymous bootstrap, snapshot, history, proof catalog, proof head, signed proof segments, content-addressed proof downloads, and the public layout (`pointer.json`, read models, history pages, live objects). The mirror mounts no SSE route.
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

Read the layout on the public listener. The objects are the same ones the bucket serves. Set the shell variable `PUBLIC_ORIGIN` to `http://127.0.0.1:8082` for a local mirror, or to the bucket's public origin:

```bash
curl -fsS "${PUBLIC_ORIGIN}/pointer.json" | jq '{schema_version, revision, read_model_sha256, high_water_sequence, active}'
curl -fsS "${PUBLIC_ORIGIN}/live/status.json" | jq '{active_runs, tail: (.tail | length)}'
```

Read the immutable model named by one captured pointer. Its digest covers decoded JSON; run-bundle digests cover the stored gzip bytes. Set `RUN_ID` to the settled run to verify and obtain `trusted-keys.json` through a trusted channel before verification:

```bash
curl -fsS "${PUBLIC_ORIGIN}/pointer.json" > pointer.json
MODEL=$(jq -r .read_model_sha256 pointer.json)
curl -fsS --compressed "${PUBLIC_ORIGIN}/read-model/${MODEL}.json.gz" > read-model.json
BUNDLE=$(jq -r --arg run "$RUN_ID" '.recent_projections[] | select(.kind == "evaluation_summary" and .run_id == $run) | .assignment_bundle_sha256' read-model.json)
curl -fsS "${PUBLIC_ORIGIN}/runs/${BUNDLE}.jsonl.gz" > run.jsonl.gz
./ztd public verify-proof --pointer pointer.json --read-model read-model.json --bundle run.jsonl.gz --run "$RUN_ID" --key-registry trusted-keys.json
```

Only run bundles and frozen scenario fixtures are public artifact bodies. Private traces and audit databases are delivered under agreement with SHA-256 bindings in the authenticated bundle. Add `--artifacts ./delivered` for digest-named delivered files. To verify a delivered audit database's event hashes and identity, use `verify-assignment --db assignment.db` with the same pointer, read-model, bundle, run, and key inputs. See the [canonical offline verification procedure](../architecture/public_spectator.md#offline-proof-verification) for trust and freshness limits.

The listener retains `/bootstrap`, `/snapshot`, and `/history` for CLI and cloud consumers. Anonymous `/proofs/`, `/proof-catalog`, `/proof-head`, `/proof-segments`, and their static projections return 404. The authenticated private proof transport remains unchanged. Public clients omit credentials.

## Publish the layout to a bucket

The bucket publisher copies the mirror's layout to an S3-compatible bucket served from a custom domain. The layout and each object's cache header are described in [Bucket Layout](../architecture/public_spectator.md#bucket-layout). The production bucket serves `feed.opendevops.ai`.

Set up the bucket in the Cloudflare account that owns the zone:

1. Create an R2 bucket. Add `feed.opendevops.ai` as its custom domain. The domain must be in the same account as the zone.
2. Add a Cache Rule for `feed.opendevops.ai`. The rule makes the host eligible for cache and respects origin `Cache-Control`. Without it, JSON and extensionless paths miss the cache, and each poll is a Class B read.
3. Set the bucket CORS policy to allow `GET` and `HEAD` from `https://opendevops.ai` and from the staging host `https://next.opendevops.ai`.
4. Create an API token limited to object read and write on this bucket. The publisher uses it for uploads, deletes, and listings.
5. Create a second API token limited to cache purge for this zone. The publisher uses it for purges.

The bucket holds one feed source. The first sync deletes any proof artifact in the bucket that the mirror's catalog does not list.

Write the credentials to a JSON file with `access_key_id`, `secret_access_key` (from step 4), and `purge_token` (from step 5). Then set the bucket on the publisher configuration:

```bash
./ztd public config set \
  --bucket-name opendevops-feed \
  --bucket-endpoint https://<ACCOUNT_ID>.r2.cloudflarestorage.com \
  --bucket-public-origin https://feed.opendevops.ai \
  --bucket-zone-id <ZONE_ID> \
  --bucket-credentials-file ~/.config/ztd/feed-bucket.json
```

`--bucket-credentials-file` accepts a path or `-` for stdin. The credentials are sealed beside the ingest token and do not enter the export configuration. Run `./ztd public config set --help` for the full flag set.

`--bucket-remove` turns publication to the bucket off. Sealed credentials remain on disk. A configuration with no bucket fields does not publish to a bucket. A mirror hosted at a separate address is refused with an error when a bucket is configured, because the publisher must read the mirror it is colocated with.

The Gateway reads the configuration at start, so restart it to apply a change:

```bash
./ztd gw stop
./ztd gw start --public-spectator
```

Missing or unreadable sealed credentials are logged as `Public bucket publishing disabled`, with the cause. The mirror keeps serving. Publication resumes when the credentials are set again with `--bucket-credentials-file` and the Gateway restarts.

On the first sync, the publisher uploads every proof artifact and its metadata, then the rest of the layout. An interrupted upload resumes from the bucket's listing. Check the state with:

```bash
./ztd public status
```

The status output has a `Bucket:` line. It shows the pointer revision, the sequence the pointer names, how many sequences the bucket is behind the mirror, and the pending deletes and purges.

## Create the Cloudflare tunnel

The tunnel origin is the read-only public listener (`8082`), never the private listener (`8081`) and never the Gateway console (`8443`). The tunnel serves the listener routes and the Explorer that the Gateway embeds. The bucket domain serves the layout. Both read the same objects.

```bash
./ztd gw tunnel create --name opendevops-feed --hostname opendevops.ai --service http://127.0.0.1:8082
./ztd gw tunnel run
```

Start the Gateway with `--public-base-url https://opendevops.ai` so the embedded explorer points at the public origin.

## Publish to a remote Cloud Gateway

When the Gateway runs on a cloud VM with direct mirror ingress (`--public-spectator-public-listen 0.0.0.0:8082 --public-spectator-allow-container-bind`; see [cloud deployment](../devs/cloud_deployment.md)), publish and restore from a workstation with `-e / --endpoint`. The CLI derives the anonymous mirror read origin `http://<gateway-host>:8082` from the endpoint host. Pass `--mirror-origin` to override it, for example when the mirror is fronted by a domain:

```bash
./ztd -e <GATEWAY_IP> public restore --queue
./ztd -e <GATEWAY_IP> eval runs publish --mirror-origin https://opendevops.ai
./ztd -e <GATEWAY_IP> public status --mirror-origin http://<GATEWAY_IP>:8082
```

`--mirror-origin` is the public read origin, never the private ingest listener (`127.0.0.1:8081`). Writes still reach the Gateway over its authenticated API; the mirror origin is used only for anonymous presence and status reads.

## External acceptance

After starting the tunnel, verify HTTPS, CORS, cache headers, rate limiting, `pointer.json` and its read model, live status, proof catalog, proof head, signed proof segments, and content-addressed proof download from an external client. Check that `pointer.json` carries `Cache-Control: public, max-age=30` and that a read model carries the immutable header.

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

`public status` also compares each run's stored verification report with the verifier state the read model publishes, and lists disagreements in `feed_drift`, with a warning per run on stderr. `feed_drift_checked` is `false`, with the cause in `feed_drift_error` and a warning, when the stored reports or the read model behind the comparison cannot be read. Pass `--mirror-origin` when the public mirror is not at the default origin. See [Feed Drift](../devs/troubleshooting.md#feed-drift) to clear a reported run.

`public publish` durably appends records before transmission. `public push` retries outbox delivery and pushes the proof package. When publication is Gateway-owned (no host publisher configuration and a healthy Gateway), `public push` prints that the Gateway-owned public mirror is already synchronized and does nothing. `public restore --queue` republishes completed, passing verified runs from the campaign queue to the mirror.

## Links out

- [Public Spectator Architecture and Threat Model](../architecture/public_spectator.md) — Mirror threat model and outbox mechanics.
- [Unified Stack Guide](unified_stack.md) — Host-native evaluation stack.
- [Generator-Neutral Builder Guide](build_observe_frontend.md) — Observe frontend builder specification.
- [Cloudflare Tunnel Integration](cloudflare_tunnel.md) — Cloudflare tunnel configuration.
