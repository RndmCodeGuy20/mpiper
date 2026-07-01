"""
worker.consumer.consumer

Documented Redis Streams consumer for the media worker.

This module exposes the `Consumer` class which reads messages from a Redis
Stream (consumer group), claims work by loading a corresponding job row from
Postgres and dispatches processing to `process_asset_dispatch`.

Design goals:
- Durable job truth lives in Postgres (`jobs` table). Redis Streams is used
  only as the delivery transport.
- Messages may carry either a `job_id` (preferred) or an `asset_id` (convenience
  path that will ensure a job row exists).
- Idempotency is enforced by checking job/asset status before doing heavy work.
- Stuck or missing stream messages are recovered by re-adding pending jobs
  back to the stream (simple requeue strategy).

Concurrency model — thread pool (and why):
- Per-job work is dominated by I/O and subprocesses, not pure-Python CPU:
  object-store download/upload (network I/O, releases the GIL), ffmpeg invoked
  via `subprocess` (a separate OS process — true parallelism regardless of the
  GIL), Pillow (releases the GIL for most pixel ops), and psycopg calls (I/O).
  A `ThreadPoolExecutor` therefore gives real concurrency here while keeping a
  single shared `psycopg_pool.ConnectionPool` (thread-safe) and a single set of
  OTel instruments (thread-safe; context is per-thread, so each task starts its
  own `worker.consume` span cleanly).
- A process pool was considered and rejected for now: it would force a DB/Redis
  pool per process, require pickling the storage client across the process
  boundary, and re-initialise OTel in every worker — significant cost for little
  gain given how little time is spent in GIL-bound Python.
- GIL escalation path: if profiling later shows pure-Python sections (e.g. large
  file hashing, GIL-holding Pillow paths) dominate and threads stop scaling,
  move the CPU-bound stage to a ProcessPoolExecutor (hybrid: threads for I/O,
  processes for transform) rather than converting the whole consumer.

Notes about external expectations:
- `pg_pool` must expose `connect_pg()` returning a DB connection context manager
  compatible with `psycopg` (connection yields `cursor()` and supports commit/rollback).
- `storage` implements the storage client used by the processing logic.
- `cfg` must provide stream_name, consumer_group, MAX_JOB_ATTEMPTS and other
  configuration values referenced below.

"""

from __future__ import annotations

import threading
import time
from concurrent.futures import Future, ThreadPoolExecutor
from concurrent.futures import wait as futures_wait
from contextlib import nullcontext
from typing import Dict, Set

import redis
from redis.exceptions import ResponseError
from opentelemetry import trace
from opentelemetry.propagate import extract

from worker.consumer.config import WorkerConfig
from worker.consumer.db import PgPool
from worker.processing.processor import RetryableException, process_asset_dispatch
from worker.storage.base import StorageX
from worker.utils.logger import get_logger
from worker.utils.tracing import get_tracer
from worker.utils import metrics as wm
from worker.webhooks import insert_webhook_deliveries

logger = get_logger(__name__)


class Consumer:
    """Redis Streams consumer backed by Postgres job rows.

    Attributes
    ----------
    pg: PgPool
        Postgres connection pool wrapper providing `connect_pg()`.
    redis: redis.Redis
        Redis client instance.
    storage: Any
        Storage client used by the processing layer.
    cfg: Any
        Configuration object with stream_name, consumer_group, and other values.

    Behavior
    --------
    - Ensures the consumer group exists on construction (noop if already exists).
    - `consume()` reads a single message and processes it. Returns True when work
      was performed, False when no messages were available.

    """

    def __init__(
        self, pg_pool: PgPool, redis_url: str, storage: StorageX, cfg: WorkerConfig
    ) -> None:
        """Create a consumer instance.

        Parameters
        ----------
        pg_pool:
            Postgres pool wrapper.
        redis_url:
            Redis connection URL (e.g. redis://localhost:6379/0).
        storage:
            Storage client passed through to processing functions.
        cfg:
            Configuration object with stream_name and consumer_group.
        """
        self.pg = pg_pool
        self.redis = redis.Redis.from_url(
            redis_url,
            decode_responses=True,
            retry_on_timeout=True,
            socket_connect_timeout=5,
            socket_timeout=10,
        )
        self.storage = storage
        self.cfg = cfg

        # Bounded worker pool. Honours MAX_CONCURRENT_JOBS (cfg.max_concurrent_jobs):
        # up to that many jobs run concurrently, one per pool thread. In-flight
        # work is tracked so the read loop only fetches as many new messages as
        # there is free capacity, and so shutdown can drain deterministically.
        self._max_workers = max(1, int(getattr(cfg, "max_concurrent_jobs", 1) or 1))
        self._executor = ThreadPoolExecutor(
            max_workers=self._max_workers, thread_name_prefix="job"
        )
        self._inflight_lock = threading.Lock()
        self._inflight = 0
        self._futures: Set[Future] = set()
        self._closed = False

        # Periodic recovery state. _last_recovery = 0 makes recovery run on the
        # first consume() so leftovers from a prior crash are swept at startup.
        # The cadence matches the XAUTOCLAIM min-idle threshold below. See DEV-35.
        self._last_recovery = 0.0
        self._recovery_interval = 120.0
        # Minimum idle time (ms) before a pending message is eligible to be
        # reclaimed from a (presumed dead) consumer via XAUTOCLAIM.
        self._recovery_min_idle_ms = int(getattr(cfg, "recovery_min_idle_ms", 120000))

        # Ensure the consumer group exists. If it already exists Redis raises an
        # error; ignore that specific error.
        try:
            self.redis.xgroup_create(
                self.cfg.stream_name, self.cfg.consumer_group, id="$", mkstream=True
            )
        except ResponseError as exc:
            logger.debug("consumer group exists or cannot be created: %s", exc)

        # Write a health sentinel once the consumer group is initialised. The
        # container healthcheck (test -f /tmp/worker_healthy) reads this file.
        # Reaching this point means Redis is connected and the group exists.
        try:
            with open("/tmp/worker_healthy", "w") as fh:
                fh.write("ok")
        except OSError as exc:
            logger.warning("could not write health sentinel: %s", exc)

        # Expose the dead-letter stream depth as an observable gauge so DLQ
        # accumulation is visible on the dashboards (no-op if telemetry is off).
        try:
            wm.register_dlq_depth_gauge(
                lambda: self.redis.xlen(self.cfg.dlq_stream_name)
            )
        except Exception as exc:  # never let telemetry wiring break startup
            logger.warning("could not register DLQ depth gauge: %s", exc)

    def consume(self, consumer_name: str) -> bool:
        """Top up the worker pool with new stream messages.

        Reads up to the current free capacity (MAX_CONCURRENT_JOBS minus in-flight)
        and submits each message to the thread pool, where it is processed
        concurrently. Each message carries either `job_id` (preferred) or
        `asset_id`; dispatch + per-message ack happen inside the task.

        Parameters
        ----------
        consumer_name:
            Consumer identifier used for the Redis consumer group.

        Returns
        -------
        bool
            True if at least one message was read and submitted, False when there
            was no free capacity or no messages were available. The caller should
            sleep briefly on False to avoid a busy loop.
        """
        # Recover stuck jobs on a fixed cadence, independent of load. Doing this
        # only on the idle path meant recovery never ran under sustained load —
        # exactly when crashed-mid-job rows are most likely. See DEV-35.
        self._maybe_recover(consumer_name)

        # Only fetch as many messages as we can actually start right now. When at
        # capacity we return immediately (don't hold a 5s blocking read open while
        # full) so freed slots are picked up promptly on the next call.
        free = self._free_capacity()
        if free <= 0:
            return False

        try:
            resp = self.redis.xreadgroup(
                groupname=self.cfg.consumer_group,
                consumername=consumer_name,
                streams={self.cfg.stream_name: ">"},
                count=free,
                block=5000,
            )
        except (TimeoutError, redis.exceptions.TimeoutError):
            return False

        if not resp:
            return False

        # Response format: [(stream_name, [(msg_id, {field: value}), ...])]
        _, messages = resp[0]
        for msg_id, fields in messages:
            self._submit(msg_id, fields)

        return len(messages) > 0

    def _free_capacity(self) -> int:
        """Number of additional jobs that can be started right now."""
        with self._inflight_lock:
            return self._max_workers - self._inflight

    def _submit(self, msg_id: str, fields: Dict[str, str]) -> None:
        """Reserve a slot and submit one message to the pool for processing."""
        with self._inflight_lock:
            self._inflight += 1
        try:
            future = self._executor.submit(self._process_message, msg_id, dict(fields))
        except RuntimeError:
            # Executor already shut down (during drain). Release the slot; the
            # message stays in the PEL and is reclaimed by recovery later.
            with self._inflight_lock:
                self._inflight -= 1
            return
        self._futures.add(future)
        future.add_done_callback(self._on_task_done)

    def _on_task_done(self, future: Future) -> None:
        """Release the in-flight slot when a task finishes (success or failure)."""
        self._futures.discard(future)
        with self._inflight_lock:
            self._inflight -= 1

    def _process_message(self, msg_id: str, fields: Dict[str, str]) -> None:
        """Process a single stream message inside a pool thread.

        Each task starts its OWN `worker.consume` span (carrying this message's
        extracted trace context) so concurrent jobs never share a span and the
        per-asset Tempo waterfalls stay separate. The message is acked by its own
        msg_id only on success (inside `_handle_job`); on failure it is left in
        the PEL for recovery.
        """
        wm.record_consume()
        try:
            # Normalize fields to a dict
            payload: Dict[str, str] = {k: fields[k] for k in fields}
            logger.info("message received id=%s payload=%s", msg_id, payload)

            body = payload.get("body")
            if body:
                # If a body field is present, it contains a JSON-encoded dict
                import json

                body_dict = json.loads(body)
                payload.update(body_dict)
                payload.pop("body")

            job_id = payload.get("job_id")
            asset_id = payload.get("asset_id")

            # Extract the producer trace context (injected by the Go relay) and
            # continue the trace here, starting the consume span INSIDE this task
            # thread. See _consume_span.
            with self._consume_span(payload, msg_id, job_id, asset_id):
                if job_id:
                    self._handle_job(job_id, msg_id)
                elif asset_id:
                    self._handle_asset_message(asset_id, msg_id)
                else:
                    logger.error("message missing job_id and asset_id: %s", payload)
                    # Acknowledge to remove the malformed message from the stream.
                    self.redis.xack(
                        self.cfg.stream_name, self.cfg.consumer_group, msg_id
                    )
        except Exception:
            logger.exception("unhandled exception while processing message %s", msg_id)
            # Do not ack the message so it remains in the pending entries list
            # for recovery/retry later.

    def _await_inflight(self, timeout: float | None = None):
        """Block until all in-flight tasks finish or `timeout` elapses.

        Returns the (done, not_done) future sets from concurrent.futures.wait.
        Used by graceful shutdown and by tests to make submission deterministic.
        """
        pending = list(self._futures)
        if not pending:
            return set(), set()
        return futures_wait(pending, timeout=timeout)

    def shutdown(self, timeout: float = 30.0) -> None:
        """Stop accepting work and drain in-flight tasks, bounded by `timeout`.

        Aligns with the container stop_grace_period: we wait up to `timeout` for
        running jobs to finish, then stop. Any task still running is abandoned —
        its Redis message stays unacked in the PEL and is safely reclaimed by
        XAUTOCLAIM recovery on the next worker to run.
        """
        if self._closed:
            return
        self._closed = True
        _, not_done = self._await_inflight(timeout=timeout)
        if not_done:
            logger.warning(
                "shutdown: %d job(s) still running after %.0fs; abandoning "
                "(messages remain in PEL for recovery)",
                len(not_done),
                timeout,
            )
        # Don't block again on the abandoned tasks.
        self._executor.shutdown(wait=False)

    def _consume_span(self, payload, msg_id, job_id, asset_id):
        """Start the worker.consume span continuing the producer trace.

        Returns a context manager. When tracing is not initialised (telemetry
        failed at startup) this is a no-op so message processing is unaffected.
        """
        tracer = get_tracer()
        if tracer is None:
            return nullcontext()

        carrier = {
            k: payload[k]
            for k in ("traceparent", "tracestate", "baggage")
            if k in payload
        }
        parent_ctx = extract(carrier)
        producer_sc = trace.get_current_span(parent_ctx).get_span_context()
        links = [trace.Link(producer_sc)] if producer_sc.is_valid else None

        attrs = {
            "messaging.system": "redis",
            "messaging.destination.name": self.cfg.stream_name,
            "messaging.message.id": msg_id,
        }
        if job_id:
            attrs["job_id"] = str(job_id)
        if asset_id:
            attrs["asset_id"] = str(asset_id)

        return tracer.start_as_current_span(
            "worker.consume",
            context=parent_ctx,
            kind=trace.SpanKind.CONSUMER,
            links=links,
            attributes=attrs,
        )

    def _handle_job(self, job_id: int, msg_id: str) -> None:
        """Load the job row, mark it in-progress, run processing, and finalize.

        The method uses a DB row lock (SELECT ... FOR UPDATE) to claim the job
        before processing. Heavy work is performed outside the transaction.
        After processing the job row and asset state are updated and the Redis
        message is acknowledged.
        """
        with self.pg.get_pg_conn() as conn:
            cur = conn.cursor()
            cur.execute(
                "SELECT job_id, asset_id, status, attempts FROM jobs WHERE job_id = %s FOR UPDATE",
                (str(job_id),),
            )
            row = cur.fetchone()

            if not row:
                logger.error("job not found: %s", job_id)
                # Acknowledge the message to avoid repeated processing of an unknown job.
                self.redis.xack(self.cfg.stream_name, self.cfg.consumer_group, msg_id)
                return

            jid, asset_id, status, attempts = row

            if status == "done":
                logger.info("job already completed: %s", jid)
                self.redis.xack(self.cfg.stream_name, self.cfg.consumer_group, msg_id)
                return

            # Claim the job for processing and increment attempt counter.
            cur.execute(
                "UPDATE jobs SET status = 'in_progress', attempts = attempts + 1, updated_at = now() WHERE job_id = %s",
                (str(job_id),),
            )
            insert_webhook_deliveries(cur, asset_id, job_id, "job.started")
            conn.commit()

        # Run the processing outside the DB transaction.
        job_start = time.time()
        try:
            process_asset_dispatch(asset_id, self.pg, self.storage, self.cfg)
        except Exception as exc:
            logger.exception("processing failed for job=%s asset=%s", job_id, asset_id)
            wm.record_job(success=False, duration_seconds=time.time() - job_start)

            with self.pg.get_pg_conn() as conn:
                cur = conn.cursor()
                # Fetch attempts count and update job/asset state accordingly.
                cur.execute(
                    "SELECT attempts FROM jobs WHERE job_id = %s", (str(job_id),)
                )
                row = cur.fetchone()
                attempts_now = row[0] if row else 0

                # Only RetryableException is worth retrying. Any other exception
                # is permanent (bad asset type, corrupt file, decode failure) —
                # fail it immediately instead of burning the whole retry budget.
                retryable = isinstance(exc, RetryableException)

                permanent = not retryable or attempts_now >= self.cfg.redis.max_retries
                if permanent:
                    cur.execute(
                        "UPDATE jobs SET status = 'failed', last_error = %s, updated_at = now() WHERE job_id = %s",
                        (str(exc), str(job_id)),
                    )
                    cur.execute(
                        "UPDATE assets SET status = 'failed', error_reason = %s, updated_at = now() WHERE asset_id = %s",
                        (str(exc), asset_id),
                    )
                    insert_webhook_deliveries(cur, asset_id, job_id, "job.failed")
                else:
                    cur.execute(
                        "UPDATE jobs SET status = 'pending', last_error = %s, updated_at = now() WHERE job_id = %s",
                        (str(exc), str(job_id)),
                    )
                conn.commit()

            if permanent:
                # Poison message: route to the dead-letter stream with failure
                # metadata for inspection/replay, then ACK the original so it
                # stops being redelivered. (Previously the message was left
                # unacked here, so a permanently-failed job lingered in the PEL
                # and got reclaimed forever.)
                self._dead_letter(
                    msg_id,
                    {
                        "job_id": str(job_id),
                        "asset_id": str(asset_id),
                        "error": str(exc),
                        "attempts": str(attempts_now),
                        "original_msg_id": str(msg_id),
                        "failed_at": str(time.time()),
                    },
                )
            # Retryable failures are left unacked so they remain in the PEL and
            # are picked up again by XAUTOCLAIM recovery.
            return

        # On success, mark job done and mark related asset ready.
        with self.pg.get_pg_conn() as conn:
            cur = conn.cursor()
            cur.execute(
                "UPDATE jobs SET status = 'done', updated_at = now() WHERE job_id = %s",
                (str(job_id),),
            )
            cur.execute(
                "UPDATE assets SET status = 'ready', updated_at = now() WHERE asset_id = %s",
                (asset_id,),
            )
            insert_webhook_deliveries(cur, asset_id, job_id, "job.done")
            conn.commit()

        wm.record_job(success=True, duration_seconds=time.time() - job_start)

        # Acknowledge the Redis stream message.
        self.redis.xack(self.cfg.stream_name, self.cfg.consumer_group, msg_id)

    def _handle_asset_message(self, asset_id: str, msg_id: str) -> None:
        """Ensure a job row exists for the given asset and delegate to _handle_job.

        This code path is used when the producer published an `asset_id` message
        rather than a `job_id`. It creates a `process_asset` job row using a
        uniqueness constraint on (asset_id, type) and returns the job id.
        """
        with self.pg.get_pg_conn() as conn:
            cur = conn.cursor()
            cur.execute(
                "SELECT asset_id, status, content_hash FROM assets WHERE asset_id = %s FOR UPDATE",
                (asset_id,),
            )
            row = cur.fetchone()

            if not row:
                logger.error("asset not found: %s", asset_id)
                self.redis.xack(self.cfg.stream_name, self.cfg.consumer_group, msg_id)
                return

            _, status, content_hash = row

            # Only proceed for assets that were uploaded or are already processing.
            if status not in ("uploaded", "processing"):
                logger.info("skipping asset %s with status %s", asset_id, status)
                self.redis.xack(self.cfg.stream_name, self.cfg.consumer_group, msg_id)
                return

            # Insert a pending job if it does not already exist. Unique index on
            # (asset_id, type) should prevent duplicates.
            cur.execute(
                """
                INSERT INTO jobs (asset_id, type, status, created_at, updated_at)
                VALUES (%s, 'process_asset', 'pending', now(), now())
                ON CONFLICT (asset_id, type) DO NOTHING
                RETURNING job_id
                """,
                (asset_id,),
            )

            jr = cur.fetchone()
            if jr:
                job_id = jr[0]
            else:
                cur.execute(
                    "SELECT job_id FROM jobs WHERE asset_id = %s AND type = 'process_asset'",
                    (asset_id,),
                )
                job_id = cur.fetchone()[0]

            conn.commit()

        # Delegate to _handle_job using the job id we now have.
        self._handle_job(job_id, msg_id)

    def _maybe_recover(self, consumer_name: str | None = None) -> None:
        """Run stuck-job recovery if the recovery interval has elapsed.

        Time-gated so recovery fires on a fixed cadence regardless of whether
        the consumer is busy or idle.
        """
        now = time.time()
        if now - self._last_recovery >= self._recovery_interval:
            self._last_recovery = now
            self._recover_stuck_pending(consumer_name)

    def _dead_letter(self, msg_id: str, fields: Dict[str, str]) -> None:
        """Move a poison message to the dead-letter stream and ack the original.

        XADD the failure metadata to `dlq_stream_name`, then XACK the original
        message so it leaves the main stream's PEL. Best-effort: if Redis errors
        here we log and leave the message unacked (recovery will retry), rather
        than losing it.
        """
        try:
            self.redis.xadd(self.cfg.dlq_stream_name, fields)
            self.redis.xack(self.cfg.stream_name, self.cfg.consumer_group, msg_id)
            logger.warning(
                "message %s dead-lettered to %s", msg_id, self.cfg.dlq_stream_name
            )
        except redis.exceptions.RedisError:
            logger.exception("failed to dead-letter message %s", msg_id)

    def _recover_stuck_pending(self, consumer_name: str | None = None) -> None:
        """Reclaim messages stuck in the PEL of dead consumers via XAUTOCLAIM.

        Uses Redis Streams' own delivery state instead of scanning Postgres: any
        message idle longer than `recovery_min_idle_ms` (i.e. delivered to a
        consumer that never acked it — typically because that consumer crashed)
        is transferred to THIS consumer and re-dispatched through the same bounded
        pool. Idempotency is still guaranteed downstream by the `SELECT ... FOR
        UPDATE` job claim and the `status == 'done'` short-circuit in _handle_job.

        Only reclaims up to the current free capacity so recovery never overruns
        the in-flight cap; remaining stuck messages are picked up on later passes.
        """
        if consumer_name is None:
            consumer_name = getattr(self.cfg, "worker_id", None) or "recovery"

        free = self._free_capacity()
        if free <= 0:
            return

        try:
            result = self.redis.xautoclaim(
                name=self.cfg.stream_name,
                groupname=self.cfg.consumer_group,
                consumername=consumer_name,
                min_idle_time=self._recovery_min_idle_ms,
                start_id="0-0",
                count=free,
            )
        except (ResponseError, redis.exceptions.RedisError) as exc:
            logger.warning("xautoclaim recovery failed: %s", exc)
            return

        # redis-py returns (next_cursor, claimed_messages) on older versions and
        # (next_cursor, claimed_messages, deleted_ids) on newer ones.
        messages = result[1] if len(result) >= 2 else []
        # Cap on how many times a message may be reclaimed before it is treated
        # as poison. A message that keeps being reclaimed but never acked (e.g. a
        # job that crashes the worker every time) would otherwise loop forever.
        max_deliveries = int(getattr(self.cfg.redis, "max_retries", 5))
        for msg_id, fields in messages:
            if not fields:
                # Entry was deleted from the stream but lingered in the PEL; ack
                # to clear it so it stops being reported as pending.
                self.redis.xack(self.cfg.stream_name, self.cfg.consumer_group, msg_id)
                continue

            deliveries = self._delivery_count(msg_id)
            if deliveries > max_deliveries:
                dlq_fields = dict(fields)
                dlq_fields.update(
                    {
                        "original_msg_id": str(msg_id),
                        "deliveries": str(deliveries),
                        "reason": "max_deliveries_exceeded",
                        "failed_at": str(time.time()),
                    }
                )
                self._dead_letter(msg_id, dlq_fields)
                continue

            logger.info("reclaimed idle message id=%s for redispatch", msg_id)
            self._submit(msg_id, fields)

    def _delivery_count(self, msg_id: str) -> int:
        """Return how many times `msg_id` has been delivered (from XPENDING).

        Used to detect messages that are repeatedly reclaimed but never acked so
        they can be dead-lettered. Returns 0 on any error (fail open: prefer
        re-dispatch over erroneously dead-lettering).
        """
        try:
            pending = self.redis.xpending_range(
                self.cfg.stream_name,
                self.cfg.consumer_group,
                min=msg_id,
                max=msg_id,
                count=1,
            )
            if pending:
                return int(pending[0].get("times_delivered", 0))
        except redis.exceptions.RedisError as exc:
            logger.warning("xpending lookup failed for %s: %s", msg_id, exc)
        return 0
