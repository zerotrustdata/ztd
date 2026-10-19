---
doc_id: build_frontend
title: Build a ztd-Compatible Frontend
audience: frontend developers, web application builders
status: current
version: v1.0.0
last_updated: 2026-10-12
owners:
  - docs/guides/build_frontend.md
  - docs/guides/build_observe_frontend.md
  - internal/services/gateway/
related:
  - docs/guides/lovable.md
  - docs/guides/build_observe_frontend.md
  - docs/guides/public_spectator.md
  - docs/guides/cloudflare_tunnel.md
  - docs/guides/connect_apps_to_gateway.md
  - docs/architecture/auth.md
  - docs/architecture/gateway.md
  - docs/architecture/sse.md
when_to_read: Building or integrating a browser-hosted frontend with ztd Gateway authentication, WebAuthn ceremonies, SSE streaming, transaction approvals, or observe API endpoints.
do_not_use_for:
  - Deployment architecture and tunnel setup (docs/guides/cloudflare_tunnel.md, docs/guides/public_spectator.md)
  - CLI authentication flows and token-gated enrollment (docs/guides/connect_apps_to_gateway.md)
  - WebAuthn cryptography and threat modeling (docs/architecture/auth.md)
---

# Build a ztd-Compatible Frontend

## Purpose

Describes how to build a ztd-compatible browser frontend. The guide covers Gateway configuration, WebAuthn passkey ceremonies, SSE audit streaming, transaction approvals, observe API access, component structure, and integration testing. It applies to custom React, Vue, and vanilla JavaScript SPAs, Lovable-generated frontends, and any browser app that communicates directly with the Gateway over HTTPS with passkey authentication.

For minimal local Lovable setup with guided one-command configuration, see [Connect a Lovable App](./lovable.md). For generator-neutral observe frontends, see [Generator-Neutral Builder Guide](./build_observe_frontend.md).

## Quick index

- [Purpose](#purpose)
- [Quick index](#quick-index)
- [Prerequisites](#prerequisites)
- [Gateway-Side Configuration](#gateway-side-configuration)
- [API Reference](#api-reference)
- [WebAuthn Flow Requirements](#webauthn-flow-requirements)
- [SSE Live Audit Stream](#sse-live-audit-stream)
- [Observe API](#observe-api-read-only-browser-surface)
- [Pages and Components](#pages-and-components)
- [Error Handling](#error-handling)
- [Recommended Project Structure](#recommended-project-structure)
- [Frontend Integration Checklist](#frontend-integration-checklist)
- [Troubleshooting](#troubleshooting)
- [Links out](#links-out)

The browser SPA communicates with the ztd Gateway over HTTPS at the configured origin. Authentication uses WebAuthn passkeys (no passwords, no API keys). The Gateway issues an HttpOnly, Secure session cookie after successful passkey verification. All authenticated API calls include `credentials: 'include'` to send the session cookie cross-origin. Real-time telemetry arrives via Server-Sent Events (SSE), not WebSockets (the WebSocket endpoint at `/api/v1/pubsub/stream` requires mTLS and is not browser-accessible).

The Gateway ships with an embedded console at `/console/` (React and TypeScript, source in `console/`) that implements passkey registration and authentication, transaction approvals, CLI recovery approval, platform workload enrollment approval, and Operator inventory and binding.

## Prerequisites

- ztd Gateway running and healthy (`./ztd gw start`).
- Frontend application served from a known top-level origin (e.g. `https://your-app.example.com`, `http://localhost:3003`).
- Gateway started with `--cors-origin` and `--passkey-rp-origin` matching the frontend origin.
- Browser with WebAuthn support and trust in the Gateway's HTTPS certificate.

For same-computer local frontends, run `./ztd gw connect <frontend-origin>` to automate origin configuration and trust installation.

## Gateway-Side Configuration

```bash
./ztd gw start \
  --passkey-rp-id example.com \
  --passkey-rp-name "ztd Console" \
  --passkey-rp-origin https://your-app.example.com \
  --cors-origin https://your-app.example.com
```

- `--passkey-rp-id`: WebAuthn Relying Party ID. Must be the frontend origin hostname or a registrable parent domain suffix.
- `--passkey-rp-origin`: Allowed origin for WebAuthn ceremonies.
- `--cors-origin`: Allowed origin for CORS fetch requests.
- `--public-base-url`: Public URL of the Gateway for approval redirect links.

### Session Cookie Behavior

The Gateway sets `ztd_web_session_cookie` on successful passkey verification:
- `HttpOnly`: Always true.
- `Secure`: Always true.
- `SameSite=Lax`: Default when no cross-origin origins are configured.
- `SameSite=None`: Automatically set when `--cors-origin` is specified.

## API Reference

All paths are relative to the Gateway's base URL. Authenticated routes require `credentials: 'include'`.

### Public Routes

| Method | Path | Description |
| :--- | :--- | :--- |
| `GET` | `/api/v1/health` | Gateway health check |
| `GET` | `/api/v1/auth/bootstrap/status` | Report whether Gateway has an owner user |
| `POST` | `/api/v1/auth/passkeys/console/register/challenge` | Get bootstrap WebAuthn registration challenge |
| `POST` | `/api/v1/auth/passkeys/console/register/verify` | Verify bootstrap registration attestation and create web session |
| `POST` | `/api/v1/auth/passkeys/console/authenticate/challenge` | Get WebAuthn authentication challenge for a user ID |
| `POST` | `/api/v1/auth/passkeys/console/authenticate/verify` | Verify authentication assertion and create web session |
| `POST` | `/api/v1/auth/passkeys/enrollment/register/challenge` | Get token-gated registration challenge |
| `POST` | `/api/v1/auth/passkeys/enrollment/register/verify` | Verify token-gated registration and create web session |
| `POST` | `/api/v1/auth/logout` | End session and clear session cookie |

### Authenticated Routes

| Method | Path | Description |
| :--- | :--- | :--- |
| `GET` | `/api/v1/users/me` | Get current authenticated user |
| `GET` | `/api/v1/auth/sessions/me` | Get current web session information |
| `GET` | `/api/v1/auth/passkeys` | List user's passkey credentials |
| `DELETE` | `/api/v1/auth/passkeys/{credentialId}` | Revoke a passkey credential |
| `GET` | `/api/v1/approvals` | List pending suspended transactions |
| `POST` | `/api/v1/approvals/respond` | Deny an approval request |
| `GET` | `/api/v1/approvals/{txHash}/challenge` | Get WebAuthn approval challenge |
| `POST` | `/api/v1/approvals/{txHash}/verify` | Verify approval assertion |
| `GET` | `/api/v1/sse/stream` | SSE stream for live audit events |
| `GET` | `/api/v1/sse/events?since_id={n}&limit={n}` | Query stored SSE events (backfill) |

## WebAuthn Flow Requirements

Binary fields in challenges and responses use unpadded base64url encoding.

### Registration Flow (First-Time Setup)

1. Verify `GET /api/v1/auth/bootstrap/status` returns `{ "bootstrapped": false }`.
2. POST `/api/v1/auth/passkeys/console/register/challenge` with `{ "user_name": "Admin", "cli_session_id": "browser" }`.
3. Save returned `user_id`. Decode `options.publicKey` and invoke `navigator.credentials.create({ publicKey })`.
4. POST `/api/v1/auth/passkeys/console/register/verify` with `{ "user_id": <user_id>, "cli_session_id": "browser", "attestation_response": <flat attestation> }`.
5. On success, Gateway sets `ztd_web_session_cookie`. Load user data from `/api/v1/users/me`.

### Authentication Flow (Returning User)

1. POST `/api/v1/auth/passkeys/console/authenticate/challenge` with `{ "user_id": <user_id> }`.
2. Decode `options.publicKey` and invoke `navigator.credentials.get({ publicKey })`.
3. POST `/api/v1/auth/passkeys/console/authenticate/verify` with `{ "user_id": <user_id>, "assertion_response": <flat assertion> }`.
4. On success, Gateway sets `ztd_web_session_cookie`.

### Approval Flow (Suspended Transaction)

1. GET `/api/v1/approvals/{txHash}/challenge` with credentials.
2. Decode `publicKey` and invoke `navigator.credentials.get({ publicKey })`.
3. POST `/api/v1/approvals/{txHash}/verify` with flat assertion as the entire JSON body.
4. Response 200 contains the executed `ActionReceipt`.

## SSE Live Audit Stream

Connect using `EventSource` with `withCredentials: true`:

```javascript
const source = new EventSource('https://gateway-host:8443/api/v1/sse/stream', { withCredentials: true });

source.addEventListener('message', (event) => {
  const envelope = JSON.parse(event.data);
  console.log('Audit event:', envelope);
});
```

Session routing is derived from the cookie. Do not append `user_id` or `web_session_id` in query strings.

## Observe API (Read-Only Browser Surface)

| Method | Path | Description |
| :--- | :--- | :--- |
| `GET` | `/api/v1/observe/bootstrap` | Bootstrap snapshot: agents, runs, metrics, downloads |
| `GET` | `/api/v1/observe/runs?cursor=&limit=` | Paginated run summaries |
| `GET` | `/api/v1/observe/runs/{run_id}` | Run detail with tasks and evidence links |
| `GET` | `/api/v1/observe/evals` | Paginated evaluation summaries |
| `GET` | `/api/v1/observe/evals/{run_id}` | Evaluation detail with metrics |
| `GET` | `/api/v1/observe/downloads` | Paginated download artifacts |

## Frontend Integration Checklist

- [ ] CORS headers: Verify `Access-Control-Allow-Origin` and `Access-Control-Allow-Credentials: true`.
- [ ] Preflight: Verify OPTIONS preflight returns 204 No Content.
- [ ] Registration: Test bootstrap passkey creation.
- [ ] Authentication: Test returning user passkey sign-in.
- [ ] Session cookie: Confirm `ztd_web_session_cookie` is HttpOnly and Secure.
- [ ] API calls: Confirm `GET /api/v1/users/me` returns 200 with credentials.
- [ ] SSE: Verify live audit stream receives push events.
- [ ] Approvals: Verify WebAuthn approval resumes suspended transactions.

## Links out

- [Connect a Lovable App](lovable.md) — Fast-path local Lovable setup.
- [Generator-Neutral Builder Guide](build_observe_frontend.md) — Requirements for generated observe frontends.
- [Connect Frontend to Gateway](connect_frontend_to_gateway.md) — Wire contracts and connection details.
- [Public Spectator Operations Guide](public_spectator.md) — Anonymous public mirror.
- [Authentication Architecture](../architecture/auth.md) — WebAuthn passkey mechanics.
