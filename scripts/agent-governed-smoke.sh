#!/usr/bin/env bash
# Copyright (c) 2026 Lateralus Labs, LLC.
# Use of this source code is governed by the Business Source License
# included in the LICENSE file.
#
# As of the Change Date listed in the LICENSE file, this software is
# released under the Apache License, Version 2.0.

# One-command governed-agent smoke. Builds ztd, ensures a local model
# backend, runs `ztd mcp agent run goose` through one allowed tool call and
# one L1 denial, verifies the signed receipt and commitment chain, and stops
# only what this run started.
#
# CPU and a few gigabytes of RAM are enough. The default model is
# qwen2.5:0.5b on Ollama: the smallest tag in the local inventory that
# reliably emitted both tool calls (see docs/architecture/scripts.md).
# Override with ZTD_SMOKE_MODEL and ZTD_SMOKE_BACKEND=ollama|llamacpp.
#
# Bash 3.2 compatible (macOS /bin/bash). No GNU-only flags.
#
#   make test-agent-smoke
#   ZTD_SMOKE_MODEL=qwen3:0.6b bash scripts/agent-governed-smoke.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

BACKEND="${ZTD_SMOKE_BACKEND:-ollama}"
MODEL="${ZTD_SMOKE_MODEL:-qwen2.5:0.5b}"
AGENT_TIMEOUT="${ZTD_SMOKE_AGENT_TIMEOUT:-600}"
PULL_TIMEOUT="${ZTD_SMOKE_PULL_TIMEOUT:-900}"
READY_TIMEOUT="${ZTD_SMOKE_READY_TIMEOUT:-90}"
GOOSE_VERSION="${ZTD_SMOKE_GOOSE_VERSION:-v1.56.0}"
ATTEMPTS="${ZTD_SMOKE_ATTEMPTS:-3}"
PROMPT='Use run_shell_command exactly twice and no other tool. Call 1 JSON arguments: {"command":"echo","args":["ztd-smoke-ok"]}. Call 2 JSON arguments: {"command":"rm","args":["-rf","/"]}. Do not omit args.'

SMOKE_ROOT="${ZTD_SMOKE_ROOT:-}"
started_agent=0
started_ollama=0
started_llama=0
ollama_pid=""
llama_pid=""
sampler_pid=""
peak_file=""
agent_elapsed=0

die() {
    echo "ERROR: $*" >&2
    exit 1
}

log() {
    echo "==> $*"
}

# Portable wait. GNU timeout is not on macOS.
run_timeout() {
    secs="$1"
    shift
    "$@" &
    child=$!
    waited=0
    while kill -0 "$child" 2>/dev/null; do
        if [ "$waited" -ge "$secs" ]; then
            kill "$child" 2>/dev/null || true
            wait "$child" 2>/dev/null || true
            return 124
        fi
        sleep 1
        waited=$((waited + 1))
    done
    wait "$child"
}

wait_http() {
    url="$1"
    secs="$2"
    waited=0
    while [ "$waited" -lt "$secs" ]; do
        if curl -fsS -o /dev/null "$url" 2>/dev/null; then
            return 0
        fi
        sleep 1
        waited=$((waited + 1))
    done
    return 1
}

uname_s="$(uname -s)"
uname_m="$(uname -m)"
case "$uname_m" in
    x86_64|amd64) cpu_arch="amd64"; goose_arch="x86_64" ;;
    arm64|aarch64) cpu_arch="arm64"; goose_arch="aarch64" ;;
    *) die "unsupported architecture: $uname_m" ;;
esac

require_cmd() {
    if ! command -v "$1" >/dev/null 2>&1; then
        die "$2"
    fi
}

require_cmd ssh-add "ssh-add is required (OpenSSH). Install openssh-client, then rerun make test-agent-smoke."
if ! ssh-add -l >/dev/null 2>&1; then
    require_cmd ssh-keygen "ssh-keygen is required to create this smoke's throwaway agent key. Install openssh-client, or start an ssh-agent that already holds a key (ssh-add -l)."
    require_cmd ssh-agent "ssh-agent is required. Install openssh-client, or export SSH_AUTH_SOCK for an agent that already holds a key."
fi
if [ "$BACKEND" = "ollama" ] && ! command -v ollama >/dev/null 2>&1; then
    require_cmd curl "ollama is not on PATH and curl is required to install it. Install ollama or curl, then rerun make test-agent-smoke."
    if ! curl -fsS -o /dev/null "http://127.0.0.1:11434/api/tags" 2>/dev/null; then
        case "$uname_s" in
            Linux)
                require_cmd zstd "ollama is not installed and zstd is required to unpack it. Install zstd, or install ollama and start it on 127.0.0.1:11434."
                ;;
            Darwin)
                require_cmd unzip "ollama is not installed and unzip is required to unpack it. Install unzip, or install ollama and start it on 127.0.0.1:11434."
                ;;
        esac
        log "ollama is not on PATH and nothing is listening on 127.0.0.1:11434; the smoke will install a copy under the smoke root"
    fi
fi

cleanup() {
    status=$?
    if [ -n "$sampler_pid" ]; then
        kill "$sampler_pid" 2>/dev/null || true
        wait "$sampler_pid" 2>/dev/null || true
        sampler_pid=""
    fi
    if [ -n "${SMOKE_ROOT:-}" ] && [ -x "${REPO_ROOT}/ztd" ]; then
        (cd "$SMOKE_ROOT" && "$REPO_ROOT/ztd" gw stop >/dev/null 2>&1) || true
    fi
    if [ "$started_ollama" -eq 1 ] && [ -n "$ollama_pid" ]; then
        kill "$ollama_pid" 2>/dev/null || true
        wait "$ollama_pid" 2>/dev/null || true
    fi
    if [ "$started_llama" -eq 1 ] && [ -n "$llama_pid" ]; then
        kill "$llama_pid" 2>/dev/null || true
        wait "$llama_pid" 2>/dev/null || true
    fi
    if [ "$started_agent" -eq 1 ] && [ -n "${SSH_AGENT_PID:-}" ]; then
        ssh-agent -k >/dev/null 2>&1 || true
    fi
    if [ "$status" -ne 0 ]; then
        echo "agent governed smoke failed (exit $status). Logs: ${SMOKE_ROOT:-unknown}" >&2
    elif [ "${ZTD_SMOKE_CLEAN:-0}" = 1 ] && [ -n "${SMOKE_ROOT:-}" ] && [ -z "${ZTD_SMOKE_ROOT:-}" ]; then
        rm -rf "$SMOKE_ROOT"
    fi
    exit "$status"
}
trap cleanup EXIT

if [ -z "$SMOKE_ROOT" ]; then
    SMOKE_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/ztd-agent-governed-smoke.XXXXXX")"
else
    mkdir -p "$SMOKE_ROOT"
fi

log "smoke root $SMOKE_ROOT"
mkdir -p "$SMOKE_ROOT/bin" "$SMOKE_ROOT/home" "$SMOKE_ROOT/evidence" "$SMOKE_ROOT/logs"
export HOME="$SMOKE_ROOT/home"
export PATH="$SMOKE_ROOT/bin:$PATH"

log "building ztd"
make -C "$REPO_ROOT" build-go
ZTD="$REPO_ROOT/ztd"
[ -x "$ZTD" ] || die "make build-go did not produce $ZTD"

umask 077
cp "$REPO_ROOT/.env.example" "$SMOKE_ROOT/.env"
chmod 600 "$SMOKE_ROOT/.env"

if ! ssh-add -l >/dev/null 2>&1; then
    log "starting a throwaway ssh-agent (this root's vault key)"
    key="$SMOKE_ROOT/smoke_ed25519"
    if [ ! -f "$key" ]; then
        ssh-keygen -t ed25519 -f "$key" -N "" -q
    fi
    eval "$(ssh-agent -s)"
    ssh-add "$key" >/dev/null
    started_agent=1
fi

install_goose() {
    if command -v goose >/dev/null 2>&1; then
        return 0
    fi
    log "installing goose $GOOSE_VERSION"
    case "$uname_s" in
        Linux) goose_os="unknown-linux-gnu" ;;
        Darwin) goose_os="apple-darwin" ;;
        *) die "goose install supports Linux and macOS (this host is $uname_s)" ;;
    esac
    url="https://github.com/block/goose/releases/download/${GOOSE_VERSION}/goose-${goose_arch}-${goose_os}.tar.gz"
    archive="$SMOKE_ROOT/logs/goose.tar.gz"
    curl -fL --retry 3 -o "$archive" "$url" || die "download goose from $url"
    tar -xzf "$archive" -C "$SMOKE_ROOT/bin" || die "extract goose"
    [ -x "$SMOKE_ROOT/bin/goose" ] || die "goose archive did not contain bin/goose"
}

install_ollama() {
    if curl -fsS -o /dev/null "http://127.0.0.1:11434/api/tags" 2>/dev/null; then
        log "reusing Ollama at 127.0.0.1:11434"
        return 0
    fi
    if ! command -v ollama >/dev/null 2>&1; then
        log "installing Ollama into the smoke root (no systemd, no sudo)"
        prefix="$SMOKE_ROOT/opt/ollama"
        mkdir -p "$prefix"
        case "$uname_s" in
            Linux)
                command -v zstd >/dev/null 2>&1 || die "zstd is required to unpack Ollama (apt-get install zstd, or brew install zstd)"
                archive="$SMOKE_ROOT/logs/ollama.tar.zst"
                curl -fL --retry 3 -o "$archive" "https://ollama.com/download/ollama-linux-${cpu_arch}.tar.zst" \
                    || die "download Ollama"
                zstd -d -c "$archive" | tar -xf - -C "$prefix" || die "extract Ollama"
                ln -sf "$prefix/bin/ollama" "$SMOKE_ROOT/bin/ollama"
                ;;
            Darwin)
                command -v unzip >/dev/null 2>&1 || die "unzip is required to unpack Ollama"
                archive="$SMOKE_ROOT/logs/Ollama-darwin.zip"
                curl -fL --retry 3 -o "$archive" "https://ollama.com/download/Ollama-darwin.zip" \
                    || die "download Ollama"
                unzip -q "$archive" -d "$SMOKE_ROOT/opt" || die "extract Ollama"
                app_bin="$(find "$SMOKE_ROOT/opt" -type f -name ollama | head -n 1)"
                [ -n "$app_bin" ] || die "Ollama.app did not contain an ollama binary"
                ln -sf "$app_bin" "$SMOKE_ROOT/bin/ollama"
                ;;
            *) die "Ollama install supports Linux and macOS" ;;
        esac
    fi
    command -v ollama >/dev/null 2>&1 || die "ollama is not on PATH"
    log "starting ollama serve"
    ollama serve >"$SMOKE_ROOT/logs/ollama.log" 2>&1 &
    ollama_pid=$!
    started_ollama=1
    wait_http "http://127.0.0.1:11434/api/tags" "$READY_TIMEOUT" \
        || die "Ollama did not answer on :11434 (see $SMOKE_ROOT/logs/ollama.log)"
}

start_llamacpp() {
    endpoint="${ZTD_SMOKE_LLAMACPP_ENDPOINT:-http://127.0.0.1:8080}"
    if curl -fsS -o /dev/null "$endpoint/v1/models" 2>/dev/null; then
        log "reusing llama-server at $endpoint"
        return 0
    fi
    command -v llama-server >/dev/null 2>&1 || die "llama-server is not on PATH and $endpoint is not serving /v1/models"
    [ -n "${ZTD_SMOKE_GGUF:-}" ] || die "set ZTD_SMOKE_GGUF to the GGUF llama-server should load, or start llama-server yourself and set ZTD_SMOKE_LLAMACPP_ENDPOINT"
    log "starting llama-server"
    llama-server -m "$ZTD_SMOKE_GGUF" --host 127.0.0.1 --port 8080 >"$SMOKE_ROOT/logs/llama-server.log" 2>&1 &
    llama_pid=$!
    started_llama=1
    wait_http "http://127.0.0.1:8080/v1/models" "$READY_TIMEOUT" \
        || die "llama-server did not answer (see $SMOKE_ROOT/logs/llama-server.log)"
}

install_goose

case "$BACKEND" in
    ollama)
        install_ollama
        export OLLAMA_HOST="${OLLAMA_HOST:-http://127.0.0.1:11434}"
        log "pulling $MODEL"
        run_timeout "$PULL_TIMEOUT" ollama pull "$MODEL" \
            || die "ollama pull $MODEL failed or exceeded ${PULL_TIMEOUT}s"
        show="$(curl -fsS "$OLLAMA_HOST/api/show" -H 'Content-Type: application/json' -d "{\"name\":\"$MODEL\"}")" \
            || die "ollama show $MODEL failed"
        echo "$show" | grep -q '"tools"' || die "$MODEL does not advertise tool calling; pick a tool-capable tag (default qwen2.5:0.5b)"
        provider_args="--provider ollama --model $MODEL"
        ;;
    llamacpp)
        start_llamacpp
        export OPENAI_HOST="${ZTD_SMOKE_LLAMACPP_ENDPOINT:-http://127.0.0.1:8080}"
        export OPENAI_API_KEY="${OPENAI_API_KEY:-local}"
        provider_args="--provider openai --model $MODEL"
        ;;
    *)
        die "ZTD_SMOKE_BACKEND must be ollama or llamacpp (got $BACKEND)"
        ;;
esac

peak_file="$SMOKE_ROOT/evidence/peak-rss-kb"
echo 0 >"$peak_file"
smoke_pid=$$
# Count only this smoke's processes: the script tree, the throwaway agent it
# started, and the Gateway pid file under the smoke root. A host-wide match on
# /ztd or ollama would include unrelated runtimes.
(
    while true; do
        gwpid=""
        if [ -f "$SMOKE_ROOT/.ztd/pids/operator.pid" ]; then
            gwpid="$(tr -d '[:space:]' <"$SMOKE_ROOT/.ztd/pids/operator.pid" 2>/dev/null || true)"
        fi
        agent_pid=""
        if [ "${started_agent:-0}" -eq 1 ]; then
            agent_pid="${SSH_AGENT_PID:-}"
        fi
        rss="$(ps -axo pid=,ppid=,rss=,command= | awk \
            -v root="$SMOKE_ROOT" -v self="$smoke_pid" -v gw="$gwpid" \
            -v ollama="${ollama_pid:-}" -v llama="${llama_pid:-}" -v agent="$agent_pid" '
            {
                pid = $1 + 0
                ppid = $2 + 0
                rss = $3 + 0
                cmd = ""
                for (i = 4; i <= NF; i++) cmd = cmd (i == 4 ? "" : " ") $i
                n = n + 1
                p_pid[n] = pid
                p_ppid[n] = ppid
                p_rss[n] = rss
                p_cmd[n] = cmd
            }
            END {
                if (self > 0) seed[self] = 1
                if (gw > 0) seed[gw] = 1
                if (ollama > 0) seed[ollama] = 1
                if (llama > 0) seed[llama] = 1
                if (agent > 0) seed[agent] = 1
                changed = 1
                while (changed) {
                    changed = 0
                    for (i = 1; i <= n; i++) {
                        if (seed[p_pid[i]]) continue
                        if (seed[p_ppid[i]] || (root != "" && index(p_cmd[i], root) > 0)) {
                            seed[p_pid[i]] = 1
                            changed = 1
                        }
                    }
                }
                s = 0
                for (i = 1; i <= n; i++) if (seed[p_pid[i]]) s += p_rss[i]
                print s + 0
            }
        ')"
        rss="${rss:-0}"
        cur="$(cat "$peak_file")"
        if [ "$rss" -gt "$cur" ]; then
            echo "$rss" >"$peak_file"
        fi
        sleep 1
    done
) &
sampler_pid=$!

# A fresh runtime on a Linux ssh-agent seal runs the recovery-key ceremony, which
# needs a terminal; `ztd mcp agent run` has none here and would refuse every
# attempt. This root is throwaway, so create its master key unrecoverable.
log "starting the smoke Gateway (throwaway root, --accept-unrecoverable)"
(cd "$SMOKE_ROOT" && "$ZTD" gw start --accept-unrecoverable >"$SMOKE_ROOT/logs/gw-start.txt" 2>&1) \
    || die "gateway start failed (see $SMOKE_ROOT/logs/gw-start.txt)"

log "running governed goose task (up to ${ATTEMPTS} attempts, timeout ${AGENT_TIMEOUT}s each)"
agent_start="$(date +%s)"
attempt=1
completed=0
denied=0
completed_id=""
while [ "$attempt" -le "$ATTEMPTS" ]; do
    log "attempt ${attempt}"
    set +e
    (
        cd "$SMOKE_ROOT"
        # shellcheck disable=SC2086
        run_timeout "$AGENT_TIMEOUT" "$ZTD" mcp agent run goose -- $provider_args --max-turns 4 -t "$PROMPT"
    ) >"$SMOKE_ROOT/logs/agent-stdout.txt" 2>"$SMOKE_ROOT/logs/agent-stderr.txt"
    agent_status=$?
    set -e
    cp "$SMOKE_ROOT/logs/agent-stdout.txt" "$SMOKE_ROOT/evidence/attempt-${attempt}-stdout.txt"
    cp "$SMOKE_ROOT/logs/agent-stderr.txt" "$SMOKE_ROOT/evidence/attempt-${attempt}-stderr.txt"
    if [ "$agent_status" -ne 0 ]; then
        log "attempt ${attempt} exited ${agent_status}"
        attempt=$((attempt + 1))
        continue
    fi
    (
        cd "$SMOKE_ROOT"
        "$ZTD" audit event-receipts --app 'spiffe://ztd.local/app/goose' --json
    ) >"$SMOKE_ROOT/evidence/receipts-list.json"
    ids="$(grep -oE '"transaction_id":[[:space:]]*"[0-9a-f]{64}"' "$SMOKE_ROOT/evidence/receipts-list.json" | sed 's/.*"\([0-9a-f]\{64\}\)".*/\1/' | sort -u || true)"
    for id in $ids; do
        (
            cd "$SMOKE_ROOT"
            "$ZTD" audit event-receipts --tx-id "$id"
        ) >"$SMOKE_ROOT/evidence/receipt-${id}.json"
        if grep -q 'EXECUTION_STATUS_COMPLETED' "$SMOKE_ROOT/evidence/receipt-${id}.json" \
            && grep -q 'ztd-smoke-ok' "$SMOKE_ROOT/evidence/receipt-${id}.json"; then
            completed=1
            completed_id="$id"
        fi
        if grep -q 'GOVERNANCE_REJECTION_CODE_L1_DOCTRINE' "$SMOKE_ROOT/evidence/receipt-${id}.json" \
            && grep -q 'EXECUTION_STATUS_FAILED' "$SMOKE_ROOT/evidence/receipt-${id}.json"; then
            denied=1
        fi
    done
    if [ "$completed" -eq 1 ] && [ "$denied" -eq 1 ]; then
        break
    fi
    log "attempt ${attempt} has completed=${completed} denied=${denied}"
    attempt=$((attempt + 1))
done
agent_end="$(date +%s)"
agent_elapsed=$((agent_end - agent_start))
[ "$completed" -eq 1 ] || die "no COMPLETED receipt whose result contains ztd-smoke-ok after ${ATTEMPTS} attempts"
[ "$denied" -eq 1 ] || die "no FAILED receipt with GOVERNANCE_REJECTION_CODE_L1_DOCTRINE after ${ATTEMPTS} attempts"

(
    cd "$SMOKE_ROOT"
    "$ZTD" audit verify
) >"$SMOKE_ROOT/evidence/audit-verify.txt"
grep -q 'Audit chain OK' "$SMOKE_ROOT/evidence/audit-verify.txt" || die "audit verify did not report Audit chain OK"

(
    cd "$SMOKE_ROOT"
    "$ZTD" report verify --out "$SMOKE_ROOT/evidence/report"
) >"$SMOKE_ROOT/evidence/report-verify.txt"
grep -q 'Verification PASSED' "$SMOKE_ROOT/evidence/report-verify.txt" || die "report verify did not pass"
if grep -q ',FAIL,' "$SMOKE_ROOT/evidence/report/verification_summary.csv"; then
    die "verification_summary.csv contains a FAIL row"
fi

(
    cd "$SMOKE_ROOT"
    "$ZTD" report all --out "$SMOKE_ROOT/evidence/report-all"
) >"$SMOKE_ROOT/evidence/report-all.txt"
grep -q "$completed_id" "$SMOKE_ROOT/evidence/report-all/commitments.csv" \
    || die "allowed transaction $completed_id has no commitment-ledger row"

cp "$SMOKE_ROOT/logs/agent-stdout.txt" "$SMOKE_ROOT/logs/agent-stderr.txt" "$SMOKE_ROOT/evidence/"
{
    echo "backend=$BACKEND"
    echo "model=$MODEL"
    echo "attempts=$attempt"
    echo "agent_elapsed_seconds=$agent_elapsed"
    echo "peak_rss_kb=$(cat "$peak_file")"
    echo "completed_transaction=$completed_id"
    echo "goose=$(goose --version 2>/dev/null || echo unknown)"
} >"$SMOKE_ROOT/evidence/summary.txt"

if [ -n "${ZTD_SMOKE_ARTIFACTS:-}" ]; then
    mkdir -p "$ZTD_SMOKE_ARTIFACTS"
    cp -R "$SMOKE_ROOT/evidence/." "$ZTD_SMOKE_ARTIFACTS/"
fi

log "passed in ${agent_elapsed}s peak_rss_kb=$(cat "$peak_file") model=$MODEL backend=$BACKEND"
cat "$SMOKE_ROOT/evidence/summary.txt"
echo "evidence: $SMOKE_ROOT/evidence"
