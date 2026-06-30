# Track 2 — Queue-depth autoscaling — Session Handoff (start here)

**Purpose:** everything a fresh conversation needs to begin **Track 2 (scale the
worker fleet on queue lag)** without prior context. Read this top to bottom. It is
the *operational* companion; pair it with a short design doc
(`track-02-autoscaling.md`) written before coding, per the per-track design-doc
philosophy. Assumes **Tracks 3, 1, and 1b are done** — tracing/SLOs/dashboards/k6
exist, the worker is a bounded concurrent pool, and webhook delivery is concurrent.

---

## 1. What MPiper is (60-second orientation)

Go **API** (`cmd/server`, `internal/`) accepts uploads; a Python **worker**
(`worker/`) processes them. They talk over **Redis Streams** (`media:jobs`, group
`worker-group`). **Postgres** is the source of truth; **MinIO** stores objects.
Full orientation + topology + runbook live in
[`track-01-handoff.md`](track-01-handoff.md) §1, §6, §7 — reuse them.

**What Track 1 changed (your starting point):** the worker now runs a bounded
`ThreadPoolExecutor` sized by `MAX_CONCURRENT_JOBS` (honour `mcj ≈ cores-per-pod`),
recovers dead-consumer messages with `XAUTOCLAIM`, and dead-letters poison
messages to `media:jobs:dlq`. Measured **2.37× throughput** at `mcj=4` on 4 cores
(`experiments/0002`). Crucially: **per-pod throughput now scales with cores, so the
next lever is more pods.**

---

## 2. The goal in one sentence

Make the **number of worker pods** track the **Redis Streams backlog** (queue lag),
so the pipeline absorbs bursts (scale up) and stops wasting capacity when idle
(scale down) — a closed control loop driven by a real saturation signal, not CPU.

> Why queue-lag, not CPU: CPU-based HPA reacts to *symptom* not *cause*, and lags
> bursty I/O+transcode work. Queue depth / oldest-message-age is the direct
> backpressure signal (Little's Law: `L = λW` — a growing `L` at fixed `W` means
> `λ > μ`, i.e. add workers).

---

## 3. Prerequisites & gotchas verified in code (do these FIRST)

- **⚠️ The queue-depth gauge is NOT wired.** `internal/metrics/metrics.go` defines
  `QueueDepth` and `RegisterQueueDepthFunc(...)`, but **`cmd/server/main.go` never
  calls it** (it calls `RegisterOutboxPendingFunc` and `RegisterWebhookPendingFunc`
  only). So `queue.depth` is defined-but-empty, and `sli:queue_depth:current`
  reads off it. **Task 0: wire it** — register a callback returning
  `XLEN media:jobs` (or, better, the consumer-group *lag*: `XINFO GROUPS` →
  `lag`, which excludes already-delivered-but-unacked entries). Without a recorded
  signal there is nothing to autoscale on.
- **Queue depth vs lag.** `XLEN media:jobs` counts *all* stream entries (acked ones
  linger until trimmed — `MaxStreamLength: 10_000` in `queue.NewRedisQueue`), so it
  is **not** a true backlog. Prefer the consumer-group **`lag`** (undelivered) and/or
  the **oldest-pending age** via `XPENDING`. Decide and document which signal drives
  scaling.
- **k8s manifests already exist** but scale on the wrong signal: `deploy/k8s/worker-deployment.yaml`
  has `replicas: 2` and a **`HorizontalPodAutoscaler` (min 2 / max 10) on CPU 75% /
  mem 85%**. Track 2 replaces/augments this with a **lag-driven** scaler.
- **`mcj ≈ cores-per-pod` (Track 1 lesson).** Autoscaling adds *pods*; each pod runs
  its own thread pool. Don't crank `MAX_CONCURRENT_JOBS` — set it to the pod's CPU
  limit and scale pod count. Oversubscription was measured to *reduce* throughput.
- **Recovery interplay.** New pods join `worker-group` and read `>` (new messages);
  a scaled-*down* pod's in-flight work is abandoned and reclaimed by `XAUTOCLAIM`
  after `RECOVERY_MIN_IDLE_MS` (default 120s). For responsive scale-down, ensure the
  pod's `terminationGracePeriodSeconds` ≥ the worker's `SHUTDOWN_DRAIN_TIMEOUT`
  (30s) so in-flight jobs drain instead of being abandoned. Verify both.
- **DB pool pressure.** N pods × `mcj` connections. Each pod sizes its pool to
  `mcj + 2` (`worker/consumer/db.py`). At max replicas this can exceed Postgres'
  `max_connections` — compute `maxReplicas × (mcj+2) + API pool` and cap accordingly
  (watch `mpiper_db_connections_*`).

---

## 4. Engineering targets

1. **Wire the scaling signal** (Task 0 above): record consumer-group lag (and/or
   oldest-pending age) as a gauge, expose it where the scaler can read it.
2. **Choose the scaler.** Options to weigh in the design doc:
   - **KEDA `redis-streams` scaler** — purpose-built; scales a Deployment on
     `pendingEntriesCount`/lag of a stream+group. Cleanest fit; needs KEDA installed.
   - **Prometheus-adapter + HPA on a custom metric** (the lag gauge) — reuses the
     existing Prometheus, no new operator, but more wiring.
   - **A custom controller** — most work, most lesson; probably overkill.
   Recommend KEDA `redis-streams`; document the tradeoff.
3. **Tune the control loop.** Target lag per pod, `pollingInterval`,
   `cooldownPeriod`/stabilization, min/max replicas. Avoid flapping (hysteresis).
4. **Graceful scale-down.** Confirm SIGTERM → bounded drain → no lost work (relies on
   Track 1's `shutdown(timeout)` + `XAUTOCLAIM` safety net).

---

## 5. Acceptance / how we'll know it worked

Re-use the k6 harness and the consolidated overlay (`docker-compose.loadtest.yml`
env knobs; `./loadtest/run.sh`). For k8s, run on the cluster the manifests target
(or kind/minikube + KEDA).

- **Backlog → scale-up → drain cycle:** drive `open --rate` above one pod's μ; the
  scaler adds pods; aggregate μ rises ~linearly with pods (until CPU/DB-bound);
  **lag rises then drains to ~0**; then load stops → pods **scale back down** after
  cooldown.
- **No flapping** under steady load; **no lost/double-processed jobs** across
  scale events (verify via DB job counts + dedup; a scaled-down pod's job is
  reclaimed, not dropped).
- **DB pool stays under `max_connections`** at `maxReplicas`.
- Write `experiments/0004-autoscaling.md` (0001 template: setup w/ pod & resource
  limits → method → backlog/replica/lag timeseries → conclusion). Capture the
  replica-count and lag panels.

---

## 6. Suggested first-session scope

1. **Task 0:** wire the consumer-group **lag** gauge (`RegisterQueueDepthFunc` is
   unused) + a Grafana panel; prove it tracks a manually-XADD'd backlog. *Demo:*
   panel moves with `XADD`/drain.
2. **Design doc** `track-02-autoscaling.md`: signal choice (lag vs depth vs age),
   scaler choice (KEDA vs prom-adapter), control-loop params, scale-down safety.
3. **KEDA `ScaledObject`** on the worker Deployment driven by the lag signal;
   replace the CPU HPA. Set `mcj` = pod CPU limit; min/max replicas; cooldown.
4. **Load test the cycle** + `experiments/0004`.

Banks a clean, demoable win (lag-driven scale-up/drain) on top of the now-concurrent
worker, and is provable by re-running the existing k6 profile against the dashboards.

---

## 7. Key reads

- [`track-01-handoff.md`](track-01-handoff.md) — topology, runbook, env, landmines (reuse).
- [`experiments/0002-concurrent-worker.md`](../../experiments/0002-concurrent-worker.md) — the per-pod baseline μ to multiply by replica count.
- `internal/metrics/metrics.go` (`QueueDepth`, `RegisterQueueDepthFunc`, `QueueProcessingLag`), `cmd/server/main.go` (where to wire it), `internal/queue/queue.go` (`MaxStreamLength`), `deploy/k8s/worker-deployment.yaml` (current CPU HPA to replace).
