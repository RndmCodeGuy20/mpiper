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

logger = logging.getLogger(__name__)


def main():
    # Initialise configurations, database connections, and consumer
    logger.info("Starting worker consumer...")

    cfg = get_config()
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
    consumer.consume("media:jobs")

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
    
    # Shutdown metrics on exit
    worker_metrics.shutdown_metrics()


if __name__ == "__main__":
    main()
