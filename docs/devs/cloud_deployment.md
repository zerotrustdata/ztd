---
doc_id: cloud_deployment
title: Cloud VM Deployment Guide
audience: maintainers and coding agents
status: current
version: v1.4.1
last_updated: 2026-10-26
owners:
  - internal/cli/cmd/gw/
  - internal/services/gateway/
  - internal/services/network/
related:
  - docs/devs/devs.md
  - docs/devs/troubleshooting.md
  - docs/architecture/gateway.md
  - docs/architecture/network.md
  - docs/guides/cloudflare_tunnel.md
when_to_read: Deploying, configuring, or operating the ztd Governance Gateway on public cloud virtual machines (GCP, AWS, DigitalOcean, Hetzner) without relying on ingress tunnels.
do_not_use_for:
  - Gateway internal control-plane architecture and L1-L3 pipeline (docs/architecture/gateway.md)
  - SPIFFE PKI hierarchy and workload identity specifications (docs/architecture/network.md)
  - Local workstation Cloudflare Tunnel configuration (docs/guides/cloudflare_tunnel.md)
---

# Cloud VM Deployment Guide

## Purpose

Defines the architecture, requirements, and deployment procedures for running the ztd Governance Gateway (`ztd gw`) on dedicated public cloud virtual machines (e.g., Google Cloud Platform Compute Engine, AWS EC2, DigitalOcean, or Hetzner Cloud). Documents why edge serverless isolates (e.g., Cloudflare Workers) cannot host the Gateway binary directly, specifies network ingress topology and DNS requirements, and provides reproducible operational procedures for running the Gateway daemon in production environments.

## Quick index

- [Purpose](#purpose)
- [Quick index](#quick-index)
- [Invariants](#invariants)
  - [Hosting and Storage Requirements (`INV-DEPLOY-HOST`)](#hosting-and-storage-requirements-inv-deploy-host)
  - [Network Ingress and TLS Integrity (`INV-DEPLOY-NET`)](#network-ingress-and-tls-integrity-inv-deploy-net)
- [Owned surfaces](#owned-surfaces)
- [Procedures](#procedures)
  - [Cloud VM Provisioning (GCP Example)](#cloud-vm-provisioning-gcp-example)
  - [Firewall and Ingress Port Configuration](#firewall-and-ingress-port-configuration)
  - [Binary Build and Deployment](#binary-build-and-deployment)
  - [Systemd Service Supervision](#systemd-service-supervision)
  - [DNS and Edge Routing (Cloudflare Unproxied)](#dns-and-edge-routing-cloudflare-unproxied)
  - [Remote Operator Connection](#remote-operator-connection)
- [Anti-patterns](#anti-patterns)
- [Links out](#links-out)

## Invariants

Ids are stable. Append the next free number in a topic. Do not renumber.

### Hosting and Storage Requirements (`INV-DEPLOY-HOST`)

| ID | Rule |
| :--- | :--- |
| INV-DEPLOY-HOST-01 | The Gateway host environment MUST provide a persistent, POSIX-compliant local filesystem for the runtime state directory (`.ztd/`). Ephemeral container runtimes or serverless execution layers (e.g., standard Cloudflare Workers) that lack persistent disk MUST NOT host the Gateway, as state loss destroys the internal PKI root, SQLite audit history, and incremental Merkle state roots. |
| INV-DEPLOY-HOST-02 | Production Gateway deployments on Linux VMs SHOULD be supervised by a process manager (such as `systemd`) configured with `Restart=always` and executing `ztd gw start --follow` to ensure process recovery, log streaming to `journalctl`, and clean signal handling. |
| INV-DEPLOY-HOST-03 | The host VM MUST provide a minimum of 2 vCPUs and 2 GB RAM (e.g., GCP `e2-small` or `e2-medium`, AWS `t4g.small` or `t3.small`) to support concurrent Operator pub/sub WebSocket streams, cryptographic signature verification, and Merkle tree state tracking. |

### Network Ingress and TLS Integrity (`INV-DEPLOY-NET`)

| ID | Rule |
| :--- | :--- |
| INV-DEPLOY-NET-01 | Cloud VM firewall rules MUST allow inbound ingress on TCP port 8080 (HTTP discovery, health probes, PKI bootstrap) and TCP port 8443 (HTTPS API, Console web UI, MCP, and mTLS pub/sub streams). Port 22 (SSH) MUST be restricted or protected according to organizational security policy. |
| INV-DEPLOY-NET-02 | When using third-party DNS management (such as Cloudflare DNS), the Gateway record MUST be configured in DNS-only mode (unproxied / grey cloud). Upstream TLS-terminating reverse proxies MUST NOT terminate client connections on port 8443, as this strips the client certificates required for mutual TLS (mTLS) authentication between Governed Operators and the Gateway. |
| INV-DEPLOY-NET-03 | When deploying with a public IP or custom domain, `ztd gw start` MUST be invoked with `--listen-host 0.0.0.0` and `--cert-identity-mode full` (or an explicit `--public-base-url`) to guarantee that the Gateway's serving certificate includes all routable public IP and DNS Subject Alternative Names (SANs). |

## Owned surfaces

| Claim | Path | Verify |
| :--- | :--- | :--- |
| Gateway startup and flags | `internal/cli/cmd/gw/gateway.go` | `./ztd gw start --help` |
| Network identity detection | `internal/services/network/identity.go` | `./ztd test unit --run TestNetworkDetector` |
| Route authentication policy | `internal/services/gateway/gateway_auth.go` | `./ztd test integration --run TestGatewayAuth` |
| Systemd service specification | `docs/devs/cloud_deployment.md` | Verification via unit tests and integration suites |

## Procedures

### Cloud VM Provisioning (GCP Example)

Deploying the Gateway on a dedicated cloud VM provides persistent storage, direct dual-port listening, and full control over mTLS termination without requiring an ingress tunnel (such as `cloudflared`).

To provision a Google Cloud Platform (GCP) Compute Engine instance using the `gcloud` CLI:

```bash
# Set target project and zone variables
PROJECT_ID="<PROJECT_ID>"
ZONE="us-central1-a"
INSTANCE_NAME="ztd-gateway"

# Ensure Google Compute Engine API is enabled
gcloud services enable compute.googleapis.com --project="${PROJECT_ID}"

# Create the VM instance (Debian 12, 2 vCPU, 4GB RAM, 20GB persistent disk)
gcloud compute instances create "${INSTANCE_NAME}" \
    --project="${PROJECT_ID}" \
    --zone="${ZONE}" \
    --machine-type=e2-medium \
    --image-family=debian-12 \
    --image-project=debian-cloud \
    --boot-disk-size=20GB \
    --tags=ztd-gateway
```

### Firewall and Ingress Port Configuration

The Gateway exposes a consolidated two-port topology: HTTP port 8080 and HTTPS port 8443. Configure network firewall rules to permit inbound traffic:

```bash
gcloud compute firewall-rules create allow-ztd-gateway \
    --project="${PROJECT_ID}" \
    --network=default \
    --allow=tcp:22,tcp:8080,tcp:8443,icmp \
    --source-ranges=0.0.0.0/0 \
    --target-tags=ztd-gateway \
    --description="Allow SSH and ztd Gateway ports 8080 and 8443"
```

### Binary Build and Deployment

1. On the local development machine, compile the static Linux binary:

```bash
make build
# Produces bin/ztd-linux-amd64 (or bin/ztd-linux-arm64)
```

2. Transfer the executable to the cloud VM:

```bash
gcloud compute scp bin/ztd-linux-amd64 "${INSTANCE_NAME}":/tmp/ztd \
    --zone="${ZONE}" \
    --project="${PROJECT_ID}"
```

3. SSH into the instance and install the binary:

```bash
gcloud compute ssh "${INSTANCE_NAME}" --zone="${ZONE}" --project="${PROJECT_ID}"

# Inside the VM:
sudo mv /tmp/ztd /usr/local/bin/ztd
sudo chmod +x /usr/local/bin/ztd
```

### Systemd Service Supervision

To ensure the Gateway runs continuously as a system daemon and recovers automatically after reboots, create a systemd unit file at `/etc/systemd/system/ztd-gateway.service`:

```ini
[Unit]
Description=ztd Governance Gateway
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=/root
ExecStart=/usr/local/bin/ztd gw start \
    --follow \
    --listen-host 0.0.0.0 \
    --http-port 8080 \
    --https-port 8443 \
    --public-base-url https://<GATEWAY_DOMAIN_OR_PUBLIC_IP>:8443 \
    --cert-identity-mode full \
    --posture doctrine

Restart=always
RestartSec=5s
LimitNOFILE=65536

StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
```

Enable and start the service:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now ztd-gateway

# Verify daemon status and logs:
sudo systemctl status ztd-gateway
sudo journalctl -u ztd-gateway -f
```

Verify service responsiveness:

```bash
# HTTP discovery probe
curl -I http://<GATEWAY_DOMAIN_OR_PUBLIC_IP>:8080/healthz

# HTTPS health probe
curl -k https://<GATEWAY_DOMAIN_OR_PUBLIC_IP>:8443/healthz
```

### DNS and Edge Routing (Cloudflare Unproxied)

If using Cloudflare for domain DNS management:

1. Create a DNS `A` or `AAAA` record pointing to the VM's public IP:
   - **Type**: `A`
   - **Name**: `gateway` (e.g., `gateway.example.com`)
   - **IPv4 address**: `<GATEWAY_PUBLIC_IP>`
2. **Set Proxy Status to DNS only (Grey Cloud)**:
   - **MUST NOT** enable Cloudflare Proxy (Orange Cloud). Cloudflare's edge proxy terminates TLS using public edge certificates, which strips the client certificate during the TLS 1.3 handshake and breaks Operator mutual authentication (`spiffe://ztd.local/`).

### Remote Operator Connection

Governed Operators (PEPs) establish outbound-only mTLS WebSocket connections to the Gateway's pub/sub stream at `/api/v1/pubsub/stream`. Because the cloud Gateway possesses a public IP, Operators on local developer workstations or remote clusters connect directly without requiring ingress tunnels:

```bash
# Connect and enroll a remote Operator
ztd operator start --gateway https://<GATEWAY_DOMAIN_OR_PUBLIC_IP>:8443
```

All command dispatches, health heartbeats, and signed event receipts flow across the persistent WebSocket channel.

## Anti-patterns

- **Hosting Gateway in Ephemeral Serverless Environments**: Running `ztd gw` inside standard Cloudflare Workers, ephemeral containers, or stateless micro-VMs without persistent local block storage causes state-root and PKI loss on container recycling.
- **Enabling Edge Proxying (Orange Cloud) on Port 8443**: Terminating TLS at an upstream CDN/edge proxy breaks end-to-end mTLS authentication for Governed Operators and CLIs.
- **Running `ztd gw tunnel` on a Public VM**: Using Cloudflare Tunnel when the host VM already has an external public IP and configured firewall rules adds unnecessary latency and proxy layers. `ztd gw tunnel` is strictly intended for hosts behind private residential NATs.
- **Hardcoding Loopback in Public Deployments**: Omitting `--listen-host 0.0.0.0` or using `--cert-identity-mode localhost` on a public server prevents remote Operators from establishing secure TLS connections.

## Links out

- [Gateway Architecture](../architecture/gateway.md) — Comprehensive specification of the Gateway PDP, routes, and operational modes.
- [Network Architecture](../architecture/network.md) — Detailed explanation of the SPIFFE PKI hierarchy, dual-port topology, and mTLS transport.
- [Cloudflare Tunnel Guide](../guides/cloudflare_tunnel.md) — Operational instructions for running local gateways behind NAT using `ztd gw tunnel`.
- [Troubleshooting Guide](troubleshooting.md) — Remediation steps for network drift, certificate mismatches, and vault unlock failures.

