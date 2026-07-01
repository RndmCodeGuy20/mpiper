# Track 4 — Multi-tenancy, Auth & Quotas — Design

**Status:** accepted · **Scope:** fully local (no k8s) · **Predecessor:** see
[`track-04-handoff.md`](track-04-handoff.md).

This document records the confirmed design decisions for Track 4 before
implementation. It turns the single-user, best-effort API into one that safely
serves multiple tenants: real authN/authZ, tenant isolation on every asset
operation, idempotency keys, and per-tenant quotas/rate limits.

---

## 1. Tenancy model — flat `owner_id`

**Decision:** flat tenancy. `assets.owner_id` (TEXT) **is** the tenant
identifier. No `org → project → asset` hierarchy.

- The column **keeps its name** (`owner_id`). Renaming it to `tenant_id` would
  ripple into the worker (`worker/webhooks.py` and its test assert the exact SQL
  `JOIN assets a ON a.owner_id = wr.user_id`). Keeping the name means **zero
  worker churn**. "tenant" is only the in-process *concept*; on disk it stays
  `owner_id` / `webhook_registrations.user_id`.
- The tenant identifier is a free-form TEXT string sourced from the API key (see
  §2). There is no separate `users`/`tenants` table — the `api_keys` table is the
  identity source of record.

**Rejected:** `org → project` hierarchy — more schema + bigger worker/webhook
ripple for no near-term value. The high-value core is the IDOR fix +
idempotency, not a richer tenancy graph.

## 2. Auth — scoped API keys (drop the AES token)

**Decision:** replace the homegrown AES-256-GCM token with **scoped API keys**.

- **Wire format:** `mp_<prefix>_<secret>` where `prefix` is a short public
  identifier (used to narrow lookups and shown in listings) and `secret` is
  high-entropy random.
- **At rest:** only the **SHA-256 hash** of the full key is stored
  (`api_keys.key_hash`, UNIQUE). API keys are high-entropy, so a fast hash with
  an indexed equality lookup is appropriate (bcrypt is for low-entropy
  passwords and cannot be indexed). The plaintext key is shown **once** at mint
  time and never persisted.
- **Lifecycle:** keys carry optional `expires_at` and `revoked_at`. The auth
  middleware rejects missing/unknown/expired/revoked keys with `401`.
- **Scopes:** `scopes JSONB` is carried for future authorization granularity;
  initially keys are minted with a broad scope.
- **Context contract:** the single chokepoint `middleware.GetUserID` is renamed
  to `middleware.GetTenant`, returning the tenant id (and scopes) from the
  validated key. All call sites move to `GetTenant`.
- **No HTTP admin surface.** Keys are minted out-of-band via a CLI
  (`cmd/mint-api-key`, wrapped by `scripts/mint-api-key.sh`) that inserts a row
  and prints the plaintext once.

The old AES auth path (`utils.GenerateToken`/`DecryptToken` for auth) is
**removed**. The demo/loadtest/test-webhooks scripts and the README are cut over
to mint and use an API key.

### `api_keys` schema

| column       | type          | notes                                  |
|--------------|---------------|----------------------------------------|
| `id`         | UUID PK       | `gen_random_uuid()` / `uuid_generate_v4()` |
| `tenant_id`  | TEXT NOT NULL | the tenant identifier (== `owner_id`)  |
| `key_hash`   | TEXT UNIQUE NOT NULL | SHA-256 hex of full `mp_..._...` key |
| `prefix`     | TEXT NOT NULL | public, indexed; narrows lookup        |
| `scopes`     | JSONB NOT NULL DEFAULT `'[]'` | reserved for authz     |
| `expires_at` | TIMESTAMPTZ   | NULL = never expires                   |
| `revoked_at` | TIMESTAMPTZ   | NULL = active                          |
| `created_at` | TIMESTAMPTZ NOT NULL DEFAULT now() |                   |

Indexes on `prefix` and `key_hash`.

## 3. Key split — separate webhook-signing key from auth key

**Decision:** introduce `WEBHOOK_ENCRYPTION_KEY` (32 bytes). Webhook secrets are
encrypted/decrypted with it (`service/webhook.go` + `webhook/dispatcher.go`)
instead of the shared `ENCRYPTION_KEY`. After the API-key cutover, `ENCRYPTION_KEY`
is no longer used for auth; the webhook key fully owns webhook-secret encryption,
so a leak of one no longer compromises the other.

**Migration:** because the project is **pre-launch and local**, existing
`webhook_registrations` rows are **truncated** rather than re-encrypted — no
dual-read window or one-time re-encrypt pass is needed.

## 4. Tenant isolation — repository-layer scoping (close the IDOR)

**Decision:** enforce `WHERE owner_id = $tenant` at the repository layer so a
caller can never touch another tenant's asset by ID.

- The verified IDOR surface today is the **`complete` write path**:
  `MarkAssetUploadedTx` updates by `asset_id` alone. The owner guard is added:
  `WHERE asset_id = $1 AND owner_id = $tenant AND status = 'uploading'`.
- The service maps "0 rows / not owned" to a **404** (indistinguishable from a
  non-existent asset — no cross-tenant existence leak).
- The tenant id is threaded from context (`GetTenant`) into the asset
  service/repo calls.

**Migration:** delete existing `assets` (and dependent rows) — pre-launch local
data — then `ALTER COLUMN owner_id SET NOT NULL` and add index `idx_assets_owner`.

### Deferred: per-tenant storage prefix

Per-tenant object prefixes (`media/<tenant>/raw/...`) are **out of scope** for
this track. The worker reconstructs `media/raw/<assetId>`
(`worker/processing/processor.py`) and its processed-output keys from `asset_id`
without selecting `owner_id`; prefixing would break the worker's download/upload
paths and require threading tenant through the worker SQL + key construction +
tests — exactly the worker churn the flat-tenancy decision avoids. Asset IDs are
UUIDs, access is gated by presigned URLs, and the IDOR is closed at the DB layer,
so per-tenant prefixing is defense-in-depth, deferrable to a later track.

## 5. Idempotency keys — full response replay, 24h TTL

**Decision:** Stripe-style idempotency on `presign` (and `complete` when the
header is present), scoped per-tenant, with **full response replay**.

- **Header:** `Idempotency-Key`. Absent → behave exactly as today (no-op).
- **Storage:** `idempotency_keys` table, PK `(tenant_id, key)`:

  | column                | type         | notes                                |
  |-----------------------|--------------|--------------------------------------|
  | `tenant_id`           | TEXT         | part of PK                           |
  | `key`                 | TEXT         | part of PK                           |
  | `request_fingerprint` | TEXT         | hash of method+path+body             |
  | `status`              | TEXT         | `pending` / `done`                   |
  | `response_status`     | INT          | replayed HTTP status                 |
  | `response_body`       | JSONB        | replayed body                        |
  | `asset_id`            | UUID         | created asset (nullable)             |
  | `created_at`          | TIMESTAMPTZ  |                                      |
  | `expires_at`          | TIMESTAMPTZ  | `created_at + 24h`                   |

- **Concurrency:** the first request inserts a `pending` row; the PK unique
  constraint is the lock. Concurrent duplicates that collide on the `pending`
  row get `409` (in-flight). Once `done`, replays within TTL return the stored
  response **verbatim**.
- **Reuse with a different payload:** same `(tenant, key)` but a different
  `request_fingerprint` → `422` (key reused for a different request).
- **TTL:** 24h. A background sweep deletes expired rows.

## 6. Quotas + rate limits — per-tenant

**Decision:** per-tenant token-bucket rate limiting (keyed by `tenant_id`,
replacing/extending the existing per-IP `presignRateLimiter`) returning `429`
with `Retry-After`, plus usage accounting (assets and/or bytes per tenant)
checked on `presign` with over-quota rejection. Limits are config-driven with
sane defaults, and a per-tenant usage metric is exposed.

## 7. Sequencing

Auth (Tasks 1–3) lands first because idempotency and tenant scoping both depend
on a real tenant in context, and no identity table existed before. Then tenant
scoping (Task 4) closes the IDOR, and idempotency (Task 5) + quotas (Task 6)
layer on top. Each task is independently demoable.

## 8. Compatibility / landmines

- `ENCRYPTION_KEY` remains a required 32-byte boot config (webhook key split
  adds `WEBHOOK_ENCRYPTION_KEY` alongside it).
- Three scripts + README mint the old AES token inline; all are cut over to API
  keys in Task 2/3.
- `owner_id` column name is preserved → worker untouched.
