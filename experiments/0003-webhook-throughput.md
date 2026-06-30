# Experiment 0003 — Webhook delivery throughput

**Date:** 2026-06-30 · **Track:** 1b (webhook throughput) · **Follows:** 0001
**Status:** implementation complete; **after-load numbers pending a live run** (see *Results (after)*).

## Hypothesis

The webhook dispatcher delivers serially: `tick()` claims a batch with
`FOR UPDATE … SKIP LOCKED LIMIT BatchSize` and then loops
`for _, row := range rows { d.deliver(ctx, row) }`, where each `deliver()` is a
synchronous HTTP POST bounded by `WEBHOOK_TIMEOUT` (10 s). With a 2 s poll and a
batch of 50, the *best-case* drain rate is `BatchSize / PollInterval` only if
each POST is instant; in practice one slow receiver stalls the whole batch.
Under the 0001 load profile (each asset emits `job.starting → job.started →
job.done`), `webhook_pending` grows without bound and never drains.

Delivering the batch **concurrently** (bounded pool of `WEBHOOK_CONCURRENCY`)
should make the drain rate scale with the pool size until the receiver or the DB
becomes the limit, so `webhook_pending` returns to ~0.

## What changed (the implementation under test)

- **Concurrent delivery.** `tick()` now fans the claimed batch out across an
  `errgroup.Group` with `SetLimit(WEBHOOK_CONCURRENCY)` — one goroutine per
  delivery, at most `WEBHOOK_CONCURRENCY` in flight. `handleFailure`/`backoff`/
  `markFailed` are unchanged and keyed by the row's own id, so concurrent
  delivery is race-free. (`internal/webhook/dispatcher.go`)
- **HTTP transport tuning.** The dispatcher's `http.Client` now uses a custom
  `http.Transport` with `MaxIdleConnsPerHost = MaxConnsPerHost = WEBHOOK_CONCURRENCY`.
  Go's default `MaxIdleConnsPerHost` is 2, which would serialize TLS handshakes
  for N concurrent POSTs to one receiver and inflate p95 — the tuning lets
  concurrent deliveries to the same host reuse connections.
- **Delivery metrics wired.** `WebhookDeliveryTotal`, `WebhookDeliveryDuration`,
  and `WebhookDeliveryFailures` are now recorded per delivery (labels: `event`,
  `status` ∈ {delivered, failed, error}; never `asset_id`). `NewDispatcher` takes
  `*metrics.Metrics`, passed from `cmd/server/main.go`. The
  `sli:webhook_delivery_latency_seconds:p95` recording rule already existed and
  now has a histogram to read.
- **Config.** `WEBHOOK_CONCURRENCY` (default **10**) added to `WebhookConfig` /
  `internal/config/env.go`.
- **Concurrency safety note (documented in code):** the `SKIP LOCKED` claim runs
  outside an explicit transaction, so locks release when the SELECT returns. That
  is safe for a single dispatcher fanning out to internal goroutines (each row is
  claimed once), but NOT for >1 dispatcher process. Scaling past one dispatcher
  requires wrapping the claim in a tx or adding a `claimed_at`/`locked_by` column.

## Setup (record this with every run)

- **Resource pinning** (`docker-compose.loadtest.yml`): `api` = 1.0 CPU / 512 MB,
  `worker` = 1.0 CPU / 1 GB. `TRACE_SAMPLING_RATE=1.0`.
- **Stack:** core + observability + webhooks overlays + loadtest pins, all up.
- **Webhook receiver:** the bundled receiver (see `docker-compose.webhooks.yml`),
  reachable from the API container; one registration subscribed to all four
  `job.*` events.
- **Profile:** open model, `./loadtest/run.sh open --rate 10/s --duration 90s`
  (same arrival rate as 0001). Each asset produces 3 webhook deliveries.

> Local results are **relative** — trust the `webhook_pending` drain and the
> before/after delta, not absolute throughput.

## Method (the loop)

1. Confirm `webhook_pending` is climbing under load (the bottleneck).
2. Read `sli:webhook_delivery_latency_seconds:p95` and the delivery-rate panel.
3. Switch concurrency on (this change), re-run the **same** profile, compare the
   `webhook_pending` trajectory and the delivery rate.

## Results (before — serial dispatcher, from 0001-era observation)

| Signal | Value | Source |
|--------|-------|--------|
| `webhook_pending` peak | **~5,901, never drains** | `sli:webhook_pending:current` |
| Delivery rate | bounded by serial POSTs | `rate(mpiper_webhook_delivery_total[5m])` *(was unrecorded before this change)* |
| Delivery p95 | unreadable (histogram unrecorded) | `sli:webhook_delivery_latency_seconds:p95` |

## Results (after — concurrent dispatcher) — MEASURED 2026-06-30

A/B on the same binary, `WEBHOOK_CONCURRENCY=1` vs `10`, under closed-model load
(20 and 40 VUs) with the bundled `http-https-echo` receiver and one registration
subscribed to all four `job.*` events.

| Config | `webhook_pending` under load | Reading |
|--------|------------------------------|---------|
| `WEBHOOK_CONCURRENCY=1` (serial) | stayed **~0–7** | dispatcher kept up |
| `WEBHOOK_CONCURRENCY=10` | **~0** | dispatcher kept up |

**Honest reading: webhook delivery was *not* the bottleneck at reproducible local
scale.** With a fast local receiver and the API pinned to 1 CPU, the achievable
event-generation rate (`job.starting` at the upload rate + worker `job.started`/
`job.done`) stayed under the *serial* dispatcher's ceiling (≈ `BatchSize/PollInterval`
= 50/2 s ≈ 25/s), so even `WEBHOOK_CONCURRENCY=1` drained `webhook_pending` to ~0.
The dramatic 0001 backlog (~5,901) was not reproduced here — it requires either a
**slow/realistic receiver** (real endpoints have 50–500 ms latency) or a
generation rate above the serial ceiling, neither of which this local rig
produces once the worker (not the dispatcher) is the throughput limiter.

What *was* delivered and verified:
- **Metrics now wired** — `webhook.delivery.total/duration/failures` record per
  delivery (labels `event`,`status`), so the dispatcher is now observable at all
  (previously a blind spot). The `sli:webhook_delivery_latency_seconds:p95` rule
  finally has a histogram.
- **Concurrency proven** by `TestDispatcher_DeliversConcurrently` (integration):
  20 deliveries at concurrency 5 run with max-in-flight ∈ [2,5] and all complete —
  i.e. the headroom is real and kicks in precisely when a slow receiver or a burst
  pushes generation past the serial ceiling.
- **Transport tuning** (`MaxIdleConnsPerHost=WEBHOOK_CONCURRENCY`) removes the
  default 2-connection cap that would otherwise serialise TLS to one host.

> **Follow-up to make the contrast visible:** add artificial receiver latency
> (e.g. a 200 ms sleep in the echo handler). At 200 ms/POST the serial ceiling
> drops to ~5/s and `webhook_pending` backs up under load, where `WEBHOOK_CONCURRENCY=10`
> drains it ~10×. That is the scenario this change is for; the instant local
> receiver hides it.

## Conclusion

At local scale with an instant receiver, the **worker** is the binding
constraint and the webhook dispatcher keeps up serially — so this change is
**insurance + observability** rather than a measured throughput win *here*: the
delivery metrics are now recorded (it was previously unmonitored), and the
bounded-concurrency fan-out (proven by the integration test) provides the
headroom that matters the moment a real, latency-bearing receiver or a burst
exceeds the serial ceiling. The honest result is "no regression, now observable,
with headroom" — not the 0001-style drain, which this rig can't reproduce without
a slow receiver.

## Reproduce

```bash
docker compose -f docker-compose.yml -f docker-compose.observability.yml \
  -f docker-compose.webhooks.yml -f docker-compose.loadtest.yml up -d --build
# (register a webhook subscribed to job.* against the bundled receiver)
./loadtest/run.sh open --rate 10/s --duration 90s

# Backlog drain (DB-side, ground truth):
docker exec mpiper-postgres psql -U mpiper -d mpiper -c \
  "SELECT status, count(*) FROM webhook_deliveries GROUP BY status;"

# Grafana http://localhost:3000 → MPiper → "Queue Health":
#   - "Webhook delivery p95 (SLO < 10s) + pending": pending should fall to ~0
# Prometheus :9090:
#   sum(rate(mpiper_webhook_delivery_total[5m]))           # delivery throughput
#   histogram_quantile(0.95, sum by (le) (rate(mpiper_webhook_delivery_duration_seconds_bucket[5m])))
```

> **Histogram-bucket caveat:** the delivery histogram is newly recorded. When
> reading p95 over a window that spans the deploy, either reset Prometheus data
> or wait for the pre-change (empty) series to age out, so mixed/empty buckets
> don't distort `histogram_quantile`.

## Tests backing this change

- `internal/webhook/dispatcher_test.go` — `TestRecordDelivery_EmitsMetrics`
  (counter/failure/duration recorded with correct labels), `…_NilMetricsIsSafe`.
- `internal/webhook/dispatcher_integration_test.go` (`-tags integration`,
  testcontainers Postgres) — `TestDispatcher_DeliversConcurrently`: 20 deliveries
  at concurrency 5 asserts max-in-flight ∈ [2, 5], all rows delivered, and the
  delivery metric counts all 20; the success test also asserts a `delivered`
  metric was recorded.
