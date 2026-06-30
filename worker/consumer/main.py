import logging
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

    pg = PgPool(dsn=dsn)
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
            processed = consumer.consume(
                cfg.stream_name
            )  # single iteration --- returns True if did work
            if not processed:
                time.sleep(cfg.job_poll_interval)
        except Exception:
            logger.exception("unhandled error in loop")
            time.sleep(1)

    logger.info("exiting")

    # Shutdown telemetry on exit (flush pending spans + metrics).
    worker_tracing.shutdown_tracing()
    worker_metrics.shutdown_metrics()


if __name__ == "__main__":
    main()
