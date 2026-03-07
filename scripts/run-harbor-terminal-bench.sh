#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

DATASET="${ELOK_HARBOR_DATASET:-terminal-bench-sample@2.0}"
GATEWAY_URL="${ELOK_GATEWAY_URL:-ws://127.0.0.1:7777/ws}"
TENANT_ID="${ELOK_TENANT_ID:-default}"
JOBS_DIR="${ELOK_HARBOR_JOBS_DIR:-jobs/harbor}"

if command -v uvx >/dev/null 2>&1; then
  HARBOR_CMD=(uvx --python 3.12 --with harbor --with websockets harbor)
elif command -v harbor >/dev/null 2>&1; then
  HARBOR_CMD=(harbor)
else
  cat >&2 <<'EOF'
Harbor CLI is not available.

Install uv and rerun this script:
  https://docs.astral.sh/uv/getting-started/installation/

Or install harbor directly in Python 3.12+ and ensure `harbor` is on PATH.
EOF
  exit 1
fi

AGENT_KWARGS=(
  --agent-kwarg "gateway_url=${GATEWAY_URL}"
  --agent-kwarg "tenant_id=${TENANT_ID}"
)

if [[ -n "${ELOK_SEND_PROVIDER:-}" ]]; then
  AGENT_KWARGS+=(--agent-kwarg "send_provider=${ELOK_SEND_PROVIDER}")
fi

if [[ -n "${ELOK_SEND_MODEL:-}" ]]; then
  AGENT_KWARGS+=(--agent-kwarg "send_model=${ELOK_SEND_MODEL}")
fi

"${HARBOR_CMD[@]}" run \
  --dataset "${DATASET}" \
  --agent-import-path "bench.harbor.elok_agent:ElokGatewayAgent" \
  --jobs-dir "${JOBS_DIR}" \
  "${AGENT_KWARGS[@]}" \
  "$@"
