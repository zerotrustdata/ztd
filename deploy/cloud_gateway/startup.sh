#!/usr/bin/env bash
# Copyright (c) 2026 Lateralus Labs, LLC.
# Startup and self-healing bootstrap script for ztd Governance Gateway on Cloud VMs.
# Executed by Compute Engine metadata_startup_script on instance boot.

set -euo pipefail

echo "[ztd-init] Initializing cloud gateway host environment..."

# 1. Ensure swapfile for memory stability (INV-DEPLOY-HOST-03)
if [ ! -f /swapfile ]; then
    echo "[ztd-init] Provisioning 1GB swapfile..."
    fallocate -l 1G /swapfile 2>/dev/null || dd if=/dev/zero of=/swapfile bs=1M count=1024
    chmod 600 /swapfile
    mkswap /swapfile
    swapon /swapfile
    echo '/swapfile none swap sw 0 0' >> /etc/fstab
fi

# 2. Prepare directories and permissions
mkdir -p /run/ztd /root/.ssh
chmod 700 /root/.ssh
chmod 755 /run/ztd

# 3. Provision dedicated host Ed25519 key for OS keystore seal (INV-ENC-STORE-01)
if [ ! -f /root/.ssh/id_ed25519 ]; then
    echo "[ztd-init] Generating host Ed25519 key for keystore master key seal..."
    ssh-keygen -t ed25519 -N "" -f /root/.ssh/id_ed25519 -C "ztd-gateway-keystore"
fi

# 4. GCP NAT addresses are not assigned to a local interface, so cert-mode full
# never sees them. Record the metadata external IP in /etc/hosts and pin it
# as the public base URL (INV-DEPLOY-NET-03).
PUBLIC_IP=""
PUBLIC_BASE_LINE=""
if command -v curl >/dev/null 2>&1; then
    PUBLIC_IP="$(curl -fsS -m 2 -H "Metadata-Flavor: Google" \
        "http://metadata.google.internal/computeMetadata/v1/instance/network-interfaces/0/access-configs/0/external-ip" || true)"
fi
if [[ "$PUBLIC_IP" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]]; then
    echo "[ztd-init] Recording external IP ${PUBLIC_IP} for certificate SANs"
    sed -i '/[[:space:]]ztd-gateway-public$/d' /etc/hosts
    printf '%s ztd-gateway-public\n' "$PUBLIC_IP" >> /etc/hosts
    PUBLIC_BASE_ARG="--public-base-url https://${PUBLIC_IP}:8443"
else
    echo "[ztd-init] No GCP external IP detected; serving certificate uses local identity only"
    PUBLIC_IP=""
    PUBLIC_BASE_ARG=""
fi

# One argv line. An unquoted heredoc discards backslash-newline continuations.
EXEC_START="/usr/local/bin/ztd gw start --follow --listen-host 0.0.0.0 --http-port 8080 --https-port 8443 --cert-mode full"
if [ -n "$PUBLIC_BASE_ARG" ]; then
    EXEC_START="${EXEC_START} ${PUBLIC_BASE_ARG}"
fi
EXEC_START="${EXEC_START} --posture doctrine"

# 5. Install self-healing systemd service
echo "[ztd-init] Writing systemd unit /etc/systemd/system/ztd-gateway.service..."
cat << EOF > /etc/systemd/system/ztd-gateway.service
[Unit]
Description=ztd Governance Gateway
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=/root
Environment=SSH_AUTH_SOCK=/run/ztd/ssh-agent.sock
RuntimeDirectory=ztd

# Ensure host key exists and launch persistent ssh-agent socket for keystore master key seal
ExecStartPre=/bin/sh -c 'if [ ! -f /root/.ssh/id_ed25519 ]; then ssh-keygen -t ed25519 -N "" -f /root/.ssh/id_ed25519 -C "ztd-gateway-keystore"; fi'
# pkill runs alone. The agent command also contains the socket path, and a
# combined command line matches the pattern and SIGTERMs this script.
ExecStartPre=/bin/sh -c 'pkill -u root -f "ssh-agent -a /run/ztd/ssh-agent.soc[k]" || true; rm -f /run/ztd/ssh-agent.sock'
ExecStartPre=/bin/sh -c 'ssh-agent -a /run/ztd/ssh-agent.sock && SSH_AUTH_SOCK=/run/ztd/ssh-agent.sock ssh-add /root/.ssh/id_ed25519'

ExecStart=${EXEC_START}

Restart=always
RestartSec=5s
LimitNOFILE=65536

StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable ztd-gateway

# 6. Start service immediately if binary is already present
if [ -x /usr/local/bin/ztd ]; then
    echo "[ztd-init] ztd binary detected at /usr/local/bin/ztd, restarting service..."
    systemctl restart ztd-gateway
else
    echo "[ztd-init] ztd binary not yet installed at /usr/local/bin/ztd; service enabled for start upon deployment."
fi

echo "[ztd-init] Cloud gateway initialization complete."

