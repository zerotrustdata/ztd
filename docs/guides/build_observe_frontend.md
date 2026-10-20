---
doc_id: build_observe_frontend
title: Generator-Neutral Builder Guide
audience: SPA builders and frontend developers
status: current
version: v1.2.0
last_updated: 2026-10-20
owners:
  - docs/guides/build_observe_frontend.md
  - ztd-adapter/
  - protocol/docs/reference/
related:
  - docs/guides/build_frontend.md
  - docs/guides/lovable.md
  - docs/guides/public_spectator.md
  - docs/guides/cloudflare_tunnel.md
  - docs/architecture/sse.md
  - docs/architecture/auth.md
when_to_read: Building a read-only observe frontend via SPA builder (Lovable, Notion, etc.) consuming the ztd-adapter contract pack, or understanding adapter runtime requirements and capability guarantees.
do_not_use_for:
  - Custom frontend development without builders (docs/guides/build_frontend.md)
  - Local Lovable quick setup (docs/guides/lovable.md)
  - Public Spectator deployments (docs/guides/public_spectator.md)
  - Gateway authentication details (docs/architecture/auth.md)
---

# Generator-Neutral Builder Guide

## Purpose

This guide explains runtime capability requirements and integration guarantees that a generated observe frontend must satisfy. A generated observe frontend is a read-only browser dashboard that displays agent and run lifecycle projections, evaluation summaries, downloads, and a live SSE narrative. It is produced by a builder (Lovable or another supported SPA builder) consuming the deterministic contract pack from `ztd-adapter/contract-pack/`. The builder produces a deployable single-page application that runs in a top-level browser tab and connects directly to a ztd Gateway with passkey authentication.

This guide covers the owner-local observe frontend specifically and the audited adapter boundary that builders must respect. It is separate from the anonymous [Public Spectator](../architecture/public_spectator.md) mirror. For minimal Lovable setup, see [Connect a Lovable App](./lovable.md). For full browser integration reference, see [Build a ztd-Compatible Frontend](./build_frontend.md).

## Quick index

- [Purpose](#purpose)
- [Quick index](#quick-index)
- [Prerequisites and workflow](#prerequisites-and-workflow)
- [Architecture](#architecture)
- [The audited adapter](#the-audited-adapter)
- [The contract pack](#the-contract-pack)
- [Invariants](#invariants)
- [Adapter capabilities](#adapter-capabilities)
- [Display requirements](#display-requirements)
- [Prohibited displays](#prohibited-displays)
- [Accessibility and responsive behavior](#accessibility-and-responsive-behavior)
- [Acceptance](#acceptance)
- [Anti-patterns](#anti-patterns)
- [Links out](#links-out)

## Prerequisites and workflow

The builder output is a browser SPA. Use Node.js 22.22.2+, 24.15+, or 26+ and the package manager required by the generated project.

1. Consume `contract-pack/builder-prompt.md`, `runtime-config.schema.json`, `observe.openapi.json`, `event-schemas.json`, `models.ts`, and `fixtures/` as builder inputs. Do not copy the adapter's transport implementation into generated presentation code.
2. Generate a top-level SPA that imports the built `ztd-adapter` package.
3. Inject a JSON script tag with id `ztd-runtime-config`:

```html
<script type="application/json" id="ztd-runtime-config">
{
  "schema_version": "1.0.0",
  "gateway_base_url": "https://localhost:8443",
  "passkey_rp_id": "localhost",
  "passkey_rp_name": "ztd",
  "app_name": "ztd"
}
</script>
```

4. Connect the origin to the Gateway with `./ztd gw connect <frontend-origin>`, then open the built SPA in a top-level browser tab.
5. In live mode, authenticate with WebAuthn, load the typed observe bootstrap snapshot, and start `SseStream`. On `onReconcile`, reload the bootstrap snapshot. On `onUnauthenticated`, redirect to sign-in.

## Architecture

```text
[Builder output (generated SPA)] -> imports -> [ztd-adapter (audited)]
        |                                              |
  presentation code only                        transport, auth, SSE, state
        |                                              |
        +------------------ top-level browser ---------+
                                  |
                      https://localhost:8443 (ztd Gateway)
```

The generated SPA wraps the audited `ztd-adapter` package. The adapter owns runtime configuration parsing, endpoint allowlist enforcement, credentialed fetch, WebAuthn ceremonies, SSE normalization, reconciliation signals, typed stores, and the safe presentation registry.

## The audited adapter

The `ztd-adapter/` package is the audited integration core. It exposes:

- `parseRuntimeConfig`: Validates a parsed `FrontendRuntimeConfig` object, such as the JSON read from the `ztd-runtime-config` script tag.
- `createCredentialedFetch`: Enforces allowlisted operations and sets `credentials: 'include'`.
- `createObserveClient`: Typed client for the 7 observe endpoints (bootstrap, runs, run detail, evals, eval detail, downloads, download detail).
- `registerPasskey`, `authenticatePasskey`, `enrollPasskey`: WebAuthn ceremonies.
- `SseStream`: Manages EventSource on `/api/v1/sse/stream` with reconnect backoff and reconciliation signals.
- `normalizeGatewayEvent`: Parses outer envelopes and validates recognized payloads.
- `adapterReducer`: Pure reducers for auth, projections, narrative, transport, and runtime feature stores.
- Error classes, for `instanceof` checks: `RuntimeConfigError` (invalid runtime config), `GatewayFetchError` (failed Gateway request), `EndpointNotAllowedError` (operation outside the allowlist), `ObserveResponseValidationError` (malformed observe response), `NormalizedEventError` (malformed SSE event).

## The contract pack

The `ztd-adapter/contract-pack/` directory contains deterministic, generator-neutral inputs:

| File | Purpose |
| :--- | :--- |
| `builder-prompt.md` | Specification prompt given to the SPA builder |
| `runtime-config.schema.json` | JSON Schema for `FrontendRuntimeConfig` |
| `observe.openapi.json` | Curated OpenAPI 3.0 for allowlisted browser operations |
| `event-schemas.json` | Dashboard event payloads and sentinel definitions |
| `models.ts` | TypeScript models and validators |
| `fixtures/` | Typed fixture scenarios for UI states |
| `manifest.json` | Output hashes for drift detection |

## Invariants

| ID | Rule |
| :--- | :--- |
| INV-OBSERVE-BUILDER-01 | The adapter boundary is inviolate. Generated code MUST import from the audited adapter and call only its exported APIs. Generated code MUST NOT reimplement transport, auth, SSE parsing, or allowlist enforcement. |
| INV-OBSERVE-BUILDER-02 | All Gateway requests MUST execute in the top-level browser context. Server-side proxies, service-worker relays, and iframe delegation MUST NOT be used. |
| INV-OBSERVE-BUILDER-03 | The configured Gateway origin from `FrontendRuntimeConfig` MUST be used for every request. Hardcoded origins and relative URLs MUST NOT be used. |
| INV-OBSERVE-BUILDER-04 | Credentials MUST be included on every fetch (`credentials: 'include'`) and EventSource (`withCredentials: true`). |
| INV-OBSERVE-BUILDER-05 | Only allowlisted endpoints in the adapter are reachable. Routes for mutation, approval, tool execution, or evaluation launch MUST NOT be called. |

## Adapter capabilities

- Preserves the audited adapter boundary.
- Executes all requests in the top-level browser context.
- Uses absolute configured Gateway URLs.
- Passes credentials cross-origin via HttpOnly session cookies.
- Normalizes SSE events into bounded presentation queues.

## Display requirements

Generated UI MUST populate its sections strictly from typed stores:
- Connection state: disconnected, connecting, connected, reconnecting, unauthenticated.
- Auth state: unauthenticated, authenticating, enrolling, authenticated, or error.
- Overview counters: agents running and queue length. Success rate appears only when a `success_rate` measurement is present.
- Overview measurements: observed throughput appears only when a `total_throughput` measurement is present.
- Agent roster: display name, role, status label, and freshness.
- Active run and recent runs: run kind, status, completed/total tasks.
- Latest evaluations: run ID, verification status, receipt count, metric count.
- Downloads: filename, media type, size, SHA-256, and authenticated URL.
- Live narrative: virtualized scrollable log of normalized safe events.

## Prohibited displays

Generated UI MUST NOT display throughput, CPU, RAM, VRAM, disk, parameter counts, quantization, or verification claims unless corresponding typed observed sources exist in the store. Generated UI MUST NOT render raw event payloads or model chain-of-thought traces.

## Accessibility and responsive behavior

Generated UI MUST provide:

- Keyboard navigation across all interactive controls with visible focus indicators.
- Semantic HTML landmarks (`header`, `main`, `nav`, `section`).
- Non-color status indicators (text labels alongside color badges).
- Reduced-motion support (`prefers-reduced-motion`).
- Virtualized event lists to prevent UI thread starvation.

## Acceptance

Run verification from `ztd-adapter/`:

```bash
npm run gen:contract-pack:check   # verify contract pack is current
npm test                          # run adapter tests
npm run lint                      # type-check adapter, host, and reference UI
npm run build                     # build audited adapter
npm run build:host                # build the minimal host
```

## Anti-patterns

- Reimplementing transport, auth, or SSE parsing outside the adapter.
- Bypassing the allowlist with generic fetch wrappers.
- Hardcoding Gateway origins instead of reading `FrontendRuntimeConfig`.
- Displaying unsourced metrics without typed backend telemetry.
- Mixing mock fixture data with connected live data.

## Links out

- [Build a ztd-Compatible Frontend](build_frontend.md) — Comprehensive frontend building reference.
- [Connect a Lovable App](lovable.md) — Local Lovable setup with `gw connect`.
- [Public Spectator Operations Guide](public_spectator.md) — Anonymous public mirror.
- [SSE Architecture](../architecture/sse.md) — Event streaming architecture.
- [Contract Pack README](../../ztd-adapter/contract-pack/README.md) — Deterministic generation reference.
