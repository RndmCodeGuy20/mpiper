from psycopg_pool import ConnectionPool
from contextlib import contextmanager

class PgPool:
    def __init__(self, dsn):
        self._pool = ConnectionPool(conninfo=dsn, max_size=10, open=True)

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
