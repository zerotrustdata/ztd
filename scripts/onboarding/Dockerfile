# Copyright (c) 2026 Lateralus Labs, LLC.
# Use of this source code is governed by the Business Source License
# included in the LICENSE file.
#
# As of the Change Date listed in the LICENSE file, this software is
# released under the Apache License, Version 2.0.

# A fresh Linux developer host for onboarding tests, not a ztd distribution:
# ztd itself is never packaged in a container. The host has a non-root user
# with sudo and the base tools any real install has (CA certificates, tar,
# gzip, procps). Everything else (git, make, curl, Go, Node.js, ...) must be
# installed by the setup script under test. See scripts/onboarding/run-matrix.sh.
ARG BASE=ubuntu:24.04
FROM ${BASE}

RUN set -eux; \
    if command -v apt-get >/dev/null; then \
        apt-get update; \
        DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends sudo ca-certificates tar gzip procps; \
        rm -rf /var/lib/apt/lists/*; \
    elif command -v dnf >/dev/null; then \
        dnf install -y sudo ca-certificates tar gzip procps-ng shadow-utils; \
        dnf clean all; \
    elif command -v pacman >/dev/null; then \
        pacman -Syu --noconfirm --needed sudo ca-certificates tar gzip procps-ng; \
        pacman -Scc --noconfirm; \
    elif command -v zypper >/dev/null; then \
        zypper --non-interactive install sudo ca-certificates tar gzip procps shadow; \
        zypper clean --all; \
    else \
        echo "unsupported base image" >&2; exit 1; \
    fi; \
    useradd --create-home --shell /bin/bash newcomer; \
    echo 'newcomer ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/newcomer; \
    chmod 0440 /etc/sudoers.d/newcomer

USER newcomer
WORKDIR /home/newcomer
ENV SHELL=/bin/bash LANG=C.UTF-8
