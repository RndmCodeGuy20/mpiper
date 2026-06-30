import unittest
from unittest.mock import patch

from worker.consumer.db import PgPool


class TestPgPoolSizing(unittest.TestCase):
    @patch("worker.consumer.db.ConnectionPool")
    def test_honours_configured_max_size(self, mock_pool):
        PgPool(dsn="postgresql://x/y", max_size=7)
        _, kwargs = mock_pool.call_args
        self.assertEqual(kwargs["max_size"], 7)
        # min_size stays at the psycopg default cap but never exceeds max_size.
        self.assertEqual(kwargs["min_size"], 4)

    @patch("worker.consumer.db.ConnectionPool")
    def test_defaults_to_ten(self, mock_pool):
        PgPool(dsn="postgresql://x/y")
        _, kwargs = mock_pool.call_args
        self.assertEqual(kwargs["max_size"], 10)

    @patch("worker.consumer.db.ConnectionPool")
    def test_clamps_to_at_least_one(self, mock_pool):
        PgPool(dsn="postgresql://x/y", max_size=0)
        _, kwargs = mock_pool.call_args
        self.assertEqual(kwargs["max_size"], 1)

    @patch("worker.consumer.db.ConnectionPool")
    def test_min_size_clamped_under_small_max(self, mock_pool):
        # Small pool (e.g. MAX_CONCURRENT_JOBS=1 -> size 3) must not have
        # min_size(4) > max_size, which psycopg rejects.
        PgPool(dsn="postgresql://x/y", max_size=3)
        _, kwargs = mock_pool.call_args
        self.assertEqual(kwargs["max_size"], 3)
        self.assertEqual(kwargs["min_size"], 3)
        self.assertLessEqual(kwargs["min_size"], kwargs["max_size"])


if __name__ == "__main__":
    unittest.main()
