# Reliability & Correctness Roadmap

> **Living doc.** Captures the "what makes MPiper more than a toy" thinking and the
> distributed-systems work that follows from it. Revisit periodically: check what's
> shipped, what regressed, and what the next highest-leverage piece is.
>
> Last substantive update: 2026-06-17 (v1.0.0).

---

## The thesis

CRUD is reproducible by a single prompt. What isn't: reasoning about what happens
when the worker dies *after* writing to S3 but *before* acking the message. The
differentiator between a toy and a serious system — and the thing that makes an
engineer go "I'd work on this" and a hiring manager go "this person knows their
stuff" — is **demonstrated reasoning about failure modes**, plus the artifacts that
prove the reasoning happened (ADRs, a failure-modes table, chaos tests).

MPiper is an unusually good canvas for this because it is:
- **async** (produce/consume across two services),
- **side-effectful** (object storage, outbound webhooks),
- doing **expensive, non-idempotent work** (transcoding),
- with **fan-out** (one asset → many variants),
- and **money attached** (compute + storage cost).

So the strategy is: **go deep on one coherent reliability spine, and over-document
the judgment behind it.** Don't go wide (more storage backends, k8s autoscaling,
a dashboard) — depth over breadth.

---

## The reliability spine

A single end-to-end story: *delivery & correctness guarantees from API call to
webhook.* Each link references a concrete failure mode.

1. **Transactional outbox (ingress)** — atomic "create intent + publish event".
2. **At-least-once transport** — Redis Streams (already in place).
3. **Idempotent consumer** — the other half of #1; turns at-least-once into
   *effectively-once*.
4. **Webhook delivery (egress outbox)** — signed, retried, dead-lettered.
5. **End-to-end tracing across the async boundary + a reconciliation auditor** —
   observability of *correctness*, not just latency.
6. **Fault-injection tests that prove each guarantee.**

### The key insight (the senior signal)

> "I don't chase exactly-once *delivery* — it's impossible across a network. I make
> the *effects* idempotent, so at-least-once delivery becomes effectively-once."

Outbox alone is only half a solution; shipping it without an idempotent consumer can
even read as naive. The **pair** is what signals understanding.

---

## Current state (grounded in code)

### Ingress dual-write — the real gap (NOT yet planned)

`internal/service/asset.go` → `MarkAssetUploaded` (line ~179):

```
BEGIN TX
  MarkAssetUploadedTx       -- assets.status = uploaded
  InsertProcessAssetJobTx   -- jobs row, status = pending
COMMIT                       -- line ~260
queue.Enqueue(...)           -- line ~271, AFTER commit
```

**Hazard:** crash between `COMMIT` and `Enqueue` → job committed as `pending` but no
stream message is ever published. The worker never sees it.

**Current backstop:** the worker's `_recover_stuck_pending` (DEV-35) re-adds
`pending`/`in_progress` jobs older than ~2 min back to the stream. So the system is
not *broken* — but its correctness depends on a slow, implicit, undocumented polling
sweep. An ingress outbox makes that guarantee **explicit, fast, and atomic**.

### Egress outbox — already planned (DEV-40 family)

The webhook subsystem is, by design, a textbook outbox on the *egress* side:
- **DEV-44 (Done)** — `webhook_registrations` + `webhook_deliveries` (the outbox table).
- **DEV-46 (Backlog)** — worker inserts a `webhook_deliveries` row **in the same
  transaction** as the job-completion write.
- **DEV-47 (Backlog)** — delivery poller: `SELECT … FOR UPDATE SKIP LOCKED`, POST,
  exponential backoff, DLQ after N attempts.
- **DEV-45 (Backlog)** — `POST /webhooks/register`.

So the egress half of the spine is specced. The **ingress half is the symmetric gap.**

### Idempotency today

- **Content-addressed variants** (`variant_hash`) → reprocessing writes the same
  object to the same key and upserts the same row. Naturally idempotent on storage.
- **Retry classification** (DEV-34, DEV-52, Done) → retryable vs terminal failures
  handled correctly; assets no longer stuck `failed` across retries.
- **Gap:** no explicit processed-message / inbox dedup keyed on the stream message
  ID. Reprocessing is *safe* for variants but not *cheap*, and any non-idempotent
  effect added later (e.g. the DEV-46 webhook insert) would double-fire on redelivery.

---

## Ranked additions

Status legend: ✅ done · 🟡 planned (Linear) · 🔴 gap (unplanned).

| # | Addition | Status | Why it's interesting | Discussion threads |
|---|----------|--------|----------------------|--------------------|
| 1 | **Ingress outbox + idempotent consumer** | 🟡 DEV-55 (spec'd) | The dual-write hazard above; effectively-once | polling relay vs CDC; per-asset ordering vs throughput; outbox retention; relay idempotency |
| 2 | **Webhook delivery done right** | 🟡 DEV-40/45/46/47 | At-least-once + HMAC signing + replay protection + circuit breaking + DLQ + replay API | ordering offered to subscribers; "you must be idempotent"; poison-endpoint isolation |
| 3 | **Trace context across the async boundary** | 🔴 gap | Propagate W3C `traceparent` *through the stream message* so one trace spans Go API → relay → worker → storage → webhook. Worker currently uses Prometheus, not OTel | context propagation across language + async boundary |
| 4 | **Reconciliation / auditor job** | 🟡 partial (DEV-35) | Generalize `_recover_stuck_pending` into an invariant scanner: stuck assets, jobs with no terminal state, variant rows whose objects are missing, unrelayed outbox rows | which invariants; alert vs auto-heal |
| 5 | **DLQ + poison handling, surfaced** | 🟡 partial | After N attempts → DLQ + inspect/requeue API + alert | dead-letter schema; replay tooling |
| 6 | **Backpressure & fair scheduling** | 🔴 gap | Stream `MAXLEN` (set to 10k today), consumer-lag metrics (`XPENDING`), scale-on-lag; spicy: weighted fair queueing across tenants | capacity model; tenant starvation |
| 7 | **Client idempotency keys** | 🔴 gap | Stripe-style `Idempotency-Key` on `POST /upload` so client retries don't dupe assets | key storage + TTL; response replay |

**Headline recommendation:** #1 (with #3 folded in) is the highest-leverage, most
distinctive piece, and it's the only spine link with *no* Linear coverage.

---

## Failure modes & guarantees (fill in as we build)

The single most senior-looking artifact. Each row = a failure; columns = outcome +
the mechanism that protects it + residual risk.

| Failure | Today | With ingress outbox + idempotent consumer |
|---------|-------|-------------------------------------------|
| Producer crash after COMMIT, before Enqueue | job stuck `pending` until ~2 min recovery sweep | outbox row committed atomically; relay publishes; bounded by relay interval |
| Worker crash after side effect, before XACK | message redelivered → reprocessed; variants safe (hash), other effects may double | dedup/inbox skips already-processed message |
| Redis data loss | in-flight stream entries lost; recovery sweep re-adds from DB | DB outbox is source of truth; relay re-publishes |
| Partial S3 write | variant object may be incomplete | (open) checksum/verify on read; reconciler flags missing objects |
| Webhook receiver down | (n/a yet) | egress outbox retries w/ backoff → DLQ |

---

## The meta-layer (what actually convinces)

Building the features is table stakes. The reasoning trail is the part a prompt can't
reproduce:

- **ADRs** in `docs/adr/`: "polling outbox vs CDC", "effectively-once not
  exactly-once", "Redis Streams vs Kafka/SQS — and where Redis breaks for us". Short,
  dated, with rejected alternatives.
- **This failure-modes table**, kept current.
- **Chaos / fault-injection tests** (testcontainers + a harness that kills the worker
  at the dangerous moment). The trophy test: *"kill worker after `PutObject`, before
  `XACK`; assert exactly one variant row and one storage object."*
- **An honest "where this breaks at 100×" section** — seniors signal by knowing the
  edges of their own design.

---

## What to avoid (so depth isn't diluted)

- More storage backends (GCS/S3/MinIO is enough — solved).
- Kubernetes autoscaling / service-mesh theater.
- A frontend/admin dashboard (pulls focus from the systems story).
- "Add Kafka" without being able to defend *why Redis is insufficient* — at this
  scale it isn't, so it reads as résumé-driven.

---

## Revisit checklist

When you come back to this doc, ask:
1. Which spine links are now ✅? Did any regress?
2. Is the failure-modes table still accurate against the code?
3. What's the next 🔴 gap with the highest signal-to-effort?
4. Is there an ADR for every non-obvious decision shipped since last visit?
5. Is there a chaos test proving each guarantee we *claim*?
