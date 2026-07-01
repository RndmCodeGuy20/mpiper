# Track 3 — End-to-end tracing, SLOs & local load testing

**Status:** planning · **Prereq:** none · **Unlocks:** makes every other track measurable

## 1. Problem

We can't improve what we can't see. Right now:

- The **distributed trace breaks at the Redis boundary.** The Go API traces the
  HTTP request and the `Enqueue` call, but it never injects a `traceparent` into
  the stream message. The worker has OTel **metrics** but **no tracer** and does
  no context extraction. So we cannot answer "for *this* asset, where did the 40
  seconds go?" as a single trace spanning API → outbox → Redis → worker → ffmpeg →
  variant write.
- We have metrics but **no SLOs** — no agreed definition of "good", so no way to
  say whether a change helped.
- We have **no way to generate controlled load**, so we've never seen the system
  bend. The single-threaded worker (Track 1) is an invisible bottleneck until
  something pushes on it.

The user's real question: *this is a local project — how do we test under load
and actually understand what's working, failing, and needs optimization?*

That question is answered in §3.

## 2. Goals / Non-goals

**Goals**
- One trace per asset, end to end, across the queue boundary, viewable in Tempo.
- A small, explicit set of **SLIs and SLOs** for the pipeline.
- A repeatable **local load harness** that can saturate the system on a laptop.
- Grafana dashboards (RED for the API, USE for the worker/host, a pipeline-latency
  funnel, queue lag) wired so a metric spike links to an example trace (exemplars).
- A written **bottleneck-analysis loop**: load → observe → locate → optimize → re-run → compare.

**Non-goals**
- Production-scale absolute numbers. Local results are **relative** — they reveal
  bottlenecks and validate *direction*, not real-world capacity (see §7).
- Alerting/paging infrastructure (note SLO burn-rate alerts as a follow-up).
- Replacing the existing stack — we extend the bundled Tempo/Prometheus/Loki/Grafana.

## 3. Can you load-test meaningfully on a laptop? Yes — here's the methodology

The misconception is that load testing requires cloud scale. It doesn't. Load
testing is about **saturating the system relative to its own capacity** and
watching where it bends. A single-threaded worker on a laptop saturates at a
handful of concurrent jobs — you can absolutely push it past that locally.

The thing that makes local results *interpretable* is **pinning resources** so
runs are reproducible and the bottleneck isn't hidden by spare laptop cores. We
add CPU/memory limits to the `api` and `worker` containers (compose `deploy.
resources.limits`) so "the worker is the bottleneck" is a stable, observable fact
rather than something that moves run to run.

**The loop we're building:**

```
            ┌─────────────────────────────────────────────┐
            │ 1. Define SLIs/SLOs (what "good" means)       │
            └───────────────┬─────────────────────────────┘
                            ▼
            ┌─────────────────────────────────────────────┐
            │ 2. Instrument end-to-end (close the trace gap)│
            └───────────────┬─────────────────────────────┘
                            ▼
   ┌────────────┐   generate    ┌─────────────────────────┐
   │ k6 (host)  │ ────────────▶ │ MPiper (CPU-pinned)      │
   │ load model │   presign→PUT │  API + worker            │
   └────────────┘   →complete   └───────────┬─────────────┘
        │ client-side metrics               │ app OTel traces+metrics
        ▼                                    ▼
   ┌─────────────────────────────────────────────────────┐
   │ 3. Observe in Grafana: RED, USE, pipeline funnel,     │
   │    queue lag — metric spike → exemplar trace in Tempo │
   └───────────────┬─────────────────────────────────────┘
                   ▼
   ┌─────────────────────────────────────────────────────┐
   │ 4. Locate bottleneck (trace waterfall + USE) →        │
   │    optimize → re-run same profile → compare           │
   └─────────────────────────────────────────────────────┘
```

### Load model (this is the subtle part)

- **Closed model (fixed VUs):** N virtual users each loop presign→upload→complete
  as fast as they can. Good for finding max throughput and saturation point.
- **Open model (fixed arrival rate):** X new uploads/sec regardless of how fast the
  system responds. Good for finding the **latency knee** and watching queue lag
  grow when arrival rate > service rate (a live demonstration of Little's Law:
  `L = λW`).

We use **k6** run from the **host** (like `scripts/demo-e2e.sh`): the host can
reach both the API (`localhost:5010`) and MinIO (`localhost:9000`), so k6 performs
the *real* client flow — presign, `PUT` the file to the public endpoint, then
`complete`. k6 uploads real fixtures (the existing image + `tests/test_assets/
sample.mp4`), optionally fanning out copies with unique bytes to defeat content-hash
dedup when we want true per-job work.

Two views of the same run:
- **Client view** (k6's own metrics): request rate, error rate, client-side
  latency percentiles → remote-written to the bundled Prometheus.
- **Server view** (MPiper's OTel): the pipeline's internal spans and metrics —
  this is the point of the track, and what we'll mostly read.

## 4. Design

### 4.1 Close the trace gap (the core engineering work)

1. **Inject context on enqueue (Go).** When the outbox relay (or `RedisQueue.
   Enqueue`) publishes, inject the active span context as a `traceparent` field in
   the stream message using the OTel propagator. The outbox row should carry the
   trace context too (so the trace survives the store-and-forward hop).
2. **Extract + continue on consume (Python).** Add an OTel **tracer** to the worker
   (mirroring `worker/utils/metrics.py`). In `consume()`, extract `traceparent`
   from the message and start the consumer span as a **child** (a span link is the
   correct primitive for queue fan-in; we'll use a child span with a link to keep
   the waterfall readable).
3. **Span the pipeline stages.** Wrap `process_asset_dispatch`, download,
   dedup-check, each image variant, and each ffmpeg invocation (poster / transcode /
   preview) in spans with attributes (asset_id, type, bytes, role, ffmpeg rc).
4. **Correlate logs.** Stamp `trace_id`/`span_id` into worker + API structured logs
   so Loki ↔ Tempo cross-linking works in Grafana.

End result: open an asset in Tempo and see `HTTP POST /presign … → enqueue →
(time in queue) → worker consume → download → transcode_720p → write variant`,
with the **queue wait time** visible as the gap between enqueue and consume.

### 4.2 SLIs / SLOs (initial, deliberately small)

| SLI | Definition | Initial SLO (local) |
|-----|------------|---------------------|
| Presign latency | p95 of `POST /storage/presign` | < 150 ms |
| Image ready latency | p95 (complete → asset `ready`) for images | < 5 s |
| Video ready latency | p95 (complete → asset `ready`) for videos | < 60 s |
| Queue wait | p95 (enqueue → consume start) | < 2 s |
| Job success rate | done / (done + failed) | > 99% |
| Webhook delivery latency | p95 (event row created → delivered) | < 10 s |

These come straight from spans/metrics we'll have. The numbers are starting
guesses; the *point* is to make them explicit, then move them based on data.

### 4.3 Dashboards (Grafana, provisioned in `observability/grafana/dashboards`)

- **API — RED:** request **R**ate, **E**rror rate, **D**uration (p50/p95/p99) per route.
- **Worker/host — USE:** CPU/mem **U**tilization, **S**aturation (queue depth,
  in-flight jobs), **E**rrors. (cAdvisor/node metrics or the collector's own.)
- **Pipeline funnel:** uploaded → processing → ready/failed counts + the
  per-stage latency breakdown (from spans).
- **Queue health:** stream length, oldest-pending age, outbox relay lag (metric
  already exists), webhook pending gauge (already exists).
- **Exemplars:** histogram panels link a bucket spike to a concrete Tempo trace.

### 4.4 Bottleneck-analysis loop (documented runbook)

For each experiment: fix a load profile, run it, then read in order — (1) is the
SLO breached? (2) USE: is the worker CPU-saturated or queue-saturated? (3) open an
exemplar trace: which span dominates? (4) form a hypothesis, change one thing,
re-run the **same** profile, compare. Record results in an `experiments/` log so
"the transcode span dropped from 38s→6s after X" is captured.

## 5. Phased implementation plan

Each phase is independently demoable.

- **Phase 0 — Resource pinning & baseline.** Add `deploy.resources.limits` to api/
  worker; bring up the observability overlay; capture a one-shot baseline with the
  existing `demo-e2e.sh`. *Demo:* Grafana shows the run; numbers are reproducible.
- **Phase 1 — Trace propagation.** Inject `traceparent` on enqueue (Go) + outbox
  row; extract + continue in the worker; add the worker tracer. *Demo:* a single
  Tempo trace spans API→worker for one asset, with visible queue wait.
- **Phase 2 — Pipeline spans + log correlation.** Span dispatch/download/dedup/
  each variant/each ffmpeg call; add trace IDs to logs. *Demo:* trace waterfall
  shows per-stage timing; click a log line → its trace.
- **Phase 3 — SLO recording rules + dashboards.** Prometheus recording rules for
  the SLIs in §4.2; provision the four dashboards. *Demo:* a dashboard shows each
  SLI vs its SLO target.
- **Phase 4 — k6 load harness.** `loadtest/` with closed- and open-model scripts,
  a host-run wrapper, fixture fan-out, and k6→Prometheus remote write. *Demo:*
  `./loadtest/run.sh open --rate 5/s --duration 3m` drives the system; Grafana
  shows queue lag climbing and the latency knee.
- **Phase 5 — First experiment writeup.** Run a saturating profile, capture the
  bottleneck (expected: the single-threaded worker), and write it up as the
  motivating evidence for **Track 1**. *Demo:* `experiments/0001-worker-saturation.md`
  with before numbers + the trace proving where time goes.

## 6. How we'll know it works (acceptance)

- A Tempo trace for any asset includes both API and worker spans, with queue wait
  time visible.
- Every SLI in §4.2 renders on a dashboard against its target.
- `loadtest/run.sh` reproducibly drives the system into SLO breach, and the
  responsible stage is identifiable from a trace within ~2 minutes of looking.
- Phase 5 writeup names the bottleneck with evidence — the input to Track 1.

## 7. Risks & honest caveats

- **Local ≠ production.** Absolute numbers are not portable (laptop CPU, no network
  latency, single-node Redis/PG). Treat results as **relative**: bottleneck
  location and before/after deltas are trustworthy; "we do N uploads/sec" is not.
- **Noisy neighbor.** k6, the app, and the observability stack share the laptop.
  Pin app resources and keep k6 modest; consider running k6 with `--throw` budgets.
- **Container CPU limits change behavior** (e.g. ffmpeg threads). That's fine — it's
  what makes runs comparable — but document the limits with each experiment.
- **Trace cardinality / sampling.** Asset-ID attributes are high-cardinality on
  *traces* (OK) but must never become metric labels. Keep `TRACE_SAMPLING_RATE`
  in mind; sample at 100% locally, lower in prod.
- **Dedup hides work.** Identical fixtures dedup after first processing; the load
  harness must fan out unique bytes when measuring real per-job cost.

## 8. Follow-ups (out of scope here)

- SLO **burn-rate alerting** (multi-window) once SLOs stabilize.
- Continuous profiling (Pyroscope) to attribute CPU *inside* a span.
- CI smoke load test with a latency budget (feeds Track 8).
