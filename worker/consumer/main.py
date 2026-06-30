import logging
import os
import signal
import time

from urllib.parse import quote_plus

from worker.consumer.config import get_config
from worker.consumer.consumer import Consumer
from worker.consumer.db import PgPool
from worker.consumer.migrations import run_migrations
from worker.storage import get_storage
from worker.utils import metrics as worker_metrics
from worker.utils import tracing as worker_tracing

logger = logging.getLogger(__name__)


def main():
    # Initialise configurations, database connections, and consumer
    logger.info("Starting worker consumer...")

    cfg = get_config()

    # Initialise telemetry before anything else so startup is observable.
    # NOTE: init_metrics() was previously never called — worker OTel metrics
    # were defined but never wired up. We initialise both tracing and metrics
    # here from the same OtelConfig so they share endpoint/resource/lifecycle.
    otel = cfg.otel
    try:
        worker_tracing.init_tracing(
            service_name=otel.service_name,
            service_version=otel.service_version,
            endpoint=otel.endpoint,
            deployment_env=otel.deployment_env,
            instance_id=otel.instance_id,
            tls_insecure=otel.tls_insecure,
        )
        worker_metrics.init_metrics(
            service_name=otel.service_name,
            service_version=otel.service_version,
            endpoint=otel.endpoint,
            deployment_env=otel.deployment_env,
            instance_id=otel.instance_id,
            tls_insecure=otel.tls_insecure,
        )
    except Exception:
        # Telemetry must never prevent the worker from processing jobs.
        logger.exception("failed to initialise telemetry; continuing without it")

    storage = get_storage(cfg)
    password = quote_plus(cfg.database.password)

    dsn = (
        f"postgresql://{cfg.database.user}:{password}"
        f"@{cfg.database.host}:{cfg.database.port}/{cfg.database.db_name}"
    )

    if cfg.auto_migrate:
        logger.info("AUTO_MIGRATE=true: running migrations")
        run_migrations(dsn, migrations_dir=cfg.migrations_dir)
        logger.info("Migrations applied successfully")

    # Size the DB pool to the worker concurrency. Each in-flight job holds at
    # most one connection at a time, so MAX_CONCURRENT_JOBS connections plus a
    # small headroom (recovery/bookkeeping queries) avoids jobs blocking on the
    # pool while staying well under Postgres' connection limit.
    db_pool_size = max(1, cfg.max_concurrent_jobs) + 2
    pg = PgPool(dsn=dsn, max_size=db_pool_size)
    logger.info(
        "db pool sized to %d (max_concurrent_jobs=%d + 2 headroom)",
        db_pool_size,
        cfg.max_concurrent_jobs,
    )
    consumer = Consumer(
        pg_pool=pg, storage=storage, redis_url=cfg.redis.connection_string, cfg=cfg
    )

    shutdown = False

    def _term(signum, frame):
        nonlocal shutdown
        logger.info("shutdown requested")
        shutdown = True

    signal.signal(signal.SIGINT, _term)
    signal.signal(signal.SIGTERM, _term)

    logger.info("starting job loop")
    while not shutdown:
        try:
            # consume() tops up the bounded worker pool with as many new messages
            # as there is free capacity, then returns. It returns False when the
            # pool is full or no messages were available — sleep briefly so freed
            # slots are picked up promptly without busy-spinning.
            did_work = consumer.consume(cfg.worker_id)
            if not did_work:
                time.sleep(min(cfg.job_poll_interval, 0.5))
        except Exception:
            logger.exception("unhandled error in loop")
            time.sleep(1)

    logger.info("draining in-flight jobs before exit")
    # Bounded drain: wait up to SHUTDOWN_DRAIN_TIMEOUT seconds for running jobs
    # to finish. Anything still running is abandoned (its message stays in the
    # PEL) and reclaimed by XAUTOCLAIM recovery later. Keep this <= the container
    # stop_grace_period so we shut down cleanly instead of being SIGKILLed.
    drain_timeout = float(os.getenv("SHUTDOWN_DRAIN_TIMEOUT", "30"))
    consumer.shutdown(timeout=drain_timeout)

    logger.info("exiting")

    # Shutdown telemetry on exit (flush pending spans + metrics).
    worker_tracing.shutdown_tracing()
    worker_metrics.shutdown_metrics()


if __name__ == "__main__":
    main()
