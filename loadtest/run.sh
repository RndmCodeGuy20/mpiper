#!/usr/bin/env bash
# loadtest/run.sh — host-run wrapper for the MPiper k6 load harness.
#
# Usage:
#   ./loadtest/run.sh closed --vus 10 --duration 2m [--ramp]
#   ./loadtest/run.sh open   --rate 5/s --duration 3m [--max-vus 200]
#
# Options (any model):
#   --fixture PATH   image fixture to fan out (default worker/tests/test_assets/image.jpg)
#   --base-url URL   API base (default http://localhost:5010)
#   --no-prometheus  do not stream k6 metrics to Prometheus remote-write
#
# Requires on the host: k6 (brew install k6), python3 with `cryptography`
# (only to mint the AES-GCM auth token), and the stack up with the
# observability overlay (so Prometheus remote-write is enabled).
set -euo pipefail

MODEL="${1:-}"
if [[ "$MODEL" != "closed" && "$MODEL" != "open" ]]; then
  echo "usage: $0 <closed|open> [options]" >&2
  exit 2
fi
shift || true

VUS=10
DURATION=""
RATE=5
MAX_VUS=""
RAMP=0
FIXTURE="worker/tests/test_assets/image.jpg"
BASE_URL="http://localhost:5010"
USE_PROM=1

while [[ $# -gt 0 ]]; do
  case "$1" in
    --vus) VUS="$2"; shift 2 ;;
    --duration) DURATION="$2"; shift 2 ;;
    --rate) RATE="${2%/s}"; shift 2 ;;          # accept "5/s" or "5"
    --max-vus) MAX_VUS="$2"; shift 2 ;;
    --ramp) RAMP=1; shift ;;
    --fixture) FIXTURE="$2"; shift 2 ;;
    --base-url) BASE_URL="$2"; shift 2 ;;
    --no-prometheus) USE_PROM=0; shift ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# --- Mint an auth token (matches scripts/demo-e2e.sh / README) -----------
ENCRYPTION_KEY="${ENCRYPTION_KEY:-0123456789abcdef0123456789abcdef}"
K6_TOKEN="$(ENCRYPTION_KEY="$ENCRYPTION_KEY" python3 - <<'PY'
import base64, os
from cryptography.hazmat.primitives.ciphers.aead import AESGCM
key = os.environ["ENCRYPTION_KEY"].encode()
nonce = os.urandom(12)
ct = AESGCM(key).encrypt(nonce, b"demo-user", None)
print(base64.urlsafe_b64encode(nonce + ct).rstrip(b"=").decode())
PY
)"
export K6_TOKEN BASE_URL
export FIXTURE_PATH="$REPO_ROOT/$FIXTURE"

# --- Stream client metrics to the bundled Prometheus (remote-write) ------
K6_OUT=()
if [[ "$USE_PROM" == "1" ]]; then
  export K6_PROMETHEUS_RW_SERVER_URL="${K6_PROMETHEUS_RW_SERVER_URL:-http://localhost:9090/api/v1/write}"
  export K6_PROMETHEUS_RW_TREND_STATS="p(95),p(99),avg,max"
  K6_OUT=(-o experimental-prometheus-rw)
  echo "k6 → Prometheus remote-write at $K6_PROMETHEUS_RW_SERVER_URL"
fi

cd "$REPO_ROOT"

if [[ "$MODEL" == "closed" ]]; then
  export VUS DURATION="${DURATION:-2m}" RAMP
  echo "closed model: VUS=$VUS DURATION=$DURATION RAMP=$RAMP"
  exec k6 run "${K6_OUT[@]}" loadtest/closed_model.js
else
  export RATE DURATION="${DURATION:-3m}"
  [[ -n "$MAX_VUS" ]] && export MAX_VUS
  echo "open model: RATE=${RATE}/s DURATION=$DURATION"
  exec k6 run "${K6_OUT[@]}" loadtest/open_model.js
fi
