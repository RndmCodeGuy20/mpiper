# MPiper Load Harness (k6) — Track 3, Phase 4

Drives the **real** client flow from the host (presign → PUT to MinIO →
complete), so the whole pipeline — API, outbox relay, Redis, worker, ffmpeg — is
exercised end-to-end and observable as one trace per asset.

## Install

```bash
brew install k6          # macOS
# or see https://grafana.com/docs/k6/latest/set-up/install-k6/
```

`run.sh` also needs `python3` with the `cryptography` package on the host (used
only to mint the AES-GCM auth token).

## Prerequisites

Bring the stack up **with the observability overlay** (so Prometheus accepts
k6's remote-write) and ideally the **loadtest overlay** (CPU-pinned, full
sampling) so runs are reproducible:

```bash
docker compose \
  -f docker-compose.yml \
  -f docker-compose.observability.yml \
  -f docker-compose.loadtest.yml \
  up -d --build
```

## Run

```bash
# CLOSED model — fixed VUs hammer the system (find max throughput / saturation)
./loadtest/run.sh closed --vus 10 --duration 2m
./loadtest/run.sh closed --vus 20 --duration 3m --ramp

# OPEN model — fixed arrival rate (find the latency knee; watch queue lag grow)
./loadtest/run.sh open --rate 5/s --duration 3m
./loadtest/run.sh open --rate 10/s --duration 3m --max-vus 400
```

Options: `--fixture PATH`, `--base-url URL`, `--no-prometheus`.

## A/B contrast (concurrent worker + webhooks)

The concurrency knobs live on `docker-compose.loadtest.yml` as env vars
(defaults reproduce the single-threaded baseline). Flip them on the **same
binary** — no new overlays, no code changes — to isolate the concurrency
variable at a fixed core budget:

```bash
CF="-f docker-compose.yml -f docker-compose.observability.yml -f docker-compose.loadtest.yml -f docker-compose.webhooks.yml"

# BEFORE — serial
WORKER_CPUS=4 MAX_CONCURRENT_JOBS=1 WEBHOOK_CONCURRENCY=1  docker compose $CF up -d --build
./loadtest/run.sh closed --vus 20 --duration 2m
./loadtest/run.sh capture "BEFORE serial (mcj=1, wc=1)"

# AFTER — concurrent (flip knobs, recreate worker+api, no rebuild)
WORKER_CPUS=4 MAX_CONCURRENT_JOBS=8 WEBHOOK_CONCURRENCY=10 docker compose $CF up -d --force-recreate worker api
./loadtest/run.sh closed --vus 20 --duration 2m
./loadtest/run.sh capture "AFTER concurrent (mcj=8, wc=10)"
```

`./loadtest/run.sh capture "label"` snapshots the headline signals (worker μ,
queue depth, webhook pending/rate/p95, DLQ depth, DB pool) from Prometheus —
run it right after each load run. Also grab `docker stats --no-stream
mpiper-worker` for worker CPU%.

> The default 1-CPU pin masks the worker win (threads can't exceed one core of
> CPU work), so the A/B uses `WORKER_CPUS=4` on **both** sides and the
> `closed` model to measure max sustained μ directly.

## What to watch

- **k6 terminal summary** — client-side request rate, error rate, and the custom
  trends (`mpiper_presign_latency_ms`, `mpiper_upload_latency_ms`,
  `mpiper_complete_latency_ms`). Thresholds map to the §4.2 SLOs and fail the run
  on breach (exit non-zero).
- **Grafana** (http://localhost:3000) — the Track 3 dashboards: API RED, the
  app-saturation/USE view (queue depth, in-flight, backlogs), the pipeline
  funnel, and queue health. In the open model, queue depth climbing while the
  API stays healthy is the worker bottleneck made visible.
- **Tempo** — click a latency exemplar on a histogram panel to jump straight to
  the trace for that asset and see which span dominates.

## Dedup fan-out

The worker dedups by content hash, so identical bytes do almost no work after
the first asset. The harness appends per-iteration unique bytes **after** the
JPEG end-of-image marker (decoders ignore trailing bytes), yielding a valid but
unique-hash image so every iteration costs real work. See `lib.js`.

## Caveat

Local results are **relative**: trust the bottleneck *location* and
before/after deltas, not absolute throughput. Always record the resource limits
(from `docker-compose.loadtest.yml`) with each experiment.
