#!/usr/bin/env bash
# scripts/test-webhooks.sh
# End-to-end webhook dev-test script.
# Prerequisites: docker compose stack running with webhooks overlay.
#   docker compose -f docker-compose.yml -f docker-compose.webhooks.yml up -d
#
# Usage: ./scripts/test-webhooks.sh

set -euo pipefail

API="http://localhost:5010"
WEBHOOK_RECEIVER="http://webhook-receiver:8080"  # internal docker network URL
ENCRYPTION_KEY="${ENCRYPTION_KEY:-change_me_to_a_32_byte_secret____}"

echo "=== 1. Generate auth token ==="
# Create a token for user "demo-user" using the same AES encryption the API uses.
# For dev-testing, we call the API with a pre-generated token.
TOKEN=$(python3 -c "
import sys, os
sys.path.insert(0, '.')
# Simple AES-GCM token generation matching pkg/utils/crypt.go
from cryptography.hazmat.primitives.ciphers.aead import AESGCM
import base64, os as _os
key = b'${ENCRYPTION_KEY}'
nonce = _os.urandom(12)
aes = AESGCM(key)
ct = aes.encrypt(nonce, b'demo-user', None)
token = base64.urlsafe_b64encode(nonce + ct).rstrip(b'=').decode()
print(token)
" 2>/dev/null || echo "MANUAL_TOKEN_NEEDED")

if [ "$TOKEN" = "MANUAL_TOKEN_NEEDED" ]; then
  echo "Could not auto-generate token. Set TOKEN env var manually."
  echo "  export TOKEN=<your-bearer-token>"
  exit 1
fi

echo "Token: ${TOKEN:0:20}..."
AUTH="Authorization: Bearer $TOKEN"

echo ""
echo "=== 2. Register webhook ==="
REG=$(curl -s -X POST "$API/api/v1/webhooks" \
  -H "Content-Type: application/json" \
  -H "$AUTH" \
  -d "{
    \"url\": \"$WEBHOOK_RECEIVER\",
    \"secret\": \"my-dev-secret\",
    \"events\": [\"job.starting\", \"job.started\", \"job.done\", \"job.failed\"]
  }")
echo "$REG" | python3 -m json.tool 2>/dev/null || echo "$REG"

WEBHOOK_ID=$(echo "$REG" | python3 -c "import sys,json; print(json.load(sys.stdin).get('data',{}).get('id',''))" 2>/dev/null || echo "")
echo "Webhook ID: $WEBHOOK_ID"

echo ""
echo "=== 3. List webhooks ==="
curl -s "$API/api/v1/webhooks" -H "$AUTH" | python3 -m json.tool 2>/dev/null

echo ""
echo "=== 4. Upload an asset (triggers job.starting) ==="
UPLOAD=$(curl -s -X POST "$API/api/v1/storage/presign" \
  -H "Content-Type: application/json" \
  -H "$AUTH" \
  -d '{
    "fileName": "test.jpg",
    "contentType": "image/jpeg",
    "size": 1024
  }')
echo "$UPLOAD" | python3 -m json.tool 2>/dev/null || echo "$UPLOAD"

ASSET_ID=$(echo "$UPLOAD" | python3 -c "import sys,json; print(json.load(sys.stdin).get('data',{}).get('assetId',''))" 2>/dev/null || echo "")
UPLOAD_URL=$(echo "$UPLOAD" | python3 -c "import sys,json; print(json.load(sys.stdin).get('data',{}).get('uploadUrl',''))" 2>/dev/null || echo "")
echo "Asset ID: $ASSET_ID"

if [ -n "$UPLOAD_URL" ] && [ "$UPLOAD_URL" != "None" ]; then
  echo ""
  echo "=== 5. Upload file to presigned URL ==="
  # Create a dummy JPEG file
  printf '\xff\xd8\xff\xe0' > /tmp/test-webhook.jpg
  dd if=/dev/urandom bs=1020 count=1 >> /tmp/test-webhook.jpg 2>/dev/null
  curl -s -X PUT "$UPLOAD_URL" \
    -H "Content-Type: image/jpeg" \
    --data-binary @/tmp/test-webhook.jpg
  echo "Uploaded."

  echo ""
  echo "=== 6. Mark asset uploaded (triggers job.starting webhook) ==="
  curl -s "$API/api/v1/assets/$ASSET_ID/complete" -H "$AUTH" | python3 -m json.tool 2>/dev/null
fi

echo ""
echo "=== 7. Check webhook deliveries in DB ==="
echo "Run: docker exec mpiper-postgres psql -U mpiper -d mpiper -c \"SELECT id, event, status, attempts FROM webhook_deliveries ORDER BY created_at DESC LIMIT 10;\""

echo ""
echo "=== 8. Check webhook receiver logs ==="
echo "Run: docker logs mpiper-webhook-receiver --tail 20"

echo ""
echo "=== Done! ==="
echo "The webhook dispatcher polls every 2s. Check the receiver logs to see delivered payloads."
