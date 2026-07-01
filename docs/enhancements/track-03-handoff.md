# Track 3 — Session Handoff (start here)

**Purpose:** everything a fresh conversation needs to begin **Track 3
(end-to-end tracing, SLOs & local load testing)** without prior context. Read
this top to bottom, then open `track-03-observability-and-load.md` for the full
design and phased plan. This doc is the *operational* companion: where things
are, how to run them, and the landmines already discovered.

---

## 1. What MPiper is (60-second orientation)

A media-processing pipeline: a **Go API** (`cmd/server`, `internal/`) accepts
uploads and a **Python worker** (`worker/`) processes them. They communicate over
**Redis Streams** (`media:jobs`). **Postgres** is the durable source of truth;
**MinIO** (S3-compatible) stores objects. Webhooks notify clients of job
lifecycle events.

**Asset flow:**
`POST /api/v1/storage/presign` → client `PUT`s file to MinIO →
`GET /api/v1/assets/{id}/complete` (writes asset `uploaded` + job + outbox row +
`job.starting` webhook rows in one tx) → **outbox relay** (1s poll) publishes to
Redis → **worker** consumes → image (3 webp variants) or video (poster + 720p +
preview) → variants written to MinIO + Postgres, asset `ready` → worker inserts
`job.started`/`job.done` webhook rows → **dispatcher** (2s poll) delivers signed POSTs.

---

## 2. The Track 3 goal in one sentence

Make one **trace per asset** that spans API → Redis → worker → ffmpeg (so queue
wait and per-stage time are visible), define a small set of **SLOs**, and build a
**local k6 load harness** + Grafana dashboards so we can saturate the system on a
laptop and *see* where it bends. Full plan: `track-03-observability-and-load.md`.

---

## 3. Current telemetry state (verified in code)

- **Go API:** OTel **traces + metrics**, exported OTLP to `otel-collector:4317`.
  Tracer init in `internal/metrics/otel.go`; metric instruments in
  `internal/metrics/metrics.go`.
- **Python worker:** OTel **metrics only** (`worker/utils/metrics.py`, OTLP to
  `otel-collector:4317`). **No tracer, no span creation, no context extraction.**
- **The gap:** the Go side traces the HTTP request and `Enqueue`, but **never
  injects a `traceparent`** into the Redis message or the outbox row. The worker
  therefore starts fresh with no parent. The trace dies at the queue boundary.
- **Observability stack** (`docker-compose.observability.yml`, configs in
  `observability/`): OTel Collector (bridges `mpiper_net` ↔ `mpiper_obs_net`),
  **Tempo** (traces), **Prometheus** (metrics), **Loki + Promtail** (logs),
  **Grafana** (dashboards, anonymous admin). Collector pipeline: OTLP receiver →
  Tempo (traces) + Prometheus exporter `:8889` (metrics).

> Note: `CLAUDE.md` historically said the worker is "prometheus_client (not OTel)"
> — that's **stale/wrong**; the worker uses OTel metrics. Don't trust that line.

---

## 4. Exact engineering targets for Phase 1 (close the trace gap)

These are the precise seams to touch. Verify each before editing.

**Inject context (Go):**
- `internal/queue/queue.go` — `RedisQueue.Enqueue` builds the stream message
  (a `map`); the worker reads its fields. Inject `traceparent` (and `tracestate`)
  here using the OTel propagator, as top-level message field(s).
- `internal/outbox/relay.go` — `tick()` unmarshals the outbox row payload and calls
  `queue.Enqueue(ctx, payload)`. Because enqueue is **store-and-forward**, the
  trace context must survive in the **outbox row** too: capture it when the row is
  written in `internal/service/asset.go` (`MarkAssetUploaded`, the
  `outboxRepo.InsertTx` call), persist it (extend `internal/models/outbox.go` +
  `internal/repository/outbox_repo.go` + a migration), and re-inject on relay.
- **Verify the global propagator is set** in `internal/metrics/otel.go`
  (`otel.SetTextMapPropagator(propagation.TraceContext{})`). If missing, add it —
  without it, injection is a no-op.

**Extract + continue (Python):**
- Add `worker/utils/tracing.py` mirroring `worker/utils/metrics.py` (tracer init,
  OTLP exporter to the same endpoint). Find where `init_metrics(...)` is called and
  init the tracer alongside it (same lifecycle).
- `worker/consumer/consumer.py` — in `consume()`, after the message payload is
  normalized (note: a `body` field, if present, is JSON-decoded and merged), read
  `traceparent` and start the consumer span. Use a **child span with a link** to
  the producer context (link is the correct primitive for queue fan-in; child span
  keeps the Tempo waterfall readable).

**Span the stages (Phase 2):**
- `worker/processing/processor.py` — `process_asset_dispatch` (download, dedup check).
- `worker/processing/images.py` — per-variant encode/upload.
- `worker/processing/videos.py` — `run()` wraps each ffmpeg call (poster / transcode_720p / preview).
- Stamp `trace_id`/`span_id` into worker + API structured logs for Loki↔Tempo linking.

**Message format reminder:** the consumer accepts either `job_id` (canonical) or
`asset_id`. The outbox payload (built in `asset.go`) currently carries `job_id`,
`asset_id`, `event`, `timestamp`. Add trace context as additional field(s); don't
break the existing keys.

---

## 5. Environment & topology facts (host = macOS)

**Host ports → containers:**
| Service | Host | Container | Notes |
|---|---|---|---|
| API | 5010 | 5010 | `/healthz`, `/api/v1/...` |
| Postgres | 5433 | 5432 | user `mpiper`, db `mpiper`, pw `changeme` |
| Redis | 6380 | 6379 | stream `media:jobs`, group `worker-group` |
| MinIO API | 9000 | 9000 | bucket `mpiper` (anon download on) |
| MinIO console | 9001 | 9001 | minioadmin / minioadmin |
| Grafana | 3000 | 3000 | anon admin |
| Prometheus | 9090 | 9090 | |
| Tempo | 3200 | 3200 | OTLP in on 4317/4318 (obs net) |
| webhook-receiver | 8888 | 8080 | overlay only |

**Container names:** `mpiper-api`, `mpiper-worker`, `mpiper-postgres`,
`mpiper-redis`, `mpiper-minio`, `mpiper-webhook-receiver`, `mpiper-otel-collector`,
`mpiper-tempo`, `mpiper-prometheus`, `mpiper-grafana`, `mpiper-loki`.

**Storage split endpoint (implemented):** `S3_ENDPOINT_URL=http://minio:9000`
(internal I/O) vs `S3_PUBLIC_ENDPOINT_URL=http://localhost:9000` (presigned +
public URLs). Don't undo this — host-run load tests depend on it.

**Telemetry env (`.env.local`):** `OTEL_EXPORTER_OTLP_ENDPOINT=otel-collector:4317`,
`OTEL_TLS_INSECURE=true`, `TRACE_SAMPLING_RATE` (default 0.1 in code — **set to
1.0 locally** so every asset traces). `ENCRYPTION_KEY=0123456789abcdef0123456789abcdef`
(32 bytes; used for auth tokens AND webhook secrets).

---

## 6. Runbook / command cheat sheet

```bash
# Bring up core + observability (+ webhooks if you want webhook traces too)
docker compose -f docker-compose.yml -f docker-compose.observability.yml up -d --build
# add: -f docker-compose.webhooks.yml   (for webhook receiver)

# End-to-end smoke (host-run; image + video + webhooks; 23 checks)
./scripts/demo-e2e.sh

# Go: build / vet / tests  (tests/performance_suite_test.go FAILS unless PERF_TEST_URL set — ignore)
go build ./... && go vet ./... && go test ./...

# Worker tests: the local .venv (py3.14) LACKS psycopg_pool/pytest/cryptography.
# Run them INSIDE the worker container instead:
docker exec -w /app mpiper-worker python -m unittest discover -s worker/tests -p 'test_*.py' -v

# Mint an auth token from the host (system python3 has `cryptography`; venv does not):
TOKEN=$(python3 - <<'PY'
import base64, os
from cryptography.hazmat.primitives.ciphers.aead import AESGCM
key=b"0123456789abcdef0123456789abcdef"; nonce=os.urandom(12)
print(base64.urlsafe_b64encode(nonce+AESGCM(key).encrypt(nonce,b"demo-user",None)).rstrip(b"=").decode())
PY
)

# Inspect DB
docker exec mpiper-postgres psql -U mpiper -d mpiper -c "SELECT asset_id,status,type FROM assets ORDER BY created_at DESC LIMIT 5;"

# Reset all state (assets/variants/objects accumulate across runs)
docker compose -f docker-compose.yml -f docker-compose.observability.yml down -v

# UIs: Grafana http://localhost:3000 · Prometheus http://localhost:9090 · Tempo via Grafana Explore
```

---

## 7. Landmines (things that already bit, or will)

- **Worker is single-threaded.** `MAX_CONCURRENT_JOBS` is in `worker/consumer/
  config.py` but **never used**; `consume()` does `count=1` and processes inline.
  This is the expected bottleneck Phase 5 should prove — don't "fix" it in Track 3.
- **Recovery is homegrown.** A 2-min DB scan re-`XADD`s stale jobs; no
  `XPENDING`/`XAUTOCLAIM`; poison messages are marked `failed` and dropped (no DLQ).
  That's Track 1, not Track 3.
- **Global propagator may be unset** in Go — injection silently no-ops without it. Check first.
- **Sampling.** Code default `TRACE_SAMPLING_RATE=0.1`. Set 1.0 locally or you'll
  lose most traces and think propagation is broken.
- **Dedup hides work.** Identical fixtures dedup after the first asset → near-zero
  work on repeats. The load harness must **fan out unique bytes** to measure real
  per-job cost.
- **Cardinality.** asset_id is fine as a *trace/span attribute*; **never** put it on
  a *metric* label.
- **Health check.** `cmd/server --health-check` is now a real `/healthz` probe
  (was previously booting a second server and failing to bind 5010 → api unhealthy
  → worker wouldn't start). If you change startup, keep that path lightweight.
- **Rebuild after code changes.** api/worker run from built images:
  `docker compose ... build api worker && docker compose ... up -d`.
- **Local ≠ prod.** Trust bottleneck *location* and before/after deltas, not
  absolute throughput numbers.

---

## 8. Suggested first-session scope

Do **Phase 0 + Phase 1** together (highest value, gets a real cross-boundary trace fast):

1. **Phase 0:** add `deploy.resources.limits` (cpu/mem) to `api` + `worker` in a
   compose overlay; set `TRACE_SAMPLING_RATE=1.0`; bring up with the observability
   overlay; capture a baseline `demo-e2e.sh` run and confirm spans land in Tempo.
2. **Phase 1:** Go `traceparent` injection (enqueue + outbox row + migration) →
   worker tracer + extraction in `consume()`. 

**Acceptance:** open one asset in Grafana/Tempo and see a single trace from
`POST /storage/presign` through `enqueue` → (visible queue-wait gap) → worker
`consume` span. That alone is a satisfying, demoable win.

Then continue with Phases 2–5 from the design doc (pipeline spans + log
correlation → SLO recording rules + dashboards → k6 harness → first experiment
writeup that names the worker bottleneck, feeding Track 1).

---

## 9. Repo / git state at handoff

- Branch: `feat/webhook-notifications`; open **PR #18**.
- Demo-readiness + split-endpoint work is committed (`9404c7a`) and pushed.
- `docs/enhancements/` (this file + `README.md` + `track-03-observability-and-load.md`)
  may be **uncommitted** — commit them at the start of the Track 3 session.
- Key docs to read: `docs/enhancements/README.md` (catalog),
  `track-03-observability-and-load.md` (plan), `docs/arch/*` (existing outbox/
  reliability design notes), `CLAUDE.md` (repo conventions; note the stale worker-
  telemetry line).
