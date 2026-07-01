# Track 1 + 1b — Session Handoff (start here)

**Purpose:** everything a fresh conversation needs to begin **Track 1 (concurrent
worker + stream recovery + DLQ)** and **Track 1b (webhook delivery throughput)**
without prior context. Read this top to bottom, then open
`track-01-concurrent-worker.md` for the full design. This doc is the *operational*
companion: where things are, how to run them, what the baseline is, and the
landmines already discovered. It assumes **Track 3 is done** — tracing, SLOs,
dashboards, and the k6 harness all exist, so every change here is measurable.

---

## 1. What MPiper is (60-second orientation)

A media-processing pipeline: a **Go API** (`cmd/server`, `internal/`) accepts
uploads and a **Python worker** (`worker/`) processes them. They communicate over
**Redis Streams** (`media:jobs`, group `worker-group`). **Postgres** is the
durable source of truth; **MinIO** (S3-compatible) stores objects. Webhooks notify
clients of job lifecycle events.

**Asset flow:**
`POST /api/v1/storage/presign` → client `PUT`s file to MinIO →
`GET /api/v1/assets/{id}/complete` (writes asset `uploaded` + job + outbox row +
`job.starting` webhook rows in one tx) → **outbox relay** (1s poll) publishes to
Redis → **worker** consumes → image (3 webp variants) or video (poster + 720p +
preview) → variants written to MinIO + Postgres, asset `ready` → worker inserts
`job.started`/`job.done` webhook rows → **webhook dispatcher** (2s poll) delivers
signed POSTs.

---

## 2. The goals in one sentence each

- **Track 1:** make the worker process **N jobs concurrently** (it does 1 today),
  recover dead-consumer messages with **Redis Streams' own `XPENDING`/`XAUTOCLAIM`**
  instead of a DB scan, and route poison messages to a **dead-letter stream** — so
  the worker's service rate scales with cores and a single bad/large job can't stall
  the pipeline.
- **Track 1b:** make the **webhook dispatcher deliver concurrently** (it delivers
  serially today) and **wire its delivery metrics**, so `webhook_pending` drains
  instead of growing unboundedly.

Both are throughput fixes for the two bottlenecks the Track 3 load test proved.

---

## 3. The baseline to beat (exp 0001, verified)

From `experiments/0001-worker-saturation.md` (open model, `--rate 10/s`, worker
pinned to 1 CPU). Re-run the **same** profile after each track and compare.

| Signal | Baseline | Target after the track |
|---|---|---|
| Worker service rate μ | **~1.1 jobs/s** | scales ~N× with the pool (until CPU-bound) |
| Worker CPU | **98%** (1 core, pegged) | utilizes all allotted cores |
| Peak queue depth | **2,544 and growing** | stabilizes (drains at λ ≤ μ) |
| Asset proc p50 / mean | **0.86 s / 1.76 s** | unchanged per-job; throughput is the win |
| `webhook_pending` peak | **~5,901, never drains** | drains to ~0 (Track 1b) |
| Job success rate | 100% | stays 100% (no double-processing) |
| API presign p95 / complete p99 | 48 ms / 358 ms | unaffected (API isn't the bottleneck) |
| DB | 18 ms mean, 5/25 conns, 0 waits | watch pool as worker concurrency rises |

> **Watch the DB pool** as you add worker concurrency: N concurrent jobs × the
> per-job DB calls will raise in-use connections. The new `mpiper_db_connections_*`
> gauges (added during Track 3 follow-up) will show it.

---

## 4. Exact engineering targets — Track 1 (worker)

Verify each before editing.

**The single-threaded loop:**
- `worker/consumer/main.py` `main()` — the loop is `while not shutdown: processed =
  consumer.consume(stream); if not processed: sleep(job_poll_interval)`. One message
  at a time, inline.
- `worker/consumer/consumer.py` `consume()` — `xreadgroup(..., count=1, block=5000)`,
  then dispatches inline via `_handle_job` / `_handle_asset_message`.
- `worker/consumer/config.py` — `max_concurrent_jobs` (`MAX_CONCURRENT_JOBS`, default
  5) **exists but is never used**. This is the semaphore size to honour.

**Concurrency model (this choice *is* the lesson):**
- Work is **CPU-bound**: Pillow (`images.py`) and ffmpeg (`videos.py`, via
  `subprocess`). ffmpeg runs in a separate process (true parallelism regardless of
  the GIL); Pillow releases the GIL for most ops. So a **thread pool** may suffice,
  but a **process pool** gives guaranteed parallelism for the Python-side work.
  Decide and document the tradeoff (GIL, memory, startup cost, picklability).
- Read `count=N` (or keep `count=1` and dispatch to a bounded pool); cap in-flight at
  `MAX_CONCURRENT_JOBS`.

**Invariants that MUST survive concurrency:**
- **Per-message ack.** Today `consume()` acks after the single job. With a pool,
  track `msg_id` per task and `XACK` only that message on its own success; leave
  failed ones unacked (they stay in the PEL for recovery).
- **Idempotent claim.** `_handle_job` claims a job with `SELECT ... FOR UPDATE` and
  checks `status == 'done'`. Concurrent consumers must each claim distinct rows;
  don't weaken the row lock. Content-hash dedup (`check_for_duplicate`) also guards
  double work.
- **Asset state ownership.** `_handle_job` (not the processor) owns the
  `failed`/`ready` transition — preserve that (see DEV-34 comment).
- **Tracing.** The `worker.consume` span + pipeline spans must be started **inside
  each task**, carrying that message's extracted `traceparent` (see
  `_consume_span`). Don't share one span across concurrent jobs or the Tempo
  waterfalls will merge.
- **Per-job metrics.** `wm.record_job` / `wm.record_asset` are already called; keep
  them per-task (asset_type label only — never asset_id on a metric).

**Recovery — replace the homegrown scan:**
- `consumer.py` `_recover_stuck_pending()` does a DB scan
  (`status IN ('pending','in_progress') AND updated_at < now() - interval '2 minutes'`)
  and re-`XADD`s. Replace with **`XAUTOCLAIM`** (or `XPENDING` + `XCLAIM`) on
  `media:jobs` / `worker-group` to reclaim messages idle past a threshold from dead
  consumers — using the stream's own delivery state. Keep it time-gated like
  `_maybe_recover()`.

**Dead-letter queue:**
- Today poison messages are marked `failed` and the Redis message is dropped (acked
  or abandoned). Add a **dead-letter stream** (e.g. `media:jobs:dlq`): when a job
  exceeds `cfg.redis.max_retries`, `XADD` the message (with failure metadata) to the
  DLQ and `XACK` the original, instead of silently dropping. Lets you inspect/replay.

**Head-of-line blocking (optional, in the design):**
- A 60s video blocks short thumbnails behind it. Consider **priority lanes** (e.g.
  separate streams or a priority field) so small jobs don't queue behind large
  transcodes.

---

## 5. Exact engineering targets — Track 1b (webhook dispatcher)

**The serial delivery loop:**
- `internal/webhook/dispatcher.go` `tick()` fetches a batch with
  `... FOR UPDATE OF wd SKIP LOCKED LIMIT $BatchSize`, then **delivers them one at a
  time** in `for _, row := range rows { d.deliver(ctx, row) }`. Each `deliver()` is a
  synchronous HTTP POST with `d.client.Timeout`. **This serial loop is the
  bottleneck.**
- Config: `WEBHOOK_POLL_INTERVAL` (2s), `WEBHOOK_BATCH_SIZE` (50), `WEBHOOK_TIMEOUT`
  (10s), `WEBHOOK_MAX_ATTEMPTS` (5) — in `internal/config/env.go`.

**The move:**
- Deliver the batch **concurrently** with a bounded pool (e.g. `errgroup` +
  semaphore, or a worker-pool of size `WEBHOOK_CONCURRENCY`). HMAC signing, backoff
  (`next_attempt_at`), and retry logic in `handleFailure`/`backoff` stay as-is.
- **Wire the metrics.** `WebhookDeliveryTotal`, `WebhookDeliveryDuration`,
  `WebhookDeliveryFailures` are **defined in `internal/metrics/metrics.go` but never
  recorded** in the dispatcher. `NewDispatcher(db, logger, cfg)` doesn't take
  `*metrics.Metrics` — extend it to accept `m`, record per delivery (labels:
  `event`, `status` — **never** asset_id), and pass `m` from `cmd/server/main.go`
  (where the dispatcher is constructed).
- The SLI rule `sli:webhook_delivery_latency_seconds:p95` already exists; it just
  needs the histogram to be recorded.

**Concurrency-safety note:** `tick()` runs `SELECT ... FOR UPDATE SKIP LOCKED`
**outside an explicit transaction** (`d.db.SelectContext`), so the row locks are
released as soon as the SELECT returns — fine for one dispatcher with internal
goroutines, but it does **not** prevent two *separate* dispatcher processes from
grabbing the same row. If you ever run >1 dispatcher, wrap the claim in a tx or add a
`claimed_at`/`locked_by` column. Document whichever you choose.

---

## 5b. Track 3 follow-ups to fold in (do these first; ~30 min)

These were flagged in `experiments/0001` and the roadmap; doing them first means the
0002/0003 experiments have clean, artifact-free numbers:

- **Wire `webhook_delivery_*` metrics** (part of Track 1b above).
- **Wire `storage_operation_*` metrics** (the `pkg/utils/storagex` layer doesn't
  record them; the `/complete` MinIO-HEAD cost is currently invisible).
- **Add a fine-bucket view for `db.query.duration`** in `internal/metrics/metrics.go`
  (it uses default coarse buckets, so its p95 reads ~4.75 s — an artifact; true mean
  is 18 ms). Mirror the existing `http`/`queue.processing.lag` views.
- **Standardize histogram buckets** across worker/API so p95s aren't distorted when
  old + new bucket boundaries mix in one query window (this bit the image-ready and
  enqueue-lag SLIs).

---

## 6. Environment & topology (host = macOS)

**Host ports → containers:**
| Service | Host | Notes |
|---|---|---|
| API | 5010 | `/healthz`, `/api/v1/...` |
| Postgres | 5433 | user `mpiper`, db `mpiper`, pw `changeme` |
| Redis | 6380 | stream `media:jobs`, group `worker-group` |
| MinIO API / console | 9000 / 9001 | bucket `mpiper`, minioadmin/minioadmin |
| Grafana | 3000 | anon admin; folder **MPiper** |
| Prometheus | 9090 | remote-write receiver enabled (for k6) |
| Tempo | 3200 | pinned `grafana/tempo:2.6.1` |
| OTel Collector | 8888/8889 | metrics; bridges `mpiper_net` ↔ `mpiper_obs_net` |

**Container names:** `mpiper-api`, `mpiper-worker`, `mpiper-postgres`,
`mpiper-redis`, `mpiper-minio`, `mpiper-otel-collector`, `mpiper-tempo`,
`mpiper-prometheus`, `mpiper-grafana`, `mpiper-loki`, `mpiper-promtail`.

**Compose overlays:** `docker-compose.yml` (core) + `docker-compose.observability.yml`
(Tempo/Prom/Loki/Grafana/collector) + `docker-compose.loadtest.yml` (CPU/mem pins +
`TRACE_SAMPLING_RATE=1.0`). `ENCRYPTION_KEY=0123456789abcdef0123456789abcdef`.

**Metric naming (important):** the collector's Prometheus exporter uses
`namespace: mpiper`. Go API instruments → `mpiper_http_server_request_duration_seconds_*`;
**worker instruments already carry a `mpiper.` prefix → double prefix**
`mpiper_mpiper_job_processing_success_total`, etc. k6 client metrics land under
`k6_*` (custom ones as `k6_mpiper_*`).

---

## 7. Runbook / command cheat sheet

```bash
# Bring up core + observability + loadtest pins (everything, rebuild):
docker compose -f docker-compose.yml -f docker-compose.observability.yml \
  -f docker-compose.loadtest.yml up -d --build

# Rebuild just api/worker after code changes:
docker compose -f docker-compose.yml -f docker-compose.observability.yml \
  -f docker-compose.loadtest.yml up -d --build api worker

# Worker unit tests — the image entrypoint boots the worker, so OVERRIDE it:
docker run --rm --entrypoint python -v "$PWD":/app -w /app mpiper-worker \
  -m unittest discover -s worker/tests -p 'test_*.py' -v

# Go: build / vet / test  (tests/performance_suite_test.go fails w/o PERF_TEST_URL — ignore)
go build ./... && go vet ./internal/... && go test ./internal/... ./pkg/...

# Load test (baseline profile to compare against exp 0001):
./loadtest/run.sh open --rate 10/s --duration 90s        # arrival > service
./loadtest/run.sh closed --vus 10 --duration 2m          # find max throughput

# Query Prometheus history (data persists across `down` WITHOUT -v):
#   Tempo retains traces 48h, Prometheus 30d. Instant queries only see the last
#   5 min, so for past runs wrap in last_over_time(metric[12h]) / max_over_time.

# Inspect Redis stream + consumer group / pending:
docker exec mpiper-redis redis-cli XINFO GROUPS media:jobs
docker exec mpiper-redis redis-cli XPENDING media:jobs worker-group

# Inspect webhook backlog:
docker exec mpiper-postgres psql -U mpiper -d mpiper -c \
  "SELECT status, count(*) FROM webhook_deliveries GROUP BY status;"

# UIs: Grafana http://localhost:3000 (Experiment Overview) · Prometheus :9090 · Tempo via Grafana Explore
```

---

## 8. Landmines (already bit, or will)

- **Worker tests:** the `mpiper-worker` image has an entrypoint that runs the worker;
  you MUST `--entrypoint python` to run unittest, else it tries to boot + migrate and
  hits the DB. The local `.venv` lacks deps — always test in the container.
- **Tracing under concurrency:** start the `worker.consume` span (and pipeline spans)
  *inside each task* with that message's context. Sharing context across goroutines/
  tasks will corrupt the per-asset waterfalls. Verify in Tempo after.
- **Ack discipline:** only `XACK` a message after *its* job succeeds. With a pool,
  don't ack by position — ack by `msg_id`.
- **Mixed histogram buckets:** changing bucket boundaries makes `histogram_quantile`
  over a window that spans the change produce garbage p95s. After re-instrumenting,
  either reset Prometheus data or wait for the old series to age out before reading.
- **DB pool pressure:** more concurrent jobs → more in-use connections. Pool max is
  25 (`mpiper_db_connections_max_open`). Watch `..._active` and `..._wait_count`.
- **Webhook `SKIP LOCKED` without a tx:** safe for single-dispatcher internal
  concurrency, NOT for multiple dispatcher processes (see §5).
- **Operational flakiness seen this session:** an aborted `compose up` (a stray
  `mpiper-webhook-receiver` on host :8888 collided with the collector) left
  containers with stale port publishing (`docker port` empty) and detached the
  collector from `mpiper_obs_net`. Fix = `up -d --force-recreate <svc>`. If telemetry
  "disappears," check the collector is on both networks and Prometheus targets are up.
- **k6:** no `TextEncoder` in its runtime (use charCodes); client metrics are prefixed
  `k6_`; remote-write target is `http://localhost:9090/api/v1/write`.
- **Dedup hides work:** the harness fans out unique bytes per iteration; keep that or
  repeat runs do near-zero work.
- **Don't put `asset_id` on a metric label** (high cardinality) — span attribute only.

---

## 9. Acceptance / how we'll know it worked

- **Track 1:** re-run `open --rate 10/s` → μ rises ~N× (pool size, until CPU-bound),
  queue depth **stabilizes/drains** instead of growing, job success stays 100% (no
  double-processing — verify via DB job counts and dedup). A killed-mid-job consumer's
  message is reclaimed by `XAUTOCLAIM`; a poison message lands in `media:jobs:dlq`.
  Write `experiments/0002-concurrent-worker.md` (before/after table + a trace).
- **Track 1b:** under the same load, `webhook_pending` **drains to ~0**; the new
  webhook delivery-rate and p95 panels populate; `sli:webhook_delivery_latency_seconds:p95`
  renders. Write `experiments/0003-webhook-throughput.md`.

Each writeup follows the `0001` template: setup (with resource pins) → method → before
numbers → the trace/dashboard evidence → conclusion. Local results are **relative** —
trust deltas and bottleneck location, not absolute throughput.

---

## 10. Repo / git state at handoff

- **Branch:** `feat/track-03-observability` (cut from `staging`), **10 commits**,
  Track 3 work committed (tracing, worker instrumentation, log correlation, metric
  fixes + DB pool gauges, observability infra, Grafana provisioning fix, dashboards,
  k6 harness, `experiments/0001`).
- **Uncommitted at handoff:** the roadmap README rewrite (`docs/enhancements/README.md`)
  and this handoff doc — commit them at the start of the Track 1 session
  (`docs(roadmap): mark Track 3 done, re-prioritize from exp 0001`).
- **Not pushed yet.** Decide whether to push `feat/track-03-observability` + open a PR
  against `staging` before branching for Track 1, or continue on the same branch.
- **Key reads:** `experiments/0001-worker-saturation.md` (the baseline),
  `docs/enhancements/README.md` (re-prioritized roadmap),
  `track-01-concurrent-worker.md` (full design — write it out before coding, per the
  per-track design-doc philosophy), and `track-03-handoff.md` (the doc that started
  the Track 3 session, for format).

---

## 11. Suggested first-session scope

Do the **§5b follow-ups + Track 1b first** (small, high-value, makes the next
experiment clean), then **Track 1**:

1. **Warm-up (§5b):** wire `webhook_delivery_*` + `storage_operation_*` metrics, add
   the `db.query.duration` view. *Demo:* those panels populate.
2. **Track 1b:** concurrent webhook delivery + pass `m` into the dispatcher. *Demo:*
   `webhook_pending` drains under load → `experiments/0003`.
3. **Track 1:** bounded worker pool honouring `MAX_CONCURRENT_JOBS` (pick process vs
   thread, document why), preserving ack/idempotency/tracing invariants. *Demo:* μ
   scales, queue stabilizes → `experiments/0002`.
4. **Then** `XAUTOCLAIM` recovery + DLQ stream, and (optional) priority lanes.

That order banks two quick, demoable wins (clean metrics + webhooks draining) before
the larger concurrency change, and every step is provable by re-running the existing
k6 profile against the Track 3 dashboards.
