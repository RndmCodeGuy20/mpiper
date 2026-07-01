#!/usr/bin/env bash
# scripts/_apikey.sh
#
# Shared helper for the dev/e2e/loadtest scripts: mint a scoped API key and seed
# it directly into Postgres via `docker exec psql`. This mirrors what
# `cmd/mint-api-key` does (mp_<prefix>_<secret>, SHA-256-hashed at rest) but runs
# against the containerized DB without needing the Go toolchain or an exposed DB
# port on the host. Only a stdlib python3 is required (hashlib + os.urandom).
#
# Source this file, then call:  KEY="$(mint_api_key <tenant>)"
#
# Honors PG_CONTAINER / PG_USER / PG_DB (defaults: mpiper-postgres / mpiper / mpiper).

PG_CONTAINER="${PG_CONTAINER:-mpiper-postgres}"
PG_USER="${PG_USER:-mpiper}"
PG_DB="${PG_DB:-mpiper}"
APIKEY_PYTHON_BIN="${APIKEY_PYTHON_BIN:-python3}"

# gen_api_key prints "<full> <hash> <prefix>" for a fresh key. The format and
# hashing MUST match pkg/utils/apikey.go (mp_<4-byte-hex>_<24-byte-hex>,
# key_hash = sha256_hex(full)).
gen_api_key() {
  "$APIKEY_PYTHON_BIN" - <<'PY'
import os, hashlib
prefix = os.urandom(4).hex()
secret = os.urandom(24).hex()
full = f"mp_{prefix}_{secret}"
print(full, hashlib.sha256(full.encode()).hexdigest(), prefix)
PY
}

# mint_api_key <tenant> — inserts a key row and echoes the plaintext key.
mint_api_key() {
  local tenant="$1"
  local full hash prefix
  read -r full hash prefix < <(gen_api_key)
  docker exec "$PG_CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -tAc \
    "INSERT INTO api_keys (tenant_id, key_hash, prefix, scopes) VALUES ('${tenant}', '${hash}', '${prefix}', '[\"assets:write\",\"webhooks:write\"]'::jsonb);" \
    >/dev/null
  echo "$full"
}
