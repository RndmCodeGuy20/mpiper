#!/usr/bin/env bash
# scripts/mint-api-key.sh
#
# Thin wrapper around `go run ./cmd/mint-api-key` that mints a scoped API key
# for a tenant and prints the plaintext key (shown ONCE) to stdout. The
# human-readable summary is printed to stderr, so capture the key with:
#
#   KEY="$(./scripts/mint-api-key.sh --tenant demo-user)"
#
# All flags are passed straight through to the CLI:
#   --tenant <id>            (required)
#   --env <development|...>  (default: $ENV or development)
#   --expires <duration>     (e.g. 720h; 0/omitted = never)
#   --scopes <a,b,c>         (optional, comma-separated)

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

exec go run ./cmd/mint-api-key "$@"
