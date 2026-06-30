# MPiper Enhancements — Roadmap

This directory tracks the work that takes MPiper from a well-built side project
to a production-grade media platform. Each **track** is chosen to teach a
distinct, transferable systems-engineering concept *and* to add real product
value — not feature-padding.

The philosophy: **write a design doc per track before coding** (problem, options,
decision, tradeoffs, how success is measured), and pair every track with a
load test or chaos experiment so each claim ("now it scales", "now it's
exactly-once") is *demonstrated*, not assumed.

## Where we are today

A clean, correct, **single-tenant, best-effort, single-node-throughput** pipeline
with good bones:

- Transactional enqueue via an **outbox relay** (Postgres → Redis Streams).
- An **idempotent-ish consumer** with content-hash dedup.
- **Presigned uploads** with a split internal/public storage endpoint.
- **Webhooks** with HMAC signing + exponential backoff.
- **OTel metrics** on both API and worker; a bundled Grafana/Tempo/Loki/Prometheus stack.

Known seams where "side project" becomes "system" (verified in code):

- The worker loop is **single-threaded** — `MAX_CONCURRENT_JOBS` exists in config
  but is never used; `consume()` pulls one message (`count=1`) and processes it inline.
- Recovery is a **homegrown DB-scan + re-`XADD`** every 2 min, not Redis Streams'
  own `XPENDING`/`XAUTOCLAIM` consumer-group recovery; poison messages are marked
  `failed` and dropped (no dead-letter stream).
- The **distributed trace breaks at the Redis boundary**: the API traces `Enqueue`
  but never injects a `traceparent`; the worker has OTel *metrics* but **no tracing**.
- Raw uploads in `media/raw/` are **never deleted** after processing (no lifecycle).
- Auth is a homegrown AES-GCM token with **no expiry/rotation**, and the same
  `ENCRYPTION_KEY` signs both auth tokens and webhook secrets.
- High-churn tables (`jobs`, `event_outbox`, `webhook_deliveries`) are **polled**
  and grow unbounded (cleanup-by-retention only; no partitioning).

## Tracks

| # | Track | Core systems lesson | Status |
|---|-------|---------------------|--------|
| 1 | [Concurrent worker + proper stream recovery + DLQ](track-01-concurrent-worker.md) | Concurrency models, at-least-once recovery, poison-message handling, head-of-line blocking | planned |
| 2 | [Queue-depth autoscaling](track-02-autoscaling.md) | Backpressure, control loops, Little's Law, SLO-driven capacity | planned |
| 3 | [End-to-end tracing, SLOs & local load testing](track-03-observability-and-load.md) | Context propagation across async boundaries, the three pillars, SLO/SLI/error budgets, load-test methodology | **planning (next)** |
| 4 | [Multi-tenancy, auth & quotas](track-04-multitenancy-auth.md) | AuthN vs AuthZ, key rotation, the idempotency pattern, tenant isolation | planned |
| 5 | [Production ingestion pipeline](track-05-ingestion.md) | Resumable/multipart uploads, pipeline stages, defense-in-depth, trust boundaries | planned |
| 6 | [Adaptive streaming + CDN](track-06-adaptive-streaming.md) | ABR streaming, CDN cache/invalidation, edge auth, encoding cost/quality tradeoffs | planned |
| 7 | [Data layer at scale](track-07-data-layer.md) | Table partitioning, CDC vs polling, index design under write load | planned |
| 8 | [Resilience & correctness verification](track-08-resilience.md) | Failure-mode analysis, exactly-once in practice, replay attacks, chaos engineering | planned |

> Only the catalog summaries live here for tracks 1–2 and 4–8 (see sections below).
> Track 3 has a full design doc because it's the one we build first — everything
> else becomes measurable once it lands.

## Recommended sequence

1. **Track 3 (tracing / SLOs / load testing)** — you can't improve what you can't
   see, and it makes every later track measurable. **Do this first.**
2. **Track 1 (concurrency + DLQ + stream recovery)** — richest single source of
   systems lessons; self-contained.
3. **Track 2 (autoscaling + load tests)** — prove the concurrency work under burst.
4. **Track 4 (multi-tenancy + idempotency + auth)** — the leap to "real users".
5. **Track 6 (adaptive streaming + CDN)** — the headline product feature.
6. **Tracks 5, 7, 8** — depth wherever you want to go deeper.

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
no scanning.
**Move:** S3 **multipart/resumable** uploads with part-level retry; a validation
stage with real content sniffing (`python-magic` is already a dep); optional
**ClamAV** malware scan as a stage; dedup *before* full download via verified
client-supplied hash.
**Teaches:** large-file transfer, pipeline/stage design, defense-in-depth, trust
boundaries (never trust client content-type).

### Track 6 — Adaptive streaming + CDN
**Gap:** one fixed 720p MP4 at hardcoded 2500kbps, served straight from MinIO.
**Move:** generate an **HLS/DASH adaptive ladder** (multiple renditions + manifest —
`variants.video.manifest_url` already exists in the schema); serve via **CDN** with
signed URLs + cache-control; content-aware/per-title encoding decisions.
**Teaches:** adaptive bitrate streaming, CDN cache strategy + invalidation, edge
signed-URL access control, encoding cost/quality tradeoffs.

### Track 7 — Data layer at scale
**Gap:** `jobs`, `event_outbox`, `webhook_deliveries` polled and growing; 1s outbox
poll is fine at low volume, a thundering problem at high volume.
**Move:** time-**partition** high-churn tables (monthly partitions; drop instead of
DELETE); `LISTEN/NOTIFY` or logical-replication CDC to replace polling; read
replicas for the query path; load-test where 1s polling falls over.
**Teaches:** partitioning, CDC vs polling, write-heavy index design, pool sizing.

### Track 8 — Resilience & correctness verification
**Gap:** unit + integration tests exist, but no proof of survival under failure/load.
**Move:** **fault injection / chaos** (kill the worker mid-transcode, pause Redis,
fill the disk — verify processed-once holds); **load tests** with latency budgets in
CI; **webhook contract tests** + replay protection (sign a timestamp, reject stale
deliveries — today a captured payload replays forever).
**Teaches:** failure-mode analysis, exactly-once vs at-least-once in practice,
replay attacks, reliability as a tested property.
