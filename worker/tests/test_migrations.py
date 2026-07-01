"""
Tests for the Python migration runner.

These tests use mocks for ``psycopg.connect`` so they can run without a live
PostgreSQL instance. The destructive-migration gate must short-circuit before
opening any database connection, so a refusal test can use an obviously
invalid DSN.
"""

from unittest import mock

import pytest

from worker.consumer.migrations import run_migrations


def _write_migration(tmp_path, name: str, body: str = "-- stub") -> None:
    (tmp_path / name).write_text(body)


def test_run_migrations_refuses_destructive_when_disabled(tmp_path):
    """Versions 7 and 8 must be refused unless allow_destructive=True.

    The check must run against the file-system pending list before any
    database connection is opened, so an obviously invalid DSN is fine and
    psycopg.connect must never be invoked.
    """
    _write_migration(tmp_path, "000001_init.up.sql")
    _write_migration(tmp_path, "000006_api_keys.up.sql")
    _write_migration(tmp_path, "000007_split_webhook_key.up.sql")
    _write_migration(tmp_path, "000008_assets_owner_not_null.up.sql")

    with mock.patch("worker.consumer.migrations.psycopg.connect") as mock_connect:
        with pytest.raises(RuntimeError, match="destructive migrations"):
            run_migrations(
                dsn="postgresql://invalid:invalid@127.0.0.1:1/invalid",
                migrations_dir=str(tmp_path),
                allow_destructive=False,
            )

    mock_connect.assert_not_called()


def test_run_migrations_refuses_when_only_destructive_is_pending(tmp_path):
    """A single pending destructive version is enough to abort."""
    _write_migration(tmp_path, "000007_split_webhook_key.up.sql")

    with mock.patch("worker.consumer.migrations.psycopg.connect") as mock_connect:
        with pytest.raises(RuntimeError, match=r"\['000007'\]"):
            run_migrations(
                dsn="postgresql://invalid",
                migrations_dir=str(tmp_path),
                allow_destructive=False,
            )

    mock_connect.assert_not_called()


def test_run_migrations_allows_destructive_with_flag(tmp_path, monkeypatch):
    """With allow_destructive=True, the runner proceeds past the gate.

    A fully mocked connection is used so the test does not need a real
    PostgreSQL server. The runner only needs to make it through the gate;
    per-migration application is covered by the integration smoke test.
    """
    monkeypatch.delenv("MIGRATION_ALLOW_DESTRUCTIVE", raising=False)
    _write_migration(tmp_path, "000007_split_webhook_key.up.sql")
    _write_migration(tmp_path, "000008_assets_owner_not_null.up.sql")

    # `with psycopg.connect(dsn) as conn:` resolves to:
    #   ctx = psycopg.connect(dsn); conn = ctx.__enter__()
    # so the runner operates on mock_connect.return_value.__enter__().return_value.
    # Pre-mark every pending version as already applied so no SQL executes
    # and the destructive gate is the only thing under test.
    mock_connect = mock.MagicMock()
    mock_conn = mock_connect.return_value.__enter__.return_value
    mock_cursor = mock_conn.cursor.return_value.__enter__.return_value
    mock_cursor.fetchone.return_value = (1,)

    with mock.patch(
        "worker.consumer.migrations.psycopg.connect", mock_connect
    ):
        run_migrations(
            dsn="postgresql://stub",
            migrations_dir=str(tmp_path),
            allow_destructive=True,
        )

    mock_conn.cursor.assert_called()
