from psycopg_pool import ConnectionPool
from contextlib import contextmanager


class PgPool:
    """Thin wrapper over psycopg_pool.ConnectionPool.

    `max_size` must be sized to the worker concurrency: each in-flight job holds
    at most one connection at a time, so the pool needs at least
    MAX_CONCURRENT_JOBS connections plus a little headroom for recovery/recovery
    bookkeeping queries that may run alongside job processing. Under-sizing the
    pool would silently cap effective concurrency (jobs would block waiting for a
    connection) — watch mpiper_db_connections_wait_count if you suspect this.
    """

    def __init__(self, dsn, max_size: int = 10):
        size = max(1, int(max_size))
        # psycopg_pool defaults min_size=4; clamp it under max_size so small
        # pools (low MAX_CONCURRENT_JOBS) don't violate min_size <= max_size.
        min_size = min(4, size)
        self._pool = ConnectionPool(
            conninfo=dsn, min_size=min_size, max_size=size, open=True
        )

    @contextmanager
    def get_pg_conn(self):
        conn = self._pool.getconn()
        try:
            yield conn
            conn.commit()
        except:
            conn.rollback()
            raise
        finally:
            self._pool.putconn(conn)
