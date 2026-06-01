"""
Minimal SQL migration runner for the Python worker.

Reads *.up.sql files from MIGRATIONS_DIR (or a given path) in version order,
tracks applied versions in a schema_migrations table, and applies any that
have not yet run. Safe to call on every startup.
"""

import os
import re
from pathlib import Path

import psycopg

_TRACKING_TABLE = """
CREATE TABLE IF NOT EXISTS schema_migrations (
    version     TEXT        PRIMARY KEY,
    applied_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
)
"""


def _migration_files(migrations_dir: Path):
    """Return (version, path) pairs for *.up.sql files, sorted by version."""
    files = sorted(migrations_dir.glob("*.up.sql"))
    result = []
    for f in files:
        m = re.match(r"^(\d+)", f.name)
        if m:
            result.append((m.group(1), f))
    return result


def run_migrations(dsn: str, migrations_dir: str | None = None) -> None:
    """Apply all pending migrations from migrations_dir against the given DSN."""
    if migrations_dir is None:
        migrations_dir = os.getenv(
            "MIGRATIONS_DIR",
            str(Path(__file__).resolve().parents[2] / "internal" / "database" / "migrations"),
        )

    path = Path(migrations_dir)
    if not path.is_dir():
        raise RuntimeError(f"Migrations directory not found: {path}")

    with psycopg.connect(dsn) as conn:
        conn.autocommit = True
        with conn.cursor() as cur:
            cur.execute(_TRACKING_TABLE)

        pending = _migration_files(path)

        for version, sql_file in pending:
            with conn.cursor() as cur:
                cur.execute(
                    "SELECT 1 FROM schema_migrations WHERE version = %s", (version,)
                )
                if cur.fetchone():
                    continue

            sql = sql_file.read_text()
            with conn.transaction():
                with conn.cursor() as cur:
                    cur.execute(sql)
                    cur.execute(
                        "INSERT INTO schema_migrations (version) VALUES (%s)", (version,)
                    )
