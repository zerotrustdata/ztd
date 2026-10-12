---
doc_id: lovable
title: Connect a Lovable App to the Gateway
audience: frontend builders and developers
status: current
version: v1.0.0
last_updated: 2026-10-12
owners:
  - docs/guides/lovable.md
  - internal/cli/cmd/gw/gateway_connect.go
  - ztd-adapter/
related:
  - docs/guides/build_frontend.md
  - docs/guides/build_observe_frontend.md
  - docs/guides/cloudflare_tunnel.md
when_to_read: Connecting a Lovable app or other browser-hosted frontend to a local ztd Gateway for development, testing, or secure observe workflows.
do_not_use_for:
  - Production Gateway configuration beyond the browser origin (use docs/guides/cloudflare_tunnel.md)
  - Operator or consensus configuration (use docs/devs/devs.md)
  - Contract pack generation and testing (use ztd-adapter/contract-pack/README.md)
---

# Connect a Lovable App to the Gateway

## Purpose

Describes how to connect a Lovable application (or any browser-hosted frontend) running locally to a ztd Gateway on the same machine. The browser communicates directly with the Gateway over HTTPS; no tunnel is required for local development. The workflow validates the frontend origin, configures WebAuthn and CORS, manages local certificate trust, and verifies end-to-end connectivity before handing off to the frontend builder.

For the complete browser API, WebAuthn, SSE, and frontend reference, see [Build a ztd-Compatible Frontend](build_frontend.md).

## Quick index

- [Purpose](#purpose)
- [Quick index](#quick-index)
- [Invariants](#invariants)
- [Owned surfaces](#owned-surfaces)
- [Procedures](#procedures)
  - [Before You Start](#before-you-start)
  - [1. Connect the Frontend](#1-connect-the-frontend)
  - [2. Apply the Emitted Frontend Prompt](#2-apply-the-emitted-frontend-prompt)
  - [Observe Frontend Contract Pack](#observe-frontend-contract-pack)
  - [Browser Limitations](#browser-limitations)
  - [Troubleshooting](#troubleshooting)
  - [Advanced Configuration](#advanced-configuration)
  - [When You Need a Tunnel](#when-you-need-a-tunnel)
- [Anti-patterns](#anti-patterns)
- [Links out](#links-out)

## Invariants

| ID | Rule |
| :--- | :--- |
| INV-LOVABLE-01 | `./ztd gw connect <origin>` MUST validate and canonicalize the frontend origin before any Gateway start or restart. Non-loopback origins MUST use HTTPS; HTTP is accepted only for loopback development origins (e.g., `http://localhost:3003`). |
| INV-LOVABLE-02 | The WebAuthn RP ID MUST default to the exact host extracted from the frontend origin (e.g. `your-app.lovable.app` from `https://your-app.lovable.app`). Loopback IP origins MUST use `localhost` as the RP ID. |
| INV-LOVABLE-03 | The browser MUST call the Gateway directly from the top-level page context, not from an embedded editor preview, edge function, or server proxy. Every `fetch` request MUST use `credentials: "include"`; every `EventSource` connection MUST use `withCredentials: true`. |
| INV-LOVABLE-04 | Browser restrictions (local-network access prompts, top-level context requirements, third-party cookie blocking, certificate trust) are enforced by the browser and outside the CLI's control. |

## Owned surfaces

| Claim | Path | Verify |
| :--- | :--- | :--- |
| gw connect command | `internal/cli/cmd/gw/gateway_connect.go` | `./ztd gw connect --help` |
| gw start flags | `internal/cli/cmd/gw/gateway.go` | `./ztd gw start --help` |
| Contract pack generator | `ztd-adapter/` | `npm run gen:contract-pack` |
| Contract pack README | `ztd-adapter/contract-pack/README.md` | Contract pack documentation |

## Procedures

### Before You Start

- Build the `ztd` binary at the repository root (`make build`).
- Ensure the Lovable app is deployed or previewed at a top-level browser origin (e.g. `https://your-app.lovable.app`). Extract the origin from the address bar of a standalone browser tab, not from an embedded preview or editor URL.
- Use a browser supporting WebAuthn.

The CLI accepts only canonical origins: scheme + hostname + optional non-default port, with no path, query, fragment, or trailing slash.

### 1. Connect the Frontend

Run the connect command from the repository root:

```bash
./ztd gw connect https://your-app.lovable.app
```

The command:
1. Validates the frontend origin.
2. Derives the WebAuthn RP ID (`your-app.lovable.app`).
3. Starts the Gateway with matching CORS and passkey RP origins, or restarts it with confirmation if configuration changed.
4. Discovers the CA bundle and checks OS certificate trust. Installs root CA with consent.
5. Runs HTTPS health and CORS preflight verification against the running Gateway.
6. Emits the Gateway HTTPS URL and frontend prompt upon success.

Use `--yes` to approve a managed Gateway restart non-interactively. Certificate trust installation retains a separate interactive consent prompt.

To skip OS trust installation and perform manual verification:

```bash
./ztd gw connect https://your-app.lovable.app --no-system-trust
```

### 2. Apply the Emitted Frontend Prompt

After verification succeeds, `gw connect` prints a prompt:

```text
Connect this app to the ztd Gateway at https://localhost:8443. Make all Gateway requests directly from the browser, not from an edge function or server. Include credentials: "include" on every fetch request and withCredentials: true on every EventSource connection.
```

Paste that exact prompt into Lovable. Do not substitute server-side or edge-function implementations.

Open the resulting app in a standalone, top-level browser tab, not embedded inside an iframe. If prompted for local-network access, select **Allow**.

### Observe Frontend Contract Pack

For a read-only observe dashboard (lifecycle projections, evaluation summaries, downloads, live SSE narrative), use the deterministic contract pack at `ztd-adapter/contract-pack/`:

1. Verify the pack is current:
   ```bash
   npm run gen:contract-pack:check --prefix ztd-adapter
   ```
2. Pass `contract-pack/builder-prompt.md`, `models.ts`, `observe.openapi.json`, and `event-schemas.json` to Lovable.
3. Deploy the resulting SPA at a top-level browser origin and connect it:
   ```bash
   ./ztd gw connect <deployed-spa-origin>
   ```

### Browser Limitations

- **Local Network Access**: Browsers may request permission to contact local loopback devices. Select **Allow**.
- **Top-Level Context**: Editor iframes block WebAuthn ceremonies and local fetch requests; test in a standalone tab.
- **Third-Party Cookies**: Hosted origins (e.g. `https://your-app.lovable.app`) calling `https://localhost:8443` are cross-site. Browsers that block third-party cookies reject `SameSite=None` cookies, returning `401` on authenticated calls. Deploy frontend and Gateway under the same site or use a tunnel if third-party cookies are blocked.
- **Certificate Trust**: The Gateway's local HTTPS certificate must be trusted by the OS or browser.

### Troubleshooting

- **`Failed to fetch`**: Confirm Gateway is running (`./ztd gw status`), open app in a top-level tab, and allow local network access.
- **WebAuthn `SecurityError`**: Ensure the URL origin in the browser address bar matches the origin passed to `gw connect`.
- **401 Unauthorized after login**: Ensure fetch includes `credentials: "include"`. If cookies are blocked by browser privacy policies, deploy frontend and Gateway on the same domain or use a Cloudflare Tunnel.

### When You Need a Tunnel

Use a tunnel when:
- The Gateway must be reached by users on other machines.
- Integration tests execute in remote CI environments.
- Public browser-trusted endpoints are required.

See [Cloudflare Tunnel Integration](cloudflare_tunnel.md).

## Advanced Configuration

For multi-origin setups or custom ports, start the Gateway with explicit flags:

```bash
./ztd gw start \
  --cors-origin https://app1.example.com \
  --cors-origin https://app2.example.com \
  --passkey-rp-id example.com \
  --passkey-rp-origin https://app1.example.com \
  --passkey-rp-origin https://app2.example.com \
  --public-base-url https://example.com
```

## Anti-patterns

- Using edge functions or server proxies to relay requests to the Gateway instead of calling directly from the browser.
- Running the frontend inside an iframe preview rather than a top-level browser tab.
- Omitting `credentials: "include"` on fetch or `withCredentials: true` on EventSource.
- Disabling TLS verification or ignoring certificate validation errors.

## Links out

- [Build a ztd-Compatible Frontend](build_frontend.md) — Browser API, WebAuthn, and SSE reference.
- [Generator-Neutral Builder Guide](build_observe_frontend.md) — Observe frontend builder specification.
- [Cloudflare Tunnel Integration](cloudflare_tunnel.md) — Exposing the Gateway to remote clients.
- [Console Architecture](../architecture/console.md) — Reference console implementation.
