# Track 4 — Multi-tenancy, auth & quotas — Session Handoff (start here)

**Purpose:** everything a fresh conversation needs to begin **Track 4** without
prior context. Read top to bottom, then write a short design doc
(`track-04-multitenancy-auth.md`) before coding, per the per-track philosophy.
**Fully local — no k8s required** (this is why it was picked ahead of Track 2,
which needs a cluster). Assumes Tracks 3, 1, 1b done.

---

## 1. What MPiper is (60-second orientation)

Go **API** (`cmd/server`, `internal/`) + Python **worker** (`worker/`) over
**Redis Streams**; **Postgres** is source of truth; **MinIO** stores objects.
Full orientation, topology, runbook, env, landmines: reuse
[`track-01-handoff.md`](track-01-handoff.md) §1, §6, §7.

---

## 2. The goal in one sentence

Turn the single-user, best-effort API into one that safely serves **multiple
tenants** — real authN/authZ (expiring, rotatable credentials), **tenant
isolation** on every asset read/write, **idempotency keys** so client retries
don't duplicate work, and **per-tenant quotas/rate limits**.

---

## 3. Current state & gotchas — verified in code (do these FIRST)

- **Auth is a homegrown AES-256-GCM token with no expiry or rotation.**
  `pkg/utils/crypt.go` `GenerateToken/DecryptToken` encrypts *just the userID*;
  `internal/middleware/authorization.go` decrypts it with `config.MustGet().EncryptionKey`.
  Problems: **no `exp`/issued-at**, no key rotation, opaque (no claims), and the
  middleware comment says "Invalid or expired token" but **nothing ever expires**.
- **⚠️ One key signs everything.** The same `ENCRYPTION_KEY` (exactly 32 bytes)
  encrypts **auth tokens AND webhook secrets** (webhook secrets are stored
  encrypted with it; see `internal/service/webhook.go` + the dispatcher's
  `DecryptToken`). Leaking it compromises both. **Separate the webhook-signing key
  from the auth-signing key** early — it touches stored data, so plan a migration.
- **Tenant tagging exists but is shallow.** `assets.owner_id` was added
  (`migrations/000004_assets_owner_id.up.sql`) and `internal/service/asset.go`
  (~L128) sets it from `middleware.GetUserID(ctx)` on create. The webhook→asset
  join already scopes by it (`JOIN assets a ON a.owner_id = wr.user_id`).
- **⚠️ But reads/writes are NOT consistently owner-scoped — likely IDOR.** Verify
  every asset path (`GET /assets/{id}/complete`, any asset fetch, variant lookups)
  filters by `owner_id` = caller. The repo queries (`internal/repository/asset_repo.go`)
  fetch by `asset_id` alone in places. **Task: enforce tenant scoping at the
  repository layer** so a caller can never touch another tenant's asset by ID.
- **No idempotency keys.** A retried `POST /storage/presign` creates a **duplicate
  asset** every time (confirmed gap — `docs/arch/reliability-and-correctness.md`
  §"Idempotency today", gap #7). There is no `Idempotency-Key` handling and no
  store for replaying prior responses.
- **Flat tenancy + single bucket.** No org→project hierarchy; one MinIO bucket with
  path prefixes (`media/raw/<assetId>`). No per-tenant prefix/credentials.
- **No quotas or per-tenant rate limits.** Any token can submit unbounded work.

---

## 4. Engineering targets (suggested order — highest value / lowest risk first)

1. **Idempotency keys (Stripe-style).** Accept an `Idempotency-Key` header on
   `presign` (and `complete`); store `(tenant, key) → asset_id/response` with a TTL;
   on replay within TTL return the **same** asset + response instead of creating a
   new one. Decide: key storage table + TTL, response replay vs just dedup, scope
   (per-tenant). *Teaches: the idempotency pattern, retry-safety.*
2. **Tenant isolation at the repository layer.** Thread tenant id through context
   → every asset query gets a `WHERE owner_id = $tenant` (or `tenant_id`) guard;
   add tests that a cross-tenant fetch 404s. Close the IDOR. Add per-tenant storage
   **prefixes** (`media/<tenant>/raw/...`).
3. **Real auth.** Either **JWT** (asymmetric keys, `exp`, JWKS rotation) or scoped
   **API keys** (hashed at rest, revocable). Add expiry + rotation; **split the
   webhook-signing secret from the auth key** (migration for existing encrypted
   webhook secrets). Keep the middleware contract (`GetUserID` → now `GetTenant`/claims).
4. **Quotas + rate limits.** Per-tenant request rate limit (middleware) and usage
   accounting (e.g. assets/storage per tenant) with enforcement on `presign`.
   *Teaches: backpressure at the edge, usage metering.*

> Pick a tenancy model up front and document it: minimal is keep `owner_id` =
> tenant; fuller is `org → project → asset` with row scoping. Don't over-build —
> the IDOR fix + idempotency are the high-value core.

## 5. Acceptance / how we'll know it worked

- **Idempotency:** same `Idempotency-Key` replayed → one asset, identical response;
  different key → new asset. Test under concurrent duplicate requests (no race dupes).
- **Isolation:** tenant A cannot read/complete/lookup tenant B's asset by ID
  (returns 404/403); storage objects land under the tenant prefix. Add repo-level +
  HTTP-level tests.
- **Auth:** expired token rejected; rotated signing key still validates
  unexpired tokens (JWKS/keyset); webhook secrets decrypt with their *own* key
  post-migration.
- **Quotas:** a tenant over its limit is throttled/429'd; usage metric per tenant.
- No load test needed, but add a security-focused test suite. Optionally write
  `experiments/0004-tenancy.md` documenting the IDOR-before/after.

## 6. Landmines

- **Key-split migration:** existing `webhook_registrations.secret` rows are
  encrypted with `ENCRYPTION_KEY`. Splitting keys means re-encrypting them — plan a
  one-time migration or dual-read window. Don't strand existing registrations.
- **Don't break the local token-minting path:** `scripts/demo-e2e.sh`,
  `loadtest/run.sh`, and the README all mint the current AES token inline with a
  Python snippet. If you change the token format, update all three or provide a
  compatibility shim, or the demo + load harness break.
- **Context plumbing:** `middleware.GetUserID(ctx)` is the single chokepoint —
  extend it to carry tenant/claims rather than scattering token parsing.
- **Worker side:** the worker also reads `owner_id` (webhook join in
  `worker/webhooks.py`); a tenancy-column rename ripples into the worker SQL + its
  tests. Grep both services.
- **`ENCRYPTION_KEY` is required at boot** (config panics without it, exactly 32
  bytes) — keep that contract or update config validation + all envs.

## 7. First-session scope

1. **Design doc** `track-04-multitenancy-auth.md`: tenancy model (flat owner vs
   org/project), auth choice (JWT vs API keys), idempotency-key storage + TTL,
   key-split migration plan.
2. **Idempotency keys** on `presign`/`complete` (highest value, self-contained).
   *Demo:* replayed key → one asset.
3. **Repository-layer tenant scoping** + the IDOR test. *Demo:* cross-tenant fetch 404s.
4. **Auth hardening** (expiry + rotation + key split) and **quotas** as follow-ups.

## 8. Key reads

- [`track-01-handoff.md`](track-01-handoff.md) — topology/runbook/env (reuse).
- `docs/arch/reliability-and-correctness.md` — §"Idempotency today" + the gap table (gap #7 client idempotency keys; replay protection).
- `pkg/utils/crypt.go` (token gen/decrypt), `internal/middleware/authorization.go` (`GetUserID` chokepoint), `internal/service/asset.go` (~L128 owner_id set; presign/complete flow), `internal/repository/asset_repo.go` (asset queries to scope), `internal/service/webhook.go` + `internal/webhook/dispatcher.go` (shared-key webhook secrets), `migrations/000004_assets_owner_id.*`.
