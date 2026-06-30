#!/usr/bin/env bash
# scripts/demo-e2e.sh
#
# End-to-end demo driver for MPiper, run from the HOST machine exactly like a
# real client would: it presigns an upload, PUTs the file straight to MinIO over
# the published localhost:9000 endpoint, marks the asset complete, then waits for
# the worker to produce variants and for webhook deliveries to land.
#
# It exercises BOTH an image and a video, plus the full webhook lifecycle
# (job.starting -> job.started -> job.done).
#
# Prerequisites — bring the stack up WITH the webhooks overlay first:
#
#   docker compose -f docker-compose.yml -f docker-compose.webhooks.yml up -d --build
#
# Then run:
#
#   ./scripts/demo-e2e.sh
#
# Requirements on the host: bash, curl, jq, docker, and a python3 with the
# `cryptography` package (used only to mint the auth token, matching
# pkg/utils/crypt.go). Override defaults via env vars (API, ENCRYPTION_KEY, …).

set -uo pipefail

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------
API="${API:-http://localhost:5010}"
ENCRYPTION_KEY="${ENCRYPTION_KEY:-}"
USER_ID="${USER_ID:-demo-user}"
WEBHOOK_RECEIVER_URL="${WEBHOOK_RECEIVER_URL:-http://webhook-receiver:8080}"  # internal docker name; reached by the in-container dispatcher
WEBHOOK_SECRET="${WEBHOOK_SECRET:-demo-webhook-secret}"
PG_CONTAINER="${PG_CONTAINER:-mpiper-postgres}"
PG_USER="${PG_USER:-mpiper}"
PG_DB="${PG_DB:-mpiper}"
RECEIVER_CONTAINER="${RECEIVER_CONTAINER:-mpiper-webhook-receiver}"

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGE_FILE="${IMAGE_FILE:-$ROOT_DIR/worker/tests/test_assets/image.jpg}"
VIDEO_FILE="${VIDEO_FILE:-$ROOT_DIR/tests/test_assets/sample.mp4}"

IMAGE_READY_TIMEOUT="${IMAGE_READY_TIMEOUT:-60}"
VIDEO_READY_TIMEOUT="${VIDEO_READY_TIMEOUT:-120}"
WEBHOOK_TIMEOUT="${WEBHOOK_TIMEOUT:-30}"

# ---------------------------------------------------------------------------
# Output helpers
# ---------------------------------------------------------------------------
if [ -t 1 ]; then
  RED=$'\033[0;31m'; GREEN=$'\033[0;32m'; BLUE=$'\033[0;34m'; BOLD=$'\033[1m'; NC=$'\033[0m'
else
  RED=""; GREEN=""; BLUE=""; BOLD=""; NC=""
fi

PASS_COUNT=0
FAIL_COUNT=0

step()  { printf '\n%s== %s ==%s\n' "$BLUE$BOLD" "$1" "$NC"; }
info()  { printf '   %s\n' "$1"; }
pass()  { PASS_COUNT=$((PASS_COUNT+1)); printf '   %s✓ PASS%s %s\n' "$GREEN" "$NC" "$1"; }
fail()  { FAIL_COUNT=$((FAIL_COUNT+1)); printf '   %s✗ FAIL%s %s\n' "$RED" "$NC" "$1"; }
die()   { printf '\n%sFATAL:%s %s\n' "$RED$BOLD" "$NC" "$1" >&2; exit 1; }

# ---------------------------------------------------------------------------
# Preflight
# ---------------------------------------------------------------------------
step "Preflight checks"

for bin in curl jq docker; do
  command -v "$bin" >/dev/null 2>&1 || die "'$bin' is required but not installed."
done

# Pick a python3 that can import cryptography (for token minting).
PYTHON_BIN=""
for cand in python3 python; do
  if command -v "$cand" >/dev/null 2>&1 && "$cand" -c "import cryptography" >/dev/null 2>&1; then
    PYTHON_BIN="$cand"; break
  fi
done
[ -n "$PYTHON_BIN" ] || die "Need a python3 with the 'cryptography' package on PATH (pip install cryptography)."
info "Using python: $(command -v "$PYTHON_BIN")"

# Resolve the encryption key. Prefer the env var; otherwise read it from .env.local.
if [ -z "$ENCRYPTION_KEY" ] && [ -f "$ROOT_DIR/.env.local" ]; then
  ENCRYPTION_KEY="$(grep -E '^ENCRYPTION_KEY=' "$ROOT_DIR/.env.local" | head -1 | cut -d= -f2-)"
fi
[ -n "$ENCRYPTION_KEY" ] || die "ENCRYPTION_KEY not set and not found in .env.local."
[ "${#ENCRYPTION_KEY}" -eq 32 ] || die "ENCRYPTION_KEY must be exactly 32 bytes (got ${#ENCRYPTION_KEY})."

[ -f "$IMAGE_FILE" ] || die "Image fixture not found: $IMAGE_FILE"
[ -f "$VIDEO_FILE" ] || die "Video fixture not found: $VIDEO_FILE (generate with ffmpeg or set VIDEO_FILE)."
info "Image fixture: $IMAGE_FILE ($(wc -c < "$IMAGE_FILE" | tr -d ' ') bytes)"
info "Video fixture: $VIDEO_FILE ($(wc -c < "$VIDEO_FILE" | tr -d ' ') bytes)"

# API health
if curl -fsS "$API/healthz" >/dev/null 2>&1; then
  pass "API healthy at $API"
else
  die "API not reachable at $API/healthz — is the stack up?"
fi

# Postgres reachable via the container
if docker exec "$PG_CONTAINER" pg_isready -U "$PG_USER" -d "$PG_DB" >/dev/null 2>&1; then
  pass "Postgres healthy ($PG_CONTAINER)"
else
  die "Postgres not ready in container $PG_CONTAINER."
fi

# Webhook receiver present (overlay)
if docker ps --format '{{.Names}}' | grep -q "^${RECEIVER_CONTAINER}$"; then
  pass "Webhook receiver running ($RECEIVER_CONTAINER)"
else
  die "Webhook receiver $RECEIVER_CONTAINER not running. Start with the webhooks overlay:
    docker compose -f docker-compose.yml -f docker-compose.webhooks.yml up -d"
fi

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
psql_q() { docker exec "$PG_CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -tAc "$1" 2>/dev/null; }

mint_token() {
  ENCRYPTION_KEY="$ENCRYPTION_KEY" USER_ID="$USER_ID" "$PYTHON_BIN" - <<'PY'
import base64, os
from cryptography.hazmat.primitives.ciphers.aead import AESGCM
key = os.environ["ENCRYPTION_KEY"].encode()
uid = os.environ["USER_ID"].encode()
nonce = os.urandom(12)
ct = AESGCM(key).encrypt(nonce, uid, None)
print(base64.urlsafe_b64encode(nonce + ct).rstrip(b"=").decode())
PY
}

# ---------------------------------------------------------------------------
# Auth token + webhook registration
# ---------------------------------------------------------------------------
step "Mint auth token (user=$USER_ID)"
TOKEN="$(mint_token)" || die "token generation failed"
[ -n "$TOKEN" ] || die "empty token"
AUTH="Authorization: Bearer $TOKEN"
pass "Token minted (${TOKEN:0:16}…)"

step "Register webhook"
REG_RESP="$(curl -fsS -X POST "$API/api/v1/webhooks" \
  -H "$AUTH" -H "Content-Type: application/json" \
  -d "{\"url\":\"$WEBHOOK_RECEIVER_URL\",\"secret\":\"$WEBHOOK_SECRET\",\"events\":[\"job.starting\",\"job.started\",\"job.done\",\"job.failed\"]}")" \
  || die "webhook registration request failed"
WEBHOOK_ID="$(echo "$REG_RESP" | jq -r '.data.id // empty')"
if [ -n "$WEBHOOK_ID" ]; then
  pass "Webhook registered (id=$WEBHOOK_ID -> $WEBHOOK_RECEIVER_URL)"
else
  die "webhook registration returned no id: $REG_RESP"
fi

# ---------------------------------------------------------------------------
# Core pipeline runner (per asset)
# ---------------------------------------------------------------------------
# run_asset <label> <file> <content-type> <ready-timeout> <expect: image|video>
# On success sets the global LAST_ASSET_ID. Returns non-zero on hard failure.
LAST_ASSET_ID=""
run_asset() {
  local label="$1" file="$2" ctype="$3" timeout="$4" kind="$5"
  local size assetId uploadUrl
  LAST_ASSET_ID=""

  step "[$label] Presign upload"
  size="$(wc -c < "$file" | tr -d ' ')"
  local presign
  presign="$(curl -fsS -X POST "$API/api/v1/storage/presign" \
    -H "$AUTH" -H "Content-Type: application/json" \
    -d "{\"fileName\":\"$(basename "$file")\",\"contentType\":\"$ctype\",\"size\":$size}")" \
    || { fail "[$label] presign request failed"; return 1; }

  assetId="$(echo "$presign" | jq -r '.data.assetId // empty')"
  uploadUrl="$(echo "$presign" | jq -r '.data.uploadUrl // empty')"
  [ -n "$assetId" ] && [ -n "$uploadUrl" ] || { fail "[$label] presign missing assetId/uploadUrl: $presign"; return 1; }
  info "assetId=$assetId"

  # The presigned URL must target the host-reachable public endpoint.
  case "$uploadUrl" in
    http://localhost:9000/*) pass "[$label] presigned URL uses public host (localhost:9000)" ;;
    *) fail "[$label] presigned URL is not host-reachable: $uploadUrl"; return 1 ;;
  esac

  step "[$label] Upload file to presigned URL (from host)"
  local code
  code="$(curl -s -o /dev/null -w '%{http_code}' -X PUT "$uploadUrl" \
    -H "Content-Type: $ctype" --data-binary @"$file")"
  if [ "$code" = "200" ]; then
    pass "[$label] uploaded to MinIO (HTTP $code)"
  else
    fail "[$label] upload failed (HTTP $code)"; return 1
  fi

  step "[$label] Mark asset complete (enqueue job)"
  if curl -fsS "$API/api/v1/assets/$assetId/complete" -H "$AUTH" >/dev/null; then
    pass "[$label] marked complete"
  else
    fail "[$label] complete request failed"; return 1
  fi

  step "[$label] Wait for worker to finish (timeout ${timeout}s)"
  local status="" waited=0
  while [ "$waited" -lt "$timeout" ]; do
    status="$(psql_q "SELECT status FROM assets WHERE asset_id='$assetId';")"
    case "$status" in
      ready) break ;;
      failed)
        local reason; reason="$(psql_q "SELECT error_reason FROM assets WHERE asset_id='$assetId';")"
        fail "[$label] asset FAILED: $reason"; return 1 ;;
    esac
    sleep 2; waited=$((waited+2))
    printf '   …status=%s (%ss)\r' "${status:-?}" "$waited"
  done
  printf '\n'
  if [ "$status" = "ready" ]; then
    pass "[$label] asset reached 'ready' in ~${waited}s"
  else
    fail "[$label] asset not ready after ${timeout}s (last status=${status:-none})"; return 1
  fi

  step "[$label] Verify variants"
  if [ "$kind" = "image" ]; then
    local n; n="$(psql_q "SELECT count(*) FROM variants.image WHERE asset_id='$assetId';")"
    if [ "${n:-0}" -eq 3 ]; then
      pass "[$label] 3 image variants present"
      psql_q "SELECT '     - '||role||' '||COALESCE(width::text,'?')||'x'||COALESCE(height::text,'?')||' '||format FROM variants.image WHERE asset_id='$assetId' ORDER BY role;"
    else
      fail "[$label] expected 3 image variants, found ${n:-0}"
    fi
  else
    local nv ni; nv="$(psql_q "SELECT count(*) FROM variants.video WHERE asset_id='$assetId';")"
    ni="$(psql_q "SELECT count(*) FROM variants.image WHERE asset_id='$assetId';")"
    if [ "${nv:-0}" -ge 2 ] && [ "${ni:-0}" -ge 1 ]; then
      pass "[$label] video variants present ($nv video + $ni poster)"
      psql_q "SELECT '     - video/'||role||' '||COALESCE(resolution,'?') FROM variants.video WHERE asset_id='$assetId' ORDER BY role;"
      psql_q "SELECT '     - image/'||role FROM variants.image WHERE asset_id='$assetId' ORDER BY role;"
    else
      fail "[$label] expected >=2 video + >=1 poster, found ${nv:-0} video / ${ni:-0} image"
    fi
  fi

  step "[$label] Verify a variant is fetchable from the host"
  local vurl
  if [ "$kind" = "image" ]; then
    vurl="$(psql_q "SELECT url FROM variants.image WHERE asset_id='$assetId' ORDER BY role LIMIT 1;")"
  else
    vurl="$(psql_q "SELECT url FROM variants.video WHERE asset_id='$assetId' ORDER BY role LIMIT 1;")"
  fi
  if [ -n "$vurl" ]; then
    case "$vurl" in http://localhost:9000/*) : ;; *) fail "[$label] variant URL not host-public: $vurl";; esac
    local vcode; vcode="$(curl -s -o /dev/null -w '%{http_code}' "$vurl")"
    if [ "$vcode" = "200" ]; then
      pass "[$label] variant downloadable from host (HTTP 200): ${vurl}"
    else
      fail "[$label] variant not fetchable (HTTP $vcode): $vurl"
    fi
  else
    fail "[$label] no variant URL found to fetch"
  fi

  # Publish the asset id to the caller via a global (no subshell, so PASS/FAIL
  # counters and live output are preserved).
  LAST_ASSET_ID="$assetId"
  return 0
}

run_asset "IMAGE" "$IMAGE_FILE" "image/jpeg" "$IMAGE_READY_TIMEOUT" "image"
IMAGE_ASSET_ID="$LAST_ASSET_ID"
run_asset "VIDEO" "$VIDEO_FILE" "video/mp4" "$VIDEO_READY_TIMEOUT" "video"
VIDEO_ASSET_ID="$LAST_ASSET_ID"

# ---------------------------------------------------------------------------
# Webhook delivery verification
# ---------------------------------------------------------------------------
verify_webhooks() {
  local label="$1" assetId="$2"
  step "[$label] Verify webhook deliveries"
  [ -n "$assetId" ] || { fail "[$label] no asset id for webhook check"; return 1; }

  local waited=0 delivered=""
  while [ "$waited" -lt "$WEBHOOK_TIMEOUT" ]; do
    delivered="$(psql_q "SELECT string_agg(event, ',' ORDER BY event) FROM webhook_deliveries WHERE asset_id='$assetId' AND status='delivered';")"
    if echo "$delivered" | grep -q "job.starting" \
       && echo "$delivered" | grep -q "job.started" \
       && echo "$delivered" | grep -q "job.done"; then
      break
    fi
    sleep 2; waited=$((waited+2))
  done

  for ev in job.starting job.started job.done; do
    if echo "$delivered" | grep -q "$ev"; then
      pass "[$label] webhook delivered: $ev"
    else
      fail "[$label] webhook NOT delivered: $ev"
    fi
  done
  # Surface the full delivery table for this asset.
  psql_q "SELECT '     '||event||' -> '||status||' (attempts='||attempts||')' FROM webhook_deliveries WHERE asset_id='$assetId' ORDER BY created_at;"
}

verify_webhooks "IMAGE" "$IMAGE_ASSET_ID"
verify_webhooks "VIDEO" "$VIDEO_ASSET_ID"

step "Recent webhook receiver logs"
docker logs "$RECEIVER_CONTAINER" --tail 15 2>&1 | sed 's/^/   /' || true

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
step "Summary"
printf '   %sPASS: %d%s   %sFAIL: %d%s\n' "$GREEN" "$PASS_COUNT" "$NC" "${RED}${BOLD}" "$FAIL_COUNT" "$NC"
if [ "$FAIL_COUNT" -eq 0 ]; then
  printf '   %s%sAll flows green — demo ready.%s\n' "$GREEN" "$BOLD" "$NC"
  exit 0
else
  printf '   %s%s%d check(s) failed.%s\n' "$RED" "$BOLD" "$FAIL_COUNT" "$NC"
  exit 1
fi
