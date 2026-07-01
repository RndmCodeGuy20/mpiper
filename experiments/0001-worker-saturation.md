# Experiment 0001 — Worker saturation under load

**Date:** 2026-06-30 · **Track:** 3 (observability & load) · **Feeds:** Track 1 (concurrent worker)
**Status:** complete

## Hypothesis

The Python worker is single-threaded (`consume()` reads one message, `count=1`,
and processes it inline; `MAX_CONCURRENT_JOBS` exists in config but is unused).
Under sustained upload load the worker — not the API — should be the bottleneck,
with the Redis stream growing without bound once arrival rate exceeds the
worker's service rate (Little's Law, `L = λW`).

## Setup (record this with every run)

- **Resource pinning** (`docker-compose.loadtest.yml`): `api` = 1.0 CPU / 512 MB,
  `worker` = **1.0 CPU** / 1 GB. The single-CPU pin makes the bottleneck a stable,
  observable fact rather than something that moves with spare laptop cores.
- **Sampling:** `TRACE_SAMPLING_RATE=1.0` (every asset traced).
- **Stack:** core + observability + loadtest overlays, all up.
- **Workload:** images only, unique bytes per iteration (dedup defeated — see
  `loadtest/lib.js`). Fixture `worker/tests/test_assets/image.jpg`, 3 webp
  variants per asset.
- **Profile:** open model, `./loadtest/run.sh open --rate 10/s --duration 60s`
  (fixed arrival rate; λ = 10 uploads/s).

> Local results are **relative**. Trust the bottleneck location and the
> before/after deltas, not the absolute throughput — laptop CPU, no network
> latency, single-node Redis/Postgres.

## Method (the loop)

1. Is an SLO breached? 2. USE — is the worker CPU- or queue-saturated?
3. Open an exemplar trace — which span dominates? 4. Form a hypothesis, change
one thing, re-run the **same** profile, compare.

## Results (before — no optimisation yet)

| Signal | Value | Source |
|--------|-------|--------|
| Arrival rate (λ) | 10.0 uploads/s | k6 `mpiper_assets_submitted` |
| Worker service rate (μ) | **1.13 jobs/s** | `rate(mpiper_mpiper_job_processing_success_total[2m])` |
| Mean asset processing time | 0.81 s | `…_duration_seconds_sum / …_count` |
| Queue depth before → after | 3985 → 4370 (↑) | `sli:queue_depth:current` |
| Worker CPU | **98.5 %** (pinned at 1 CPU) | `docker stats` |
| API CPU | 0.4 % | `docker stats` |
| Presign p95 (API) | 48 ms (SLO < 150 ms ✅) | `sli:presign_latency_seconds:p95` |
| Job success rate | 1.0 (SLO > 99 % ✅) | `sli:job_success_ratio:ratio_rate5m` |

**Reading:** λ (10/s) ≫ μ (1.13/s). The queue grows monotonically; the system is
**unstable for any arrival rate above ≈ 1.1 uploads/s**. The API is essentially
idle (0.4 % CPU, presign well inside SLO) while the worker is pinned at 98.5 %.
The bottleneck is unambiguously the worker, and specifically its
**single-threaded, one-job-at-a-time** processing loop — not CPU work that is
inherently slow (a single image is ~0.8 s), but the complete absence of
concurrency.

## Trace evidence (where the time goes)

With the trace gap now closed (Track 3, Phase 1), one asset is a single trace
from the API through the queue into the worker — example, 19 spans:

```
/api/v1/assets/{id}/complete                 (API HTTP request)
└ AssetHandler.MarkAssetUploaded
  └ AssetService.MarkAssetUploaded
    ├ StorageClient.GetObjectAttrs → S3.GetObjectAttrs
    └ Database.Transaction
      ├ AssetRepo.MarkAssetUploadedTx
      ├ AssetRepo.InsertProcessAssetJobTx
      └ OutboxRepo.InsertTx
        └ outbox.publish                       (relay re-activates stored context)
          └ RedisQueue.Enqueue                 (injects traceparent into the message)
            ├ RedisQueue.doXAddWithRetry
            └ worker.consume                   (── crosses the Redis boundary ──)
              └ process.dispatch
                ├ process.download
                ├ process.dedup_check
                └ image.variant × 3
```

The **gap between `RedisQueue.Enqueue` and `worker.consume`** is the queue wait —
the time an asset spends backed up behind the single worker. Under this profile
that gap dominates end-to-end latency, and it grows for every asset because the
backlog only ever increases. The in-worker stages (download, dedup, 3 variants)
are individually fast; the cost is waiting for a free worker, not the work itself.

## Conclusion

The single-threaded worker is the bottleneck, with a service rate of ~1.1
jobs/s. The pipeline cannot keep up with anything beyond a trickle of uploads,
and the deficit manifests as an unbounded Redis backlog and ever-growing
queue-wait latency — while the API and host CPU sit idle. This is the motivating
evidence for **Track 1 (concurrent worker + stream recovery + DLQ)**: honour
`MAX_CONCURRENT_JOBS` as a real concurrency limit (process pool for the
CPU-bound Pillow/ffmpeg work) so μ scales with available cores instead of being
fixed at one.

## Reproduce

```bash
docker compose -f docker-compose.yml -f docker-compose.observability.yml \
  -f docker-compose.loadtest.yml up -d --build
./loadtest/run.sh open --rate 10/s --duration 60s
# Grafana http://localhost:3000 → MPiper folder:
#   - "Worker / App Saturation (USE)": queue depth climbing, in-flight pinned
#   - "Pipeline Funnel": ready/s flat at ~1.1 while uploaded/s tracks arrival
# Tempo (Explore): TraceQL `{ name="worker.consume" }` → open one → see the
#   enqueue→consume queue-wait gap and the per-stage breakdown.
```

## Next experiment

After Track 1 lands a bounded worker pool, re-run this **exact** profile and
compare: μ should rise roughly with the pool size (until CPU-bound), queue depth
should stabilise instead of growing, and the enqueue→consume gap should shrink.
Record results as `0002-concurrent-worker.md`.
