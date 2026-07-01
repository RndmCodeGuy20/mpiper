#!/usr/bin/env bash
# loadtest/run.sh — host-run wrapper for the MPiper k6 load harness.
#
# Usage:
#   ./loadtest/run.sh closed --vus 10 --duration 2m [--ramp]
#   ./loadtest/run.sh open   --rate 5/s --duration 3m [--max-vus 200]
#   ./loadtest/run.sh capture "label"        # snapshot headline signals from Prometheus
#
# Options (closed/open):
#   --fixture PATH   image fixture to fan out (default worker/tests/test_assets/image.jpg)
#   --base-url URL   API base (default http://localhost:5010)
#   --no-prometheus  do not stream k6 metrics to Prometheus remote-write
#
# A/B contrast (concurrent worker + webhooks) — same binary, flip env knobs on
# docker-compose.loadtest.yml (see its header), then run + capture each side:
#   WORKER_CPUS=4 MAX_CONCURRENT_JOBS=1 WEBHOOK_CONCURRENCY=1  docker compose … up -d --force-recreate worker api
#   ./loadtest/run.sh closed --vus 20 --duration 2m && ./loadtest/run.sh capture "BEFORE"
#   WORKER_CPUS=4 MAX_CONCURRENT_JOBS=8 WEBHOOK_CONCURRENCY=10 docker compose … up -d --force-recreate worker api
#   ./loadtest/run.sh closed --vus 20 --duration 2m && ./loadtest/run.sh capture "AFTER"
#
# Requires on the host: k6 (brew install k6), docker (to seed an API key into
# the containerized Postgres), python3 (stdlib only), and the stack up with the
# observability overlay (so Prometheus remote-write is enabled).
set -euo pipefail

MODEL="${1:-}"
if [[ "$MODEL" != "closed" && "$MODEL" != "open" && "$MODEL" != "capture" ]]; then
  echo "usage: $0 <closed|open|capture> [options|label]" >&2
  exit 2
fi
shift || true

# --- capture mode: snapshot headline pipeline signals from Prometheus --------
# Run RIGHT AFTER a load run (instant queries see ~the last few minutes). The
# remaining args are a free-text label so before/after snapshots are labelled.
if [[ "$MODEL" == "capture" ]]; then
  LABEL="${*:-snapshot}"
  PROM="${PROM_URL:-http://localhost:9090}"
  _q() {
    python3 - "$PROM" "$1" <<'PY'
import json, sys, urllib.parse, urllib.request
prom, expr = sys.argv[1], sys.argv[2]
url = f"{prom}/api/v1/query?" + urllib.parse.urlencode({"query": expr})
try:
    with urllib.request.urlopen(url, timeout=10) as r:
        data = json.load(r)
    res = data["data"]["result"]
    print("n/a" if not res else f'{float(res[0]["value"][1]):.3f}')
except Exception as e:
    print(f"err:{e}")
PY
  }
  echo "========================================================================"
  echo " MPiper signals — $LABEL"
  echo " $(date -u +%Y-%m-%dT%H:%M:%SZ)  ·  prom=$PROM"
  echo "========================================================================"
  printf "%-42s %s\n" "Worker service rate mu (jobs/s)" "$(_q 'sum(rate(mpiper_mpiper_job_processing_success_total[2m]))')"
  printf "%-42s %s\n" "Job failures/s"                  "$(_q 'sum(rate(mpiper_mpiper_job_processing_failed_total[2m]))')"
  printf "%-42s %s\n" "Queue depth (max)"               "$(_q 'max(mpiper_queue_depth)')"
  printf "%-42s %s\n" "Asset proc mean (s)"             "$(_q 'sum(rate(mpiper_mpiper_asset_processing_duration_seconds_sum[2m])) / clamp_min(sum(rate(mpiper_mpiper_asset_processing_duration_seconds_count[2m])),1)')"
  printf "%-42s %s\n" "Webhook pending (max)"           "$(_q 'max(mpiper_webhook_pending)')"
  printf "%-42s %s\n" "Webhook delivery rate (/s)"      "$(_q 'sum(rate(mpiper_webhook_delivery_total[2m]))')"
  printf "%-42s %s\n" "Webhook delivery failures/s"     "$(_q 'sum(rate(mpiper_webhook_delivery_failures_total[2m]))')"
  printf "%-42s %s\n" "Webhook delivery p95 (s)"        "$(_q 'histogram_quantile(0.95, sum by (le) (rate(mpiper_webhook_delivery_duration_seconds_bucket[2m])))')"
  printf "%-42s %s\n" "DLQ depth (max)"                 "$(_q 'max(mpiper_mpiper_dlq_depth)')"
  printf "%-42s %s\n" "DB connections in-use (max)"     "$(_q 'max(mpiper_db_connections_active)')"
  printf "%-42s %s\n" "DB connection waits (max)"       "$(_q 'max(mpiper_db_connections_wait_count)')"
  echo "------------------------------------------------------------------------"
  echo "tip: also grab 'docker stats --no-stream mpiper-worker' for worker CPU%."
  exit 0
fi

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

# --- Mint an API key (matches scripts/demo-e2e.sh / README) --------------
# Seeds a scoped key directly into the containerized Postgres; no AES token.
# shellcheck source=/dev/null
. "$REPO_ROOT/scripts/_apikey.sh"
LOADTEST_TENANT="${LOADTEST_TENANT:-loadtest}"
K6_TOKEN="$(mint_api_key "$LOADTEST_TENANT")"
[ -n "$K6_TOKEN" ] || { echo "failed to mint API key" >&2; exit 1; }
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
