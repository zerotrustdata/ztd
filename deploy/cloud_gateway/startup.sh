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

# 4. Install self-healing systemd service
echo "[ztd-init] Writing systemd unit /etc/systemd/system/ztd-gateway.service..."
cat << 'EOF' > /etc/systemd/system/ztd-gateway.service
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
ExecStartPre=/bin/sh -c 'pkill -u root -f "ssh-agent -a /run/ztd/ssh-agent.sock" || true; rm -f /run/ztd/ssh-agent.sock && ssh-agent -a /run/ztd/ssh-agent.sock && SSH_AUTH_SOCK=/run/ztd/ssh-agent.sock ssh-add /root/.ssh/id_ed25519'

ExecStart=/usr/local/bin/ztd gw start \
    --follow \
    --listen-host 0.0.0.0 \
    --http-port 8080 \
    --https-port 8443 \
    --cert-identity-mode full \
    --posture doctrine

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

# 5. Start service immediately if binary is already present
if [ -x /usr/local/bin/ztd ]; then
    echo "[ztd-init] ztd binary detected at /usr/local/bin/ztd, restarting service..."
    systemctl restart ztd-gateway
else
    echo "[ztd-init] ztd binary not yet installed at /usr/local/bin/ztd; service enabled for start upon deployment."
fi

echo "[ztd-init] Cloud gateway initialization complete."

