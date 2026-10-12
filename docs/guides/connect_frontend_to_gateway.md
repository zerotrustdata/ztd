---
doc_id: connect_frontend_to_gateway
title: Connect Frontend to Gateway
audience: frontend developers integrating with ztd
status: current
version: v1.0.0
last_updated: 2026-10-12
owners:
  - docs/guides/connect_frontend_to_gateway.md
  - internal/services/gateway/
related:
  - docs/guides/build_frontend.md
  - docs/guides/lovable.md
  - docs/guides/cloudflare_tunnel.md
  - docs/architecture/auth.md
  - docs/architecture/gateway.md
when_to_read: Connecting a browser-based frontend application to the ztd Gateway via WebAuthn passkeys, CORS, SSE, and session authentication.
do_not_use_for:
  - Building a frontend from scratch (docs/guides/build_frontend.md)
  - Gateway deployment topology (docs/guides/build_gateway.md)
  - Application-to-gateway connectivity (docs/guides/connect_apps_to_gateway.md)
---

# Connect Frontend to Gateway

## Purpose

This guide describes how browser-hosted, JavaScript-based frontend applications authenticate with the ztd Gateway via WebAuthn passkeys, establish HTTPS connections, receive Server-Sent Events (SSE), approve suspended transactions, and manage credentials.

For single-origin local development, the `./ztd gw connect <frontend-origin>` workflow automates CORS and passkey configuration, certificate trust installation, and HTTPS verification. Advanced deployments use `./ztd gw start` flags for multi-origin and public-tunnel scenarios.

## Quick index

- [Purpose](#purpose)
- [Quick index](#quick-index)
- [Invariants](#invariants)
- [Owned surfaces](#owned-surfaces)
- [Procedures](#procedures)
- [Anti-patterns](#anti-patterns)
- [Links out](#links-out)

## Invariants

### API routing and authentication (`INV-FE-AUTH`)

| ID | Rule |
| :--- | :--- |
| INV-FE-AUTH-01 | Browser-accessible routes requiring web sessions MUST use `GET /api/v1/users/me`, `GET /api/v1/auth/sessions/me`, `GET /api/v1/auth/passkeys`, `DELETE /api/v1/auth/passkeys/{credentialId}`, `GET /api/v1/approvals`, `GET /api/v1/approvals/{txHash}/challenge`, `POST /api/v1/approvals/{txHash}/verify`, `GET /api/v1/sse/stream`, and `GET /api/v1/sse/events`. |
| INV-FE-AUTH-02 | Public endpoints (no auth required) MUST include passkey registration and authentication challenges, bootstrap status, enrollment token validation, logout, and CLI recovery approval routes. |
| INV-FE-AUTH-03 | Every session-authenticated call MUST include `credentials: 'include'` in the fetch options so the Gateway's `ztd_web_session_cookie` is sent. |
| INV-FE-AUTH-04 | API responses MUST be interpreted through HTTP status codes: 200 (success), 401 (not authenticated, redirect to login), 403 (forbidden by policy), 404 (not found), 409 (conflict), 410 (expired token), 5xx (server error). |

### Cookie and CORS behavior (`INV-FE-CORS`)

| ID | Rule |
| :--- | :--- |
| INV-FE-CORS-01 | The Gateway sets `ztd_web_session_cookie` after successful passkey verification. The cookie is always `HttpOnly` and `Secure`. |
| INV-FE-CORS-02 | When no cross-origin origins are configured, the cookie uses `SameSite=Lax`. When `--cors-origin` is non-empty, the cookie uses `SameSite=None`. |
| INV-FE-CORS-03 | The Gateway CORS middleware reflects exact-match origins in `Access-Control-Allow-Origin` and sets `Access-Control-Allow-Credentials: true` when the request origin matches the allowed set. |
| INV-FE-CORS-04 | The CORS-origin list is configured at Gateway startup via `--cors-origin`. Changes require a Gateway restart. |

### WebAuthn flows (`INV-FE-WA`)

| ID | Rule |
| :--- | :--- |
| INV-FE-WA-01 | WebAuthn challenges are base64url-encoded by the Gateway; the frontend MUST decode them to `ArrayBuffer` before calling browser credential APIs. Responses MUST be re-encoded before sending to Gateway verify endpoints. |
| INV-FE-WA-02 | The RP ID MUST match the registrable domain of the frontend page's origin, or browser ceremonies fail. |
| INV-FE-WA-03 | The console registration route permits only the first credential per user. Subsequent credentials MUST be added via the CLI token-gated enrollment flow (`ztd auth enroll user`). |
| INV-FE-WA-04 | Approval WebAuthn ceremonies (for suspended transactions) do NOT create a new session; they issue an `ActionReceipt` that resumes execution. |

## Owned surfaces

| Claim | Path | Verify |
| :--- | :--- | :--- |
| Browser credential flows | `internal/services/gateway/passkey_service_http.go` | Register and authenticate challenges, WebAuthn verification |
| CORS and session cookie behavior | `internal/services/gateway/passkey_service_http.go` | Cookie attributes and headers |
| Web session routes | `internal/services/gateway/gateway_http_router.go` | Route classification and middleware |
| SSE endpoints | `internal/services/gateway/gateway_http_router.go` | Stream and polling delivery |

## Procedures

### Configure the Gateway for Your Frontend Origin

For a single-origin local frontend, use the guided workflow:

```bash
./ztd gw connect https://localhost:3000
```

This command validates the origin, derives the RP ID, starts or restarts the Gateway with matching CORS and passkey settings, installs local certificate trust with consent, and verifies HTTPS and CORS against the running process.

For multi-origin or public deployments, use explicit `gw start` flags:

```bash
./ztd gw start \
  --passkey-rp-id example.com \
  --passkey-rp-name "ztd Console" \
  --passkey-rp-origin https://app.example.com \
  --cors-origin https://app.example.com
```

### Centralized Fetch Wrapper

Include `credentials: 'include'` on all API calls:

```javascript
const API_BASE = 'https://gateway-host:8443';

async function gatewayFetch(path, options = {}) {
  const url = new URL(path, API_BASE);
  const mergedOptions = {
    ...options,
    credentials: 'include',
    headers: {
      'Content-Type': 'application/json',
      ...options.headers,
    },
  };
  const response = await fetch(url, mergedOptions);
  if (response.status === 401) {
    window.location.href = '/login';
  }
  return response;
}
```

### Check Authentication State

```javascript
async function initAuth() {
  const bootstrapResp = await gatewayFetch('/api/v1/auth/bootstrap/status');
  const bootstrap = await bootstrapResp.json();
  const hasOwner = bootstrap.bootstrapped;

  try {
    const userResp = await gatewayFetch('/api/v1/users/me');
    if (userResp.status === 200) {
      const user = await userResp.json();
      return { authenticated: true, user, hasOwner };
    }
  } catch (e) {
    // Unauthenticated
  }
  return { authenticated: false, hasOwner };
}
```

### WebAuthn Registration (First-Time Bootstrap)

1. Verify `GET /api/v1/auth/bootstrap/status` returns `{ "bootstrapped": false }`.
2. POST `/api/v1/auth/passkeys/console/register/challenge` with `{ "user_name": "Admin", "cli_session_id": "browser" }`.
3. Decode `options.publicKey` and invoke `navigator.credentials.create({ publicKey })`.
4. POST `/api/v1/auth/passkeys/console/register/verify` with flat encoded attestation response.
5. On success (200), the Gateway sets `ztd_web_session_cookie`.

### WebAuthn Authentication (Returning User)

1. POST `/api/v1/auth/passkeys/console/authenticate/challenge` with `{ "user_id": <user_id> }`.
2. Decode `options.publicKey` and invoke `navigator.credentials.get({ publicKey })`.
3. POST `/api/v1/auth/passkeys/console/authenticate/verify` with flat encoded assertion response.
4. On success (200), the Gateway sets `ztd_web_session_cookie`.

### SSE Live Audit Stream

Connect using native `EventSource` with credentials:

```javascript
const source = new EventSource('https://gateway-host:8443/api/v1/sse/stream', { withCredentials: true });

source.addEventListener('message', (event) => {
  const envelope = JSON.parse(event.data);
  console.log('Received audit event:', envelope);
});

source.addEventListener('error', () => {
  console.error('SSE connection error; reconnecting...');
});
```

The Gateway derives `user_id` and session routes from the cookie; do not pass them in URL query parameters.

### Approvals for Suspended Transactions

Fetch pending transactions via `GET /api/v1/approvals`. To approve:

1. GET `/api/v1/approvals/{txHash}/challenge` with credentials.
2. Decode `publicKey` and call `navigator.credentials.get({ publicKey })`.
3. POST `/api/v1/approvals/{txHash}/verify` with flat assertion as the entire JSON body.
4. Response 200 contains the executed `ActionReceipt`.

## Anti-patterns

- Omitting `credentials: 'include'` on authenticated fetch requests, causing 401 Unauthorized responses.
- Hard-coding Gateway URLs as relative paths (`/api/`) instead of the absolute HTTPS origin.
- Reusing one-time enrollment tokens across multiple failed registration attempts.
- Passing `user_id` or session IDs in SSE stream query parameters instead of relying on the authenticated cookie.
- Dismissing certificate warnings instead of installing local root CA trust.

## Links out

- [Build a ztd-Compatible Frontend](build_frontend.md) — Comprehensive frontend building reference.
- [Connect a Lovable App](lovable.md) — Guided local Lovable development setup.
- [Cloudflare Tunnel Integration](cloudflare_tunnel.md) — Expose Gateway to public internet.
- [Connect Applications to Gateway](connect_apps_to_gateway.md) — Application SDK and agent connectivity.
- [Authentication Architecture](../architecture/auth.md) — WebAuthn passkey architecture.
