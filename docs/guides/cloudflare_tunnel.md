---
doc_id: cloudflare_tunnel
title: Cloudflare Tunnel Integration
audience: operators, infrastructure engineers, deployment specialists
status: current
version: v1.2.0
last_updated: 2026-10-20
owners:
  - docs/guides/cloudflare_tunnel.md
  - internal/cli/cmd/gw/tunnel.go
  - internal/cli/cmd/gw/tunnel_route_dns.go
related:
  - docs/guides/public_spectator.md
  - docs/guides/build_frontend.md
  - docs/guides/lovable.md
  - docs/architecture/network.md
  - docs/architecture/gateway.md
  - docs/architecture/auth.md
when_to_read: Publishing a local ztd Gateway to the public internet through Cloudflare's managed edge network, configuring DNS routing, or integrating external frontends with tunnel-exposed services.
do_not_use_for:
  - Frontend development and authentication (docs/guides/build_frontend.md)
  - Public spectator read-only publication (docs/guides/public_spectator.md)
  - Local same-machine frontend workflows (docs/guides/lovable.md)
  - Network trust boundaries and PKI architecture (docs/architecture/network.md)
  - Gateway authentication and authorization (docs/architecture/auth.md)
---

# Cloudflare Tunnel Integration

## Purpose

Describes how to publish a local ztd Gateway through Cloudflare Tunnel, making it accessible on the public internet without opening inbound firewall ports. Covers tunnel creation, DNS routing, configuration generation, and verification.

The tunnel secures transport between Cloudflare's edge and the local origin. Authentication and authorization remain the responsibility of the Gateway; Cloudflare Tunnel and Access do not replace ztd mTLS, session, JWT, or governance requirements on protected routes.

## Quick index

- [Purpose](#purpose)
- [Quick index](#quick-index)
- [Prerequisites](#prerequisites)
- [Scope and Architecture](#scope-and-architecture)
- [Procedures](#procedures)
- [Configuration Reference](#configuration-reference)
- [Troubleshooting](#troubleshooting)
- [Links out](#links-out)

## Prerequisites

- `cloudflared` installed and on `PATH`.
- Cloudflare account that controls the DNS zone for the target hostname.
- ztd binary built as `./ztd`.
- Selected origin running on localhost before external verification (Gateway HTTPS default: `localhost:8443`).
- For the `route-dns` command: Cloudflare API token with DNS edit permissions.

## Scope and Architecture

```text
[Browser] -> https://console.example.com -> [Cloudflare Edge] -> [cloudflared] -> [Gateway HTTPS localhost:8443]
```

`cloudflared` maintains outbound connections to Cloudflare, so the Gateway host requires zero inbound firewall openings. Cloudflare terminates public TLS. The Gateway applies its own route authentication, WebAuthn passkey authentication, WebSessions, mTLS requirements, and governance checks.

## Procedures

### 1. Create the tunnel and generate configuration

```bash
./ztd gw tunnel create --name ztd --hostname console.example.com
```

The command:
1. Verifies `cloudflared` is installed.
2. Runs `cloudflared tunnel login` if `cert.pem` is missing.
3. Creates the named tunnel (or continues if it exists).
4. Routes the hostname via `cloudflared tunnel route dns` (unless `--skip-dns` is set).
5. Writes `~/.cloudflared/config.yml` with ingress rules and a catch-all 404 rule.

### 2. Route DNS with zone-specific API token (when needed)

If the login session selected a different zone than the target domain, set `CLOUDFLARE_API_TOKEN` to an API token with DNS edit permission. The block below prompts for the token without echoing it or recording it in shell history:

```bash
read -rsp 'Cloudflare API token: ' CLOUDFLARE_API_TOKEN && echo
export CLOUDFLARE_API_TOKEN
./ztd gw tunnel route-dns --name ztd console.example.com
```

To keep the token in the repo-root `.env` instead (template: `.env.example`, mode `0600` or `0400`), set `CLOUDFLARE_API_TOKEN` there; `./ztd` loads `.env` from the working directory at startup. `--api-token` overrides the environment.

### 3. Start the Gateway with tunnel-aware configuration

```bash
./ztd gw start -f \
  --posture doctrine \
  --passkey-rp-id console.example.com \
  --passkey-rp-origin https://console.example.com \
  --public-base-url https://console.example.com \
  --cors-origin https://console.example.com
```

### 4. Start the tunnel

In a separate terminal or service supervisor:

```bash
./ztd gw tunnel run --name ztd
```

### 5. Verify tunnel and origin health

```bash
./ztd gw tunnel status --name ztd --hostname console.example.com
```

Or query health directly:

```bash
curl -fsS https://console.example.com/api/v1/health
```

Expected response contains `"status":"ok"` and `"governance_ready":true`.

## Configuration Reference

### Generated ingress configuration

```yaml
tunnel: <tunnel-id>
credentials-file: <home>/.cloudflared/<tunnel-id>.json

ingress:
  - hostname: console.example.com
    service: https://localhost:8443
    originRequest:
      noTLSVerify: true
      http2Origin: true
  - service: http_status:404
```

By default, `cloudflared` does not verify the Gateway certificate (`noTLSVerify: true`), so the `cloudflared`-to-Gateway hop is encrypted but not authenticated. To enable origin certificate verification, pass `--ca-bundle /path/to/ztdg-ca-bundle.pem` to `gw tunnel create`; the generated config then sets `originCaPool` in place of `noTLSVerify`. The origin TLS server name (SNI) is set to `ztd.local` by default, which is the DNS SAN every Gateway server certificate carries, so verification does not depend on the `--service` host matching the certificate. Pass `--origin-server-name <name>` to override it. `--origin-server-name` MUST be used with `--ca-bundle`; without it the command fails, because `noTLSVerify` mode never sends a server name.

## Troubleshooting

- **`Failed to fetch` or 502 Bad Gateway**: Verify Gateway is running on `localhost:8443` and returns healthy status locally via `curl -sk https://localhost:8443/api/v1/health`.
- **Passkey RP mismatch**: The RP ID must be the exact hostname (`console.example.com`) or a registrable parent domain without scheme or port.
- **CORS or session cookies blocked**: Add `--cors-origin https://console.example.com` and `--passkey-rp-origin https://console.example.com` to Gateway startup.

## Links out

- [Public Spectator Operations Guide](public_spectator.md) — Expose read-only public feed via tunnel.
- [Build a ztd-Compatible Frontend](build_frontend.md) — Frontend integration and WebAuthn.
- [Connect a Lovable App](lovable.md) — Local development workflow without tunnels.
- [Network Architecture](../architecture/network.md) — Network topology and mTLS boundaries.
- [Gateway Architecture](../architecture/gateway.md) — Gateway routing and authentication.
