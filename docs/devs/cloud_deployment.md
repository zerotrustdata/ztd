---
doc_id: cloud_deployment
title: Cloud VM Deployment Guide
audience: maintainers and coding agents
status: current
version: v1.7.0
last_updated: 2026-10-30
owners:
  - deploy/cloud_gateway/
  - internal/cli/cmd/gw/
  - internal/services/gateway/
  - internal/services/network/
related:
  - docs/devs/devs.md
  - docs/devs/troubleshooting.md
  - docs/architecture/gateway.md
  - docs/architecture/network.md
  - docs/architecture/public_spectator.md
  - docs/guides/cloudflare_tunnel.md
when_to_read: Deploying, configuring, automating, or operating the ztd Governance Gateway on public cloud virtual machines (GCP, AWS, DigitalOcean, Hetzner) and hosting the public spectator mirror.
do_not_use_for:
  - Gateway internal control-plane architecture and L1-L3 pipeline (docs/architecture/gateway.md)
  - SPIFFE PKI hierarchy and workload identity specifications (docs/architecture/network.md)
  - Local workstation Cloudflare Tunnel configuration (docs/guides/cloudflare_tunnel.md)
---

# Cloud VM Deployment Guide

## Purpose

Defines the architecture, requirements, and deployment procedures for running the ztd Governance Gateway (`ztd gw`) on dedicated public cloud virtual machines (e.g., Google Cloud Platform Compute Engine, AWS EC2, DigitalOcean, or Hetzner Cloud). Documents why edge serverless isolates (e.g., Cloudflare Workers) cannot host the Gateway binary directly, specifies network ingress topology and DNS requirements, provides automated Terraform infrastructure definitions, and documents how to host the public spectator mirror on a cloud VM.

## Quick index

- [Purpose](#purpose)
- [Quick index](#quick-index)
- [Invariants](#invariants)
  - [Hosting and Storage Requirements (`INV-DEPLOY-HOST`)](#hosting-and-storage-requirements-inv-deploy-host)
  - [Network Ingress and TLS Integrity (`INV-DEPLOY-NET`)](#network-ingress-and-tls-integrity-inv-deploy-net)
- [Owned surfaces](#owned-surfaces)
- [Procedures](#procedures)
  - [Automated Infrastructure Provisioning (Terraform)](#automated-infrastructure-provisioning-terraform)
  - [Manual Cloud VM Provisioning (GCP CLI Example)](#manual-cloud-vm-provisioning-gcp-cli-example)
  - [Firewall and Ingress Port Configuration](#firewall-and-ingress-port-configuration)
  - [Headless Linux Keystore and Master Key Initialization](#headless-linux-keystore-and-master-key-initialization)
  - [Binary Build and Deployment](#binary-build-and-deployment)
  - [Systemd Service Supervision (Self-Healing)](#systemd-service-supervision-self-healing)
  - [Public Mirror and Spectator Ingress](#public-mirror-and-spectator-ingress)
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
| INV-DEPLOY-HOST-04 | On headless Linux cloud VMs lacking desktop Secret Service D-Bus, the Gateway host MUST provision a persistent `ssh-agent` and host Ed25519 key (managed via systemd `ExecStartPre` or session bootstrap) to satisfy the OS keystore master key seal (`INV-ENC-STORE-01`). |

### Network Ingress and TLS Integrity (`INV-DEPLOY-NET`)

| ID | Rule |
| :--- | :--- |
| INV-DEPLOY-NET-01 | Cloud VM firewall rules MUST allow inbound ingress on TCP port 8080 (HTTP discovery, health probes, PKI bootstrap), TCP port 8443 (HTTPS API, Console web UI, MCP, and mTLS pub/sub streams), and TCP port 8082 (anonymous public mirror read and Evaluation Explorer SPA when direct ingress is enabled). Port 22 (SSH) MUST be restricted or protected according to organizational security policy. |
| INV-DEPLOY-NET-02 | When using third-party DNS management (such as Cloudflare DNS), the Gateway record MUST be configured in DNS-only mode (unproxied / grey cloud). Upstream TLS-terminating reverse proxies MUST NOT terminate client connections on port 8443, as this strips the client certificates required for mutual TLS (mTLS) authentication between Governed Operators and the Gateway. |
| INV-DEPLOY-NET-03 | When deploying with a public IP or custom domain, `ztd gw start` MUST be invoked with `--listen-host 0.0.0.0` and `--cert-mode full` (or an explicit `--public-base-url`) to guarantee that the Gateway's serving certificate includes all routable public IP and DNS Subject Alternative Names (SANs). |
| INV-DEPLOY-NET-04 | When hosting the public spectator mirror on a cloud VM, the private ingest listener (`127.0.0.1:8081`) MUST remain unconditionally loopback-isolated (`INV-SPEC-ISO-03`), while the public read listener (`:8082`) MAY be exposed directly via direct network ingress (`0.0.0.0:8082` with `--public-spectator-allow-container-bind`) or proxied via an edge tunnel (such as `cloudflared`) to provide CDN caching and DDoS mitigation without compromising the Gateway's direct unproxied mTLS port (`8443`). |

## Owned surfaces

| Claim | Path | Verify |
| :--- | :--- | :--- |
| Automated Cloud Deployment | `deploy/cloud_gateway/` | `terraform -chdir=deploy/cloud_gateway validate` |
| Gateway startup and flags | `internal/cli/cmd/gw/gateway.go` | `./ztd gw start --help` |
| Network identity detection | `internal/services/network/identity.go` | `./ztd test unit --run TestNetworkDetector` |
| Route authentication policy | `internal/services/gateway/gateway_auth.go` | `./ztd test integration --run TestGatewayAuth` |
| Public spectator runtime | `internal/services/gateway/public_spectator_runtime.go` | `./ztd test integration --pkg ./internal/services/gateway --run TestPublicSpectator` |
| Systemd service specification | `docs/devs/cloud_deployment.md` | Verification via unit tests and integration suites |

## Procedures

### Automated Infrastructure Provisioning (Terraform)

The recommended production approach defines infrastructure declaratively in `deploy/cloud_gateway/`:

1. **Initialize Terraform**:
   ```bash
   cd deploy/cloud_gateway
   terraform init
   ```

2. **Provision Infrastructure**:
   ```bash
   terraform apply -var="project_id=zerotrustdata" -var="zone=us-central1-a"
   ```
   This automatically creates:
   - Reserved static external IPv4 address (prevents SAN and DNS drift).
   - Firewall rules allowing SSH (22), HTTP (8080), HTTPS (8443), and Public Mirror Read (8082).
   - Compute Engine Debian 12 instance configured with a startup script (`startup.sh`).
   - Automated systemd service configuration with self-healing keystore SSH agent initialization.
   - The startup script records the GCP metadata external IP in `/etc/hosts` as `ztd-gateway-public` and passes `--public-base-url https://<ip>:8443`. Compute Engine NAT addresses are not on a local interface, so `cert-mode full` would otherwise omit the public IP from the serving certificate and HTTP redirects would fall back to localhost.

3. **Deploy the Gateway Binary**:
   ```bash
   make deploy
   ```
   The `deploy` target in `deploy/cloud_gateway/Makefile` builds the static Linux binary from source, transfers it to `/usr/local/bin/ztd`, restarts `ztd-gateway`, and verifies the `/healthz` probe.

### Manual Cloud VM Provisioning (GCP CLI Example)

Alternatively, provision a Compute Engine instance manually using `gcloud`:

```bash
PROJECT_ID="zerotrustdata"
ZONE="us-central1-a"
INSTANCE_NAME="ztd-gateway"

gcloud services enable compute.googleapis.com --project="${PROJECT_ID}"

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
    --allow=tcp:22,tcp:8080,tcp:8443,tcp:8082,icmp \
    --source-ranges=0.0.0.0/0 \
    --target-tags=ztd-gateway \
    --description="Allow SSH, ztd Gateway discovery/mTLS (8080, 8443), and Public Mirror Read (8082)"
```

### Headless Linux Keystore and Master Key Initialization

Per `INV-ENC-STORE-01`, ztd seals platform secrets (CA keys, vault key, spectator signing keys) under a 32-byte master key stored in an OS-protected store. Headless Linux servers lack desktop Secret Service D-Bus; they require an **ssh-agent seal**:

1. **For Interactive Testing in an SSH Session**:
   If executing `ztd gw start` directly from a shell session on the VM:
   ```bash
   # Ensure host has an Ed25519 identity
   if [ ! -f ~/.ssh/id_ed25519 ]; then
       ssh-keygen -t ed25519 -N "" -f ~/.ssh/id_ed25519 -C "ztd-gateway-keystore"
   fi

   # Start agent and load identity
   eval "$(ssh-agent -s)"
   ssh-add ~/.ssh/id_ed25519

   # First start prints a recovery key and waits for it to be typed back.
   /usr/local/bin/ztd gw start --public-spectator --listen-host=0.0.0.0 --capabilities data
   ```

2. **For Automated Daemon Execution**:
   Under systemd supervision, the service unit manages its own private ssh-agent socket at `/run/ztd/ssh-agent.sock` via `ExecStartPre`. The unit passes `--accept-unrecoverable` because systemd has no terminal. That keeps the only copy of the master key in this VM's OS key store. Losing the VM's ssh-agent key destroys the gateway. To keep an offline recovery key, run the first `ztd gw start` from an SSH terminal and save the key it prints before enabling the unit. See [Recovery key](../architecture/encryption.md#recovery-key).

### Binary Build and Deployment

1. On the local development machine, compile the static Linux binary:
   ```bash
   make build
   # Produces bin/ztd-linux-amd64
   ```

2. Transfer the executable to the cloud VM:
   ```bash
   gcloud compute scp bin/ztd-linux-amd64 "${INSTANCE_NAME}":/tmp/ztd \
       --zone="${ZONE}" \
       --project="${PROJECT_ID}"
   ```

3. Install the binary on the VM:
   ```bash
   gcloud compute ssh "${INSTANCE_NAME}" --zone="${ZONE}" --project="${PROJECT_ID}" \
       --command="sudo mv /tmp/ztd /usr/local/bin/ztd && sudo chmod +x /usr/local/bin/ztd"
   ```

### Systemd Service Supervision (Self-Healing)

To ensure the Gateway runs continuously as a system daemon and recovers automatically after reboots, create `/etc/systemd/system/ztd-gateway.service`:

```ini
[Unit]
Description=ztd Governance Gateway
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=/root
Environment=SSH_AUTH_SOCK=/run/ztd/ssh-agent.sock
RuntimeDirectory=ztd

# Ensure host identity exists and launch dedicated ssh-agent socket for keystore seal
ExecStartPre=/bin/sh -c 'if [ ! -f /root/.ssh/id_ed25519 ]; then ssh-keygen -t ed25519 -N "" -f /root/.ssh/id_ed25519 -C "ztd-gateway-keystore"; fi'
# pkill runs alone. The agent command also contains the socket path, and a
# combined command line matches the pattern and SIGTERMs this script.
ExecStartPre=/bin/sh -c 'pkill -u root -f "ssh-agent -a /run/ztd/ssh-agent.soc[k]" || true; rm -f /run/ztd/ssh-agent.sock'
ExecStartPre=/bin/sh -c 'ssh-agent -a /run/ztd/ssh-agent.sock && SSH_AUTH_SOCK=/run/ztd/ssh-agent.sock ssh-add /root/.ssh/id_ed25519'

ExecStart=/usr/local/bin/ztd gw start \
    --follow \
    --accept-unrecoverable \
    --listen-host 0.0.0.0 \
    --http-port 8080 \
    --https-port 8443 \
    --public-base-url https://<GATEWAY_DOMAIN_OR_PUBLIC_IP>:8443 \
    --cert-mode full \
    --public-spectator=true \
    --public-spectator-public-listen 0.0.0.0:8082 \
    --public-spectator-private-listen 127.0.0.1:8081 \
    --public-spectator-allow-container-bind \
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

### Public Mirror and Spectator Ingress

The cloud Gateway hosts the public mirror and Evaluation Explorer in-process (`--public-spectator` enabled by default):
- **Private Ingest**: `127.0.0.1:8081` (authenticated loopback-only ingestion per `INV-SPEC-ISO-03`)
- **Public Read & Evaluation Explorer SPA**: `0.0.0.0:8082` (anonymous read API + web UI via direct VM ingress or edge tunnel per `INV-DEPLOY-NET-04`)

#### Ingress Topology

```text
Campaign Host (Local Workstation)
       │
       ▼ (mTLS owner push)
POST https://<GATEWAY_PUBLIC_IP>:8443/api/v1/public-feed/batches
       │
       ▼
Cloud VM Gateway Process
 ├─ Private Ingest: 127.0.0.1:8081 (Strict Loopback Only)
 └─ Public Read:   0.0.0.0:8082 (Direct Ingress or Optional Tunnel)
       ▲
       │ Direct TCP Ingress (:8082) or Optional Tunnel
       │
 Public Anonymous Browser & Remote Mirror Probes
 http://<GATEWAY_PUBLIC_IP>:8082
```

1. **Exposing the Public Mirror**:
   - **Direct Cloud VM Ingress (Default)**: Pass `--public-spectator-public-listen 0.0.0.0:8082 --public-spectator-allow-container-bind` and permit TCP `8082` in cloud firewall rules (`allow-ztd-gateway`). Browsers and remote CLI commands query `http://<GATEWAY_PUBLIC_IP>:8082` directly. Private ingest remains strictly bound to `127.0.0.1:8081`.
   - **Optional Edge Tunnel**: Point `cloudflared` to `http://127.0.0.1:8082` if edge DDoS mitigation or custom CDN caching is desired (`INV-DEPLOY-NET-04`).
2. **Publishing Runs and Restoring from Local Workstations**:
   Campaigns run on local development machines publish batches over mTLS and verify public mirror status using the `-e / --endpoint` flag:
   ```bash
   # Status probing against remote Cloud Gateway
   ztd -e <GATEWAY_PUBLIC_IP> public status

   # Public restore against remote Cloud Gateway
   ztd -e <GATEWAY_PUBLIC_IP> public restore --queue

   # Override public mirror read origin explicitly if fronted by a domain
   ztd -e <GATEWAY_PUBLIC_IP> public restore --queue --mirror-origin http://<GATEWAY_PUBLIC_IP>:8082
   ```

### DNS and Edge Routing (Cloudflare Unproxied)

If using Cloudflare for domain DNS management:

1. Create a DNS `A` or `AAAA` record pointing to the VM's public IP:
   - **Type**: `A`
   - **Name**: `gateway` (e.g., `gateway.zerotrustdata.io`)
   - **IPv4 address**: `<GATEWAY_PUBLIC_IP>`
2. **Set Proxy Status to DNS only (Grey Cloud)**:
   - **MUST NOT** enable Cloudflare Proxy (Orange Cloud) for the Gateway port 8443. Cloudflare's edge proxy terminates TLS using public edge certificates, which strips the client certificate during the TLS 1.3 handshake and breaks Operator mutual authentication (`spiffe://ztd.local/`).

### Remote Operator Connection

Governed Operators (PEPs) establish outbound-only mTLS WebSocket connections to the Gateway's pub/sub stream at `/api/v1/pubsub/stream`. Because the cloud Gateway possesses a public IP, Operators on local developer workstations or remote clusters connect directly without requiring ingress tunnels:

```bash
# Connect and enroll a remote Operator
ztd operator start --endpoint <GATEWAY_DOMAIN_OR_PUBLIC_IP>
```

All command dispatches, health heartbeats, and signed event receipts flow across the persistent WebSocket channel.

## Anti-patterns

- **Hosting Gateway in Ephemeral Serverless Environments**: Running `ztd gw` inside standard Cloudflare Workers, ephemeral containers, or stateless micro-VMs without persistent local block storage causes state-root and PKI loss on container recycling.
- **Enabling Edge Proxying (Orange Cloud) on Port 8443**: Terminating TLS at an upstream CDN/edge proxy breaks end-to-end mTLS authentication for Governed Operators and CLIs.
- **Neglecting Headless Keystore Initialization**: Running `ztd gw` on a headless Linux host without `ssh-agent` or with missing SSH identity causes immediate startup failure with `ErrKeyStoreOSKeyringRequired`.
- **Conflating Gateway Ingress with Mirror Ingress**: Exposing port 8443 through an ingress proxy strips mTLS, but exposing the anonymous public mirror port 8082 through a Cloudflare Tunnel MAY be used for CDN caching and DDoS mitigation (`INV-DEPLOY-NET-04`).
- **Hardcoding Loopback in Public Deployments**: Omitting `--listen-host 0.0.0.0` or using `--cert-mode localhost` on a public server prevents remote Operators from establishing secure TLS connections.

## Links out

- [Gateway Architecture](../architecture/gateway.md) — Comprehensive specification of the Gateway PDP, routes, and operational modes.
- [Public Spectator Architecture](../architecture/public_spectator.md) — Isolated public mirror, proof catalog, and Evaluation Explorer delivery.
- [Network Architecture](../architecture/network.md) — Detailed explanation of the SPIFFE PKI hierarchy, dual-port topology, and mTLS transport.
- [Cloudflare Tunnel Guide](../guides/cloudflare_tunnel.md) — Operational instructions for running local gateways behind NAT using `ztd gw tunnel`.
- [Troubleshooting Guide](troubleshooting.md) — Remediation steps for network drift, certificate mismatches, and vault unlock failures.
