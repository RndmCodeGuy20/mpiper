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

Notes about external expectations:
- `pg_pool` must expose `connect_pg()` returning a DB connection context manager
  compatible with `psycopg` (connection yields `cursor()` and supports commit/rollback).
- `storage` implements the storage client used by the processing logic.
- `cfg` must provide stream_name, consumer_group, MAX_JOB_ATTEMPTS and other
  configuration values referenced below.

"""

from __future__ import annotations

from typing import Dict

import redis
from redis.exceptions import ResponseError

from worker.consumer.config import WorkerConfig
from worker.consumer.db import PgPool
from worker.processing.processor import process_asset_dispatch
from worker.storage.base import StorageX
from worker.utils.logger import get_logger

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

    def __init__(self, pg_pool: PgPool, redis_url: str, storage: StorageX, cfg: WorkerConfig) -> None:
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
        self.redis = redis.Redis.from_url(redis_url, decode_responses=True)
        self.storage = storage
        self.cfg = cfg

        # Ensure the consumer group exists. If it already exists Redis raises an
        # error; ignore that specific error.
        try:
            self.redis.xgroup_create(self.cfg.stream_name, self.cfg.consumer_group, id="$", mkstream=True)
        except ResponseError as exc:
            logger.debug("consumer group exists or cannot be created: %s", exc)

    def consume(self, consumer_name: str) -> bool:
        """Poll the stream and process a single message.

        This blocks briefly while waiting for messages. When a message is returned, 
        it can contain either `job_id` or `asset_id` in its payload. `job_id` is
        preferred; if `asset_id` is present, the method ensures a job row exists
        before delegating to the job handler.

        Parameters
        ----------
        consumer_name:
            Consumer identifier used for the Redis consumer group.

        Returns
        -------
        bool
            True if a message was consumed (even if processing failed), False if
            no messages were available.
        """
        # Read one message for this consumer (blocking short period)
        resp = self.redis.xreadgroup(
            groupname=self.cfg.consumer_group,
            consumername=consumer_name,
            streams={self.cfg.stream_name: ">"},
            count=1,
            block=5000,
        )

        if not resp:
            # No messages available; attempt recovery of stuck jobs and return.
            self._recover_stuck_pending()
            return False

        # Response format: [(stream_name, [(msg_id, {field: value}), ...])]
        _, messages = resp[0]
        msg_id, fields = messages[0]

        try:
            # Normalize fields to a dict
            payload: Dict[str, str] = {k: fields[k] for k in fields}
            logger.info("message received id=%s payload=%s", msg_id, payload)

            body = payload.get("body")
            # logger.debug("message body: %s", body)
            if body:
                # If a body field is present, it contains a JSON-encoded dict
                import json

                body_dict = json.loads(body)
                payload.update(body_dict)
                payload.pop('body')

            # logger.debug("normalized payload: %s", payload)

            job_id = payload.get("job_id")
            asset_id = payload.get("asset_id")

            if job_id:
                self._handle_job(job_id, msg_id)
            elif asset_id:
                self._handle_asset_message(asset_id, msg_id)
            else:
                logger.error("message missing job_id and asset_id: %s", payload)
                # Acknowledge to remove the malformed message from the stream.
                self.redis.xack(self.cfg.stream_name, self.cfg.consumer_group, msg_id)
        except Exception:
            logger.exception("unhandled exception while processing message %s", msg_id)
            # Do not ack the message so it remains in the pending entries list
            # for recovery/retry later.

        return True

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
            conn.commit()

        # Run the processing outside the DB transaction.
        try:
            process_asset_dispatch(asset_id, self.pg, self.storage, self.cfg)
        except Exception as exc:
            logger.exception("processing failed for job=%s asset=%s", job_id, asset_id)

            with self.pg.get_pg_conn() as conn:
                cur = conn.cursor()
                # Fetch attempts count and update job/asset state accordingly.
                cur.execute("SELECT attempts FROM jobs WHERE job_id = %s", (str(job_id),))
                row = cur.fetchone()
                attempts_now = row[0] if row else 0

                if attempts_now >= self.cfg.redis.max_retries:
                    cur.execute(
                        "UPDATE jobs SET status = 'failed', last_error = %s, updated_at = now() WHERE job_id = %s",
                        (str(exc), str(job_id)),
                    )
                    cur.execute(
                        "UPDATE assets SET status = 'failed', error_reason = %s, updated_at = now() WHERE asset_id = %s",
                        (str(exc), asset_id),
                    )
                else:
                    cur.execute(
                        "UPDATE jobs SET status = 'pending', last_error = %s, updated_at = now() WHERE job_id = %s",
                        (str(exc), str(job_id)),
                    )
            # Leave the Redis message unacked so it remains in the pending list.
            return

        # On success, mark job done and mark related asset ready.
        with self.pg.get_pg_conn() as conn:
            cur = conn.cursor()
            cur.execute("UPDATE jobs SET status = 'done', updated_at = now() WHERE job_id = %s", (str(job_id),))
            cur.execute("UPDATE assets SET status = 'ready', updated_at = now() WHERE asset_id = %s", (asset_id,))

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
            cur.execute("SELECT asset_id, status, content_hash FROM assets WHERE asset_id = %s FOR UPDATE", (asset_id,))
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
                cur.execute("SELECT job_id FROM jobs WHERE asset_id = %s AND type = 'process_asset'", (asset_id,))
                job_id = cur.fetchone()[0]

            conn.commit()

        # Delegate to _handle_job using the job id we now have.
        self._handle_job(job_id, msg_id)

    def _recover_stuck_pending(self) -> None:
        """Requeue stale pending/in_progress jobs back onto the stream.

        This is a conservative recovery strategy: find jobs that appear stuck
        (older than a configured threshold) and push a message for each back to
        the Redis stream so consumer groups can pick them up again.
        """
        with self.pg.get_pg_conn() as conn:
            cur = conn.cursor()
            cur.execute(
                "SELECT job_id, asset_id, status FROM jobs WHERE status IN ('pending','in_progress') AND updated_at < now() - interval '2 minutes'",
            )
            rows = cur.fetchall()

            for jid, asset_id, status in rows:
                logger.info("requeueing job %s asset %s status %s", jid, asset_id, status)
                payload = {"job_id": str(jid), "asset_id": str(asset_id)}
                # XADD will append a new message; deduping is handled by the jobs table.
                self.redis.xadd(self.cfg.stream_name, payload)
