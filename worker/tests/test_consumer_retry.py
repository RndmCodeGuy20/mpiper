import unittest
from unittest.mock import MagicMock, patch

from worker.consumer.consumer import Consumer
from worker.processing.processor import RetryableException


def _make_consumer(max_retries=3):
    cfg = MagicMock()
    cfg.stream_name = "media:jobs"
    cfg.consumer_group = "media-workers"
    cfg.redis.max_retries = max_retries
    with patch("worker.consumer.consumer.redis.Redis.from_url") as from_url:
        from_url.return_value = MagicMock()
        consumer = Consumer(
            pg_pool=MagicMock(), redis_url="redis://x", storage=MagicMock(), cfg=cfg
        )
    # SELECT job row (FOR UPDATE), then SELECT attempts in the except block.
    cursor = MagicMock()
    cursor.fetchone.side_effect = [
        (42, "asset-1", "pending", 0),  # job row: jid, asset_id, status, attempts
        (0,),                            # attempts_now (below cap)
    ]
    conn = MagicMock()
    conn.cursor.return_value = cursor
    consumer.pg.get_pg_conn.return_value.__enter__.return_value = conn
    return consumer, cursor


def _executed_sql(cursor):
    return " | ".join(c.args[0] for c in cursor.execute.call_args_list if c.args)


class TestRetryClassification(unittest.TestCase):
    """DEV-52: only RetryableException retries; other errors fail fast."""

    @patch("worker.consumer.consumer.process_asset_dispatch")
    def test_retryable_exception_requeues_below_cap(self, mock_dispatch):
        mock_dispatch.side_effect = RetryableException("not ready yet")
        consumer, cursor = _make_consumer()

        consumer._handle_job(42, "1-0")

        sql = _executed_sql(cursor)
        self.assertIn("UPDATE jobs SET status = 'pending'", sql)
        self.assertNotIn("UPDATE assets SET status = 'failed'", sql)

    @patch("worker.consumer.consumer.process_asset_dispatch")
    def test_non_retryable_exception_fails_immediately(self, mock_dispatch):
        mock_dispatch.side_effect = ValueError("Unknown asset type")
        consumer, cursor = _make_consumer()

        consumer._handle_job(42, "1-0")

        sql = _executed_sql(cursor)
        # Fails now despite attempts (0) being below the cap.
        self.assertIn("UPDATE assets SET status = 'failed'", sql)
        self.assertNotIn("UPDATE jobs SET status = 'pending'", sql)


if __name__ == "__main__":
    unittest.main()
