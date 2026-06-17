# Spec: Ingress Transactional Outbox + Idempotent Consumer

> **Status:** Draft — for review & strengthening.
> **Owner:** Shantanu Mane.
> **Related:** [reliability-and-correctness.md](./reliability-and-correctness.md) (spine item #1).
> **Linear epic:** [DEV-55](https://linear.app/shans-odyssey/issue/DEV-55) (children DEV-56…DEV-60).
> **Last updated:** 2026-06-17.

---

## 1. Summary

Close the producer-side dual-write hazard in the asset-upload path by introducing a
**transactional outbox** on ingress (API → Redis stream), and make the worker
**effectively-once** by formalising the consumer's existing idempotency. Today the API
commits the job row and *then* publishes to Redis in a separate step; a crash between
the two strands the job until a slow recovery sweep notices. The outbox makes
"intent to publish" part of the same DB transaction, and a relay publishes
asynchronously with at-least-once delivery — which the idempotent consumer absorbs.

## 2. Goals / Non-goals

**Goals**
- No job can be committed without a durable, atomic intent-to-publish.
- Bounded, observable publish latency (relay interval), not a 2-min recovery floor.
- At-least-once delivery from the relay, absorbed by an effectively-once consumer.
- Keep the change minimal and consistent with the already-designed egress outbox
  (DEV-40/47), so the two halves of the system share one mental model.

**Non-goals (this spec)**
- Trace-context propagation through the stream + OTel on the worker → **fast-follow**
  (separate issue).
- Egress/webhook outbox → already covered by DEV-40 family.
- CDC-based relay (logical replication / Debezium) → documented as the "100×" option,
  not built now.
- Exactly-once *delivery* → explicitly out of scope; we target effectively-once
  *effects*.

## 3. Background — current behaviour

### Producer dual-write (`internal/service/asset.go` → `MarkAssetUploaded`, ~L179)

```
BEGIN TX
  MarkAssetUploadedTx       -- assets.status = uploaded            (L226)
  InsertProcessAssetJobTx   -- jobs row, status = pending          (L246)
COMMIT                       -- L260
queue.Enqueue({job_id, asset_id, event:"asset_uploaded"})  -- L271, AFTER commit
```

**Hazard:** crash/restart/network failure between `COMMIT` (L260) and `Enqueue`
(L271) → job durably `pending`, no stream message ever published.

**Current backstop:** worker `_recover_stuck_pending` (`consumer.py:344`, DEV-35)
re-queues `pending`/`in_progress` jobs whose `updated_at < now() - interval '2
minutes'`. Correctness currently *depends* on this sweep; publish latency floor under
failure is ~2 min, and the guarantee is implicit.

### Consumer idempotency (already partially present)

`_handle_job` (`consumer.py:188`):
- `SELECT job_id, asset_id, status, attempts FROM jobs WHERE job_id=%s FOR UPDATE` (L199) — claims the job.
- **`if status == "done": xack and return`** (L212–214) — already acks-and-skips redelivered, completed jobs.
- Else `UPDATE … status='in_progress', attempts=attempts+1` (L219), dispatch, then on success `status='done'` + asset `ready` + `xack` (L266–276).
- Variants are content-addressed (`variant_hash`), so storage writes are already idempotent.

So a `done`-fast-path exists; this spec **hardens and documents** it rather than building it.

## 4. Design

### 4.1 `event_outbox` table

```sql
CREATE TABLE event_outbox (
    id            BIGSERIAL PRIMARY KEY,
    aggregate_id  UUID        NOT NULL,              -- asset_id
    job_id        BIGINT,                            -- nullable; canonical when present
    event         TEXT        NOT NULL,              -- e.g. 'asset_uploaded'
    payload       JSONB       NOT NULL,              -- exact stream body to publish
    traceparent   TEXT,                              -- reserved for fast-follow; nullable now
    status        TEXT        NOT NULL DEFAULT 'pending',  -- pending | published
    attempts      INT         NOT NULL DEFAULT 0,
    last_error    TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at  TIMESTAMPTZ
);

-- Relay hot path: pending rows oldest-first.
CREATE INDEX idx_event_outbox_pending
    ON event_outbox (id) WHERE status = 'pending';
```

Notes:
- `payload` stores the *exact* map the producer would have passed to `Enqueue`, so the
  relay is a dumb pipe (no business logic).
- Partial index keeps the poller scan cheap as published rows accumulate.
- Retention/cleanup: see §11.

### 4.2 Producer change (`MarkAssetUploaded`)

- Inside the **existing transaction**, after `InsertProcessAssetJobTx`, `INSERT` one
  `event_outbox` row with `payload = {job_id, asset_id, event:'asset_uploaded'}`.
- **Remove the post-commit `queue.Enqueue` call (L271–…).** Publishing is now the
  relay's job.
- Net effect: `{asset.status, jobs row, outbox row}` commit atomically or not at all.

### 4.3 Stream envelope / payload

Unchanged from today's `Enqueue` body (`{"body": json(payload)}` on stream
`media:jobs`), so the worker needs **no parsing change**. `traceparent` column is
reserved but unused until the fast-follow.

### 4.4 Relay publisher (Go, in-API goroutine)

Mirrors the DEV-47 webhook poller for consistency.

```
loop every RELAY_INTERVAL:
  BEGIN TX
    SELECT id, payload FROM event_outbox
      WHERE status='pending'
      ORDER BY id
      LIMIT RELAY_BATCH
      FOR UPDATE SKIP LOCKED            -- safe under multiple API replicas
    for each row:
      id := RedisQueue.Enqueue(payload) -- existing retrying XADD
      UPDATE event_outbox
        SET status='published', published_at=now()
        WHERE id = row.id
  COMMIT
  on Enqueue error: UPDATE attempts=attempts+1, last_error=… ; leave pending
```

- **At-least-once:** if `Enqueue` succeeds but the row update / commit fails, the row
  re-publishes next tick → duplicate stream message → consumer dedup absorbs it.
- `SKIP LOCKED` lets multiple API replicas run the relay without coordination.
- Graceful shutdown: finish in-flight batch, stop loop on server context cancel.

### 4.5 Idempotent consumer (harden existing)

- Keep & document the `done`-fast-path (L212). Add an explicit test for it.
- Confirm the `FOR UPDATE` claim + `attempts` increment behave under redelivery while
  `in_progress` (another worker holds the lock → blocks, then re-reads status).
- **Inbox table deferred:** when DEV-46 (worker inserts a non-idempotent
  `webhook_deliveries` row on completion) lands, add a `processed_messages` inbox keyed
  on stream message id so each effect fires at most once. Tracked as a dependency, not
  built here.

### 4.6 Coexistence with the recovery sweep (DEV-35)

Keep `_recover_stuck_pending` during and after rollout — it becomes a *backstop* for
the relay (e.g. relay down for an extended window), not the primary path. Its 2-min
threshold may be revisited once the relay is the norm.

## 5. Delivery semantics

- **Producer → outbox:** exactly-once (single DB transaction).
- **Relay → stream:** at-least-once.
- **Stream → worker:** at-least-once (Redis consumer group).
- **Worker effects:** effectively-once (job-status dedup + content-addressed variants;
  inbox for future non-idempotent effects).

## 6. Failure modes

| Failure | Before (today) | After |
|---|---|---|
| Crash after COMMIT, before publish | job `pending` until ~2-min sweep | outbox row committed; relay publishes within `RELAY_INTERVAL` |
| Relay enqueues, then crashes pre-mark | n/a | row stays `pending` → re-published → duplicate msg → consumer skips (job `done`) |
| Multiple API replicas run relay | n/a | `FOR UPDATE SKIP LOCKED` → no double-claim |
| Worker crash after side effect, pre-XACK | redelivered; variants safe, other effects may double | `done`-fast-path skips; inbox (future) guards non-idempotent effects |
| Redis stream data loss | sweep re-adds from DB | outbox rows still `pending` if unpublished; published+lost rows rely on sweep (document residual) |
| Relay down for a long window | n/a | recovery sweep backstop still re-queues |

## 7. Ordering

- Per-asset ordering preserved: one outbox row per asset transition, relay reads
  `ORDER BY id`. Cross-asset order under `SKIP LOCKED` batching is not guaranteed and
  not required.

## 8. Configuration

| Env var | Default | Purpose |
|---|---|---|
| `OUTBOX_RELAY_INTERVAL` | `1s` | poll cadence |
| `OUTBOX_RELAY_BATCH` | `100` | rows per tick |
| `OUTBOX_RELAY_ENABLED` | `true` | kill-switch for rollout |
| `OUTBOX_RETENTION` | `168h` | published-row cleanup age (§11) |

## 9. Observability

Metrics (OTel, API side): `outbox_pending_gauge`, `outbox_published_total`,
`outbox_publish_failures_total`, `outbox_relay_lag_seconds` (now − oldest pending
`created_at`). Alert on lag breaching a threshold (the new, *explicit* SLO that
replaces the implicit 2-min floor).

## 10. Testing strategy

- **Unit:** producer writes outbox row in-tx and no longer calls `Enqueue`; relay
  marks rows published; relay leaves rows pending on enqueue error.
- **Integration (testcontainers: postgres + redis):** end-to-end mark-uploaded →
  relay → stream → worker → asset `ready`.
- **Chaos (issue F):** kill the API between COMMIT and the relay tick → assert the job
  still reaches the stream. Kill the worker after `PutObject`, before `XACK` → assert
  exactly one variant row + one storage object after redelivery.

## 11. Migration & rollout

1. Ship the migration (additive; no impact on existing flow).
2. Ship producer + relay behind `OUTBOX_RELAY_ENABLED`. With the flag on, the producer
   writes the outbox row and stops direct-enqueuing; the relay publishes.
3. Recovery sweep stays on throughout (backstop).
4. Cleanup job/cron deletes `status='published' AND published_at < now() - OUTBOX_RETENTION`.

Rollback: flip `OUTBOX_RELAY_ENABLED=false` and restore the direct `Enqueue` (keep the
old call path behind the flag during the first release for safety).

## 12. Tradeoffs

- **Polling vs CDC:** polling chosen — no extra infra, matches DEV-47, fine at this
  scale. CDC is the documented 100× path (ADR candidate).
- **In-API goroutine vs separate relay process:** in-API now; extractable later.
- **Two outboxes (ingress + egress) vs one abstraction:** kept separate (different
  producers/consumers); shared pattern is an ADR candidate, not premature abstraction.
- **Dedup granularity:** job-status now; message-id inbox when non-idempotent effects
  land.

## 13. Open questions (to strengthen during review)

1. Should the producer keep a **dual-write fallback** (direct `Enqueue`) behind the
   flag for the first release, or cut straight to outbox-only?
2. Relay **interval/batch** defaults — tune against expected upload rate.
3. Should the relay run on **all API replicas** (SKIP LOCKED) or be leader-elected to
   reduce DB churn?
4. **Retention**: cron vs `pg_partman` partitioned outbox for cheap drops at high
   volume.
5. Do we want a **`max_attempts` → dead-letter** state on the outbox itself (poison
   payload), mirroring the egress DLQ?

## 14. Work breakdown → Linear

| Issue | Scope | Depends on |
|---|---|---|
| **DEV-56** | `event_outbox` migration | — |
| **DEV-57** | Producer writes outbox row in-tx + relay publisher (ship together) | DEV-56 |
| **DEV-58** | Idempotent consumer — harden + document `done`-fast-path; reserve inbox | DEV-57 |
| **DEV-59** (fast-follow) | `traceparent` through stream + OTel on worker | DEV-57 |
| **DEV-60** (optional) | Chaos tests — crash-window guarantees | DEV-57, DEV-58 |

> Issue 2 combines producer + relay deliberately: an outbox row with no relay is a
> broken intermediate state (nothing publishes), so the two must land in one PR.
