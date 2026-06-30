# MPiper Enhancements — Roadmap

This directory tracks the work that takes MPiper from a well-built side project
to a production-grade media platform. Each **track** is chosen to teach a
distinct, transferable systems-engineering concept *and* to add real product
value — not feature-padding.

The philosophy: **write a design doc per track before coding** (problem, options,
decision, tradeoffs, how success is measured), and pair every track with a
load test or chaos experiment so each claim ("now it scales", "now it's
exactly-once") is *demonstrated*, not assumed.

> **Progress:** Track 3 (observability + load testing) ✅, **Track 1 (concurrent
> worker + XAUTOCLAIM recovery + DLQ) ✅**, and **Track 1b (webhook delivery
> throughput) ✅** are done. Track 3 shipped the foundation that makes everything
> measurable — tracing, SLOs, dashboards, a k6 harness. Track 1 then turned the #1
> bottleneck into a **measured 2.37× worker throughput win** (0.73 → 1.73 jobs/s,
> 1 → ~3.2 cores; see [`experiments/0002`](../../experiments/0002-concurrent-worker.md)),
> and Track 1b wired the webhook delivery metrics + concurrent fan-out (see
> [`experiments/0003`](../../experiments/0003-webhook-throughput.md)). **Next: Track 2
> (autoscaling)** — now unblocked, since there is finally a concurrent worker to scale.

## Where we are today

A clean, correct, **single-tenant, best-effort, single-node-throughput** pipeline
with good bones — now fully observable:

- Transactional enqueue via an **outbox relay** (Postgres → Redis Streams).
- An **idempotent-ish consumer** with content-hash dedup.
- **Presigned uploads** with a split internal/public storage endpoint.
- **Webhooks** with HMAC signing + exponential backoff.
- **End-to-end distributed tracing** (API → outbox → Redis → worker → ffmpeg, one
  waterfall per asset), **OTel metrics** on API and worker, **SLO recording rules**,
  provisioned **Grafana dashboards**, and a host-run **k6 load harness** — on the
  bundled Grafana/Tempo/Loki/Prometheus stack.

Known seams where "side project" becomes "system" (verified in code, and now
several of them **measured** under load):

- **~~Single-threaded worker~~ ✅ RESOLVED (Track 1)** — now a bounded
  `ThreadPoolExecutor` honouring `MAX_CONCURRENT_JOBS`. *Measured:* μ rose from
  ~1.1 → **1.73 jobs/s (2.37×)** and worker CPU from 1 → ~3.2 cores at `mcj=4`.
  Lesson banked: tune `MAX_CONCURRENT_JOBS ≈ cores` — `mcj=8` on 4 cores
  *oversubscribed* and was slower.
- **~~Webhook dispatcher can't keep up~~ ✅ RESOLVED (Track 1b)** — concurrent
  `errgroup` fan-out (`WEBHOOK_CONCURRENCY`) + tuned HTTP transport, and the
  previously-unrecorded `webhook_delivery_*` metrics are now wired. *Note:* at
  local scale (fast receiver, CPU-pinned API) the dispatcher kept up even
  serially, so the win here is observability + headroom; see
  [`experiments/0003`](../../experiments/0003-webhook-throughput.md).
- **~~Homegrown recovery~~ ✅ RESOLVED (Track 1)** — replaced the DB-scan + re-`XADD`
  with `XAUTOCLAIM` consumer-group recovery, and added a **dead-letter stream**
  (`media:jobs:dlq`) with failure metadata + a depth gauge for poison/over-retried
  messages (previously dropped/unacked-forever).
- **No raw-upload lifecycle** — objects in `media/raw/` are never deleted.
  *Measured:* ~**50%** of presigned uploads are never completed → orphaned objects
  accumulate.
- **Homegrown auth** — an AES-GCM token with no expiry/rotation, and the same
  `ENCRYPTION_KEY` signs both auth tokens and webhook secrets.
- **Polled high-churn tables** (`jobs`, `event_outbox`, `webhook_deliveries`), grown
  unbounded with cleanup-by-retention only. *Measured:* `event_outbox` kept up with
  **0 backlog** and the DB had headroom (**18 ms** mean query, **5/25** connections);
  only `webhook_deliveries` actually strained.

## What the first load test proved (exp 0001)

Track 3 gave us the instrumentation to stop guessing. The first saturating run
(`open --rate 10/s`, CPU-pinned worker) turned the seams above into a **measured,
ranked** list — and every track below now has a baseline to beat by re-running the
*same* k6 profile and comparing the dashboards.

| Finding (measured) | What it means | Owner |
|---|---|---|
| Worker μ ≈ **1.1 jobs/s**, CPU 98%, queue → **2,544** | Single-threaded worker is the throughput ceiling | **Track 1 (P0)** |
| `webhook_pending` → **5,901**, never drains | Dispatcher delivery rate ≪ insertion rate | **Track 1b (P1, new)** |
| `event_outbox` **0 backlog**; DB **18 ms** mean, **5/25** conns | Outbox + DB have large headroom *today* | Track 7 → **defer** |
| `webhook_deliveries` is the one polled table straining | The real, current trigger for data-layer work | Track 7 → **rescope to this** |
| **~50%** of presigns never completed → orphaned `media/raw/` | Storage grows with abandoned uploads | Track 5 (small) |
| `/complete` p99 **358 ms** (synchronous MinIO HEAD) | Minor hot-path tail | Track 5 (small) |

Net effect: **Track 1 is confirmed P0**, a **webhook-throughput bottleneck was
surfaced that no track owned** (now Track 1b), and **Track 7's table-partitioning is
premature** — the DB isn't the problem yet; the webhook *delivery loop* is.

## Tracks

| # | Track | Core systems lesson | Status |
|---|-------|---------------------|--------|
| 1 | [Concurrent worker + proper stream recovery + DLQ](track-01-concurrent-worker.md) | Concurrency models, at-least-once recovery, poison-message handling, head-of-line blocking | **done ✅ (2.37× — exp 0002)** |
| 1b | Webhook delivery throughput *(surfaced by exp 0001)* | Concurrent I/O-bound delivery, backpressure on a side-channel, decoupling fan-out from job completion | **done ✅ (exp 0003)** |
| 2 | [Queue-depth autoscaling](track-02-handoff.md) | Backpressure, control loops, Little's Law, SLO-driven capacity | **next — P1 (unblocked by T1)** |
| 3 | [End-to-end tracing, SLOs & local load testing](track-03-observability-and-load.md) | Context propagation across async boundaries, the three pillars, SLO/SLI/error budgets, load-test methodology | **done ✅** |
| 4 | [Multi-tenancy, auth & quotas](track-04-multitenancy-auth.md) | AuthN vs AuthZ, key rotation, the idempotency pattern, tenant isolation | planned |
| 5 | [Production ingestion pipeline](track-05-ingestion.md) | Resumable/multipart uploads, pipeline stages, defense-in-depth, trust boundaries | planned |
| 6 | [Adaptive streaming + CDN](track-06-adaptive-streaming.md) | ABR streaming, CDN cache/invalidation, edge auth, encoding cost/quality tradeoffs | planned |
| 7 | [Data layer at scale](track-07-data-layer.md) | Table partitioning, CDC vs polling, index design under write load | **deferred — rescope to `webhook_deliveries`** |
| 8 | [Resilience & correctness verification](track-08-resilience.md) | Failure-mode analysis, exactly-once in practice, replay attacks, chaos engineering | planned |

> Track 3 is the only track with a full design doc checked in, because it was built
> first. Now that it's done, every track below is **measurable**: implement, re-run
> the same k6 profile, compare dashboards, and record an `experiments/NNNN-*.md`
> writeup. "It scales" is a claim we can prove, not assert.

## Recommended sequence (re-prioritized from exp 0001 data)

1. **~~Track 1 — concurrent worker + DLQ + stream recovery.~~ ✅ DONE.**
   Was the P0 throughput ceiling (μ ≈ 1.1 jobs/s). Now a bounded pool: **measured
   2.37×** (0.73 → 1.73 jobs/s), multi-core, 100% success, live DLQ. See
   `experiments/0002`. Lesson: set `MAX_CONCURRENT_JOBS ≈ cores`.
2. **~~Track 1b — webhook delivery throughput.~~ ✅ DONE.** Concurrent fan-out +
   wired `webhook_delivery_*` metrics + transport tuning. Not the bottleneck at
   local scale (kept up serially), so the win is observability + headroom; see
   `experiments/0003`. To prove it under stress, re-run with a latency-bearing
   receiver.
3. **Track 2 — autoscaling. ← NEXT.** Now unblocked: there is finally a concurrent
   worker to scale. Drive worker replica count off the queue-lag signal we already
   expose (KEDA; k8s manifests exist). *Verify:* a backlog → scale-up → drain cycle.
   Carry the Track 1 lesson forward — each replica runs its own pool, so set
   `MAX_CONCURRENT_JOBS ≈ cores-per-pod` and scale *pods*, not threads. See
   [`track-02-handoff.md`](track-02-handoff.md).
4. **Track 4 — multi-tenancy + idempotency + auth.** The leap to "real users".
5. **Track 6 — adaptive streaming + CDN.** The headline product feature.
6. **Track 5 — ingestion.** Includes the small wins exp 0001 surfaced: abandoned-upload
   lifecycle (~50% orphaned `media/raw/`) and the `/complete` MinIO-HEAD tail.
7. **Track 7 — data layer.** **Deferred and rescoped.** DB/outbox have headroom today;
   revisit when volume justifies, scoped first to `webhook_deliveries` churn (the one
   polled table that actually strained) rather than blanket partitioning.
8. **Track 8 — resilience & correctness.** Depth once the throughput tracks land.

> **~~Track 3 follow-ups~~ ✅ DONE** (folded into Track 1b): `webhook_delivery_*`
> metrics wired, `db.query.duration` fine-bucket view added, `storage_operation_*`
> confirmed already recorded. Histogram-bucket standardization remains a watch-item
> when reading p95 across a deploy window.

---

## Track catalog (summaries)

### Track 1 — Concurrent worker + proper stream recovery + DLQ
**Gap:** one job at a time; a 3s video blocks a 200ms thumbnail. Recovery scans
Postgres and re-`XADD`s instead of using consumer-group delivery state.
**Move:** bounded worker pool (process pool for CPU-bound ffmpeg/Pillow vs async
for I/O — *choosing which is the lesson*); honour `MAX_CONCURRENT_JOBS` as a
semaphore; `XAUTOCLAIM`/`XPENDING` to reclaim dead-consumer messages; a
**dead-letter stream** for messages past the attempt cap; priority lanes so small
jobs don't queue behind large transcodes.
**Teaches:** thread vs process vs async, the GIL, CPU vs I/O bound, at-least-once
recovery, poison-message handling, head-of-line blocking.

### Track 1b — Webhook delivery throughput *(surfaced by exp 0001)*
**Gap:** the dispatcher polls every 2s, batch 50, and delivers webhooks with
*synchronous* HTTP + retries on a single loop. Each job emits 3 events
(`job.starting/started/done`), so insertion rate ≫ delivery rate — the load test
drove `webhook_pending` to ~5,900 with no recovery. Delivery is also under-
instrumented: `webhook_delivery_total/duration/failures` are defined but never
recorded, so only the `pending` gauge revealed the backlog.
**Move:** a bounded pool of concurrent delivery workers (I/O-bound → async/threads
fits); decouple fan-out from job completion; wire the delivery metrics + a
delivery-latency SLI. Optionally move webhook rows onto their own stream consumer
rather than a DB poll.
**Teaches:** concurrency for I/O-bound work, backpressure on a side-channel,
decoupling producers from slow consumers, instrumenting before optimizing.

### Track 2 — Queue-depth autoscaling
**Gap:** static worker count; bursts grow latency unbounded, idle wastes capacity.
**Move:** expose stream lag + oldest-message-age (extend the existing relay-lag
metric); drive **KEDA** (k8s manifests already exist) to scale workers on lag;
load-test the backlog → scale → drain cycle.
**Teaches:** backpressure, control loops, latency- vs queue-depth-based scaling,
Little's Law (L = λW), capacity planning.

### Track 4 — Multi-tenancy, auth & quotas
**Gap:** homegrown AES token (no expiry/rotation, shared key with webhook secrets);
single bucket, path-prefixed; no idempotency keys (retried presign = duplicate asset).
**Move:** OIDC/JWT (asymmetric keys, expiry, JWKS rotation) or scoped API keys;
separate webhook-signing secret; org→project→asset model with repository-layer
row scoping and per-tenant storage prefixes/credentials; **idempotency keys** on
`presign`/`complete`; per-tenant **quotas + rate limits** with usage accounting.
**Teaches:** authN vs authZ, key management/rotation, the idempotency pattern,
tenant isolation, security blast-radius.

### Track 5 — Production ingestion pipeline
**Gap:** single presigned `PUT`, 500MB cap, no resumability, MIME-only validation,
no scanning. Plus (from exp 0001) **no lifecycle for abandoned uploads** — ~50% of
presigns never complete, orphaning `media/raw/` objects.
**Move:** S3 **multipart/resumable** uploads with part-level retry; a validation
stage with real content sniffing (`python-magic` is already a dep); optional
**ClamAV** malware scan as a stage; dedup *before* full download via verified
client-supplied hash; a TTL/lifecycle sweep for un-completed raw uploads.
**Teaches:** large-file transfer, pipeline/stage design, defense-in-depth, trust
boundaries (never trust client content-type).

### Track 6 — Adaptive streaming + CDN
**Gap:** one fixed 720p MP4 at hardcoded 2500kbps, served straight from MinIO.
**Move:** generate an **HLS/DASH adaptive ladder** (multiple renditions + manifest —
`variants.video.manifest_url` already exists in the schema); serve via **CDN** with
signed URLs + cache-control; content-aware/per-title encoding decisions.
**Teaches:** adaptive bitrate streaming, CDN cache strategy + invalidation, edge
signed-URL access control, encoding cost/quality tradeoffs.

### Track 7 — Data layer at scale *(deferred — see exp 0001)*
**Gap:** `jobs`, `event_outbox`, `webhook_deliveries` polled and growing. The load
test showed the DB and outbox have **headroom today** (18 ms mean query, 5/25
connections, 0 outbox backlog), so blanket partitioning is premature — but
`webhook_deliveries` is the one table that genuinely strained.
**Move:** start narrow — partition/clean `webhook_deliveries` and replace its poll
with `LISTEN/NOTIFY` or a stream consumer (overlaps Track 1b). Broaden to the other
tables (monthly partitions; drop instead of DELETE; read replicas; CDC) only when
volume justifies it.
**Teaches:** partitioning, CDC vs polling, write-heavy index design, pool sizing.

### Track 8 — Resilience & correctness verification
**Gap:** unit + integration tests exist, but no proof of survival under failure/load.
**Move:** **fault injection / chaos** (kill the worker mid-transcode, pause Redis,
fill the disk — verify processed-once holds); **load tests** with latency budgets in
CI; **webhook contract tests** + replay protection (sign a timestamp, reject stale
deliveries — today a captured payload replays forever).
**Teaches:** failure-mode analysis, exactly-once vs at-least-once in practice,
replay attacks, reliability as a tested property.
