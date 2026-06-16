import time
import unittest
from unittest.mock import MagicMock, patch

from worker.consumer.consumer import Consumer


def _make_consumer():
    """Build a Consumer with redis/pg mocked out (no real connections)."""
    cfg = MagicMock()
    cfg.stream_name = "media:jobs"
    cfg.consumer_group = "media-workers"
    with patch("worker.consumer.consumer.redis.Redis.from_url") as from_url:
        client = MagicMock()
        from_url.return_value = client
        consumer = Consumer(
            pg_pool=MagicMock(), redis_url="redis://x", storage=MagicMock(), cfg=cfg
        )
    return consumer, client


class TestPeriodicRecovery(unittest.TestCase):
    """DEV-35: recovery must fire on a cadence even under continuous load."""

    def test_recovery_fires_under_load_when_interval_elapsed(self):
        consumer, client = _make_consumer()
        # A message is available (the loaded / busy path).
        client.xreadgroup.return_value = [("media:jobs", [("1-0", {"job_id": "42"})])]

        consumer._recover_stuck_pending = MagicMock()
        consumer._handle_job = MagicMock()
        consumer._recovery_interval = 0.0  # gate always open
        consumer._last_recovery = 0.0

        result = consumer.consume("worker-1")

        self.assertTrue(result)  # work was performed
        consumer._handle_job.assert_called_once_with("42", "1-0")
        consumer._recover_stuck_pending.assert_called_once()  # fired despite load

    def test_recovery_does_not_fire_before_interval_elapses(self):
        consumer, client = _make_consumer()
        client.xreadgroup.return_value = [("media:jobs", [("1-0", {"job_id": "42"})])]

        consumer._recover_stuck_pending = MagicMock()
        consumer._handle_job = MagicMock()
        consumer._recovery_interval = 9999.0
        consumer._last_recovery = time.time()  # just recovered

        consumer.consume("worker-1")

        consumer._recover_stuck_pending.assert_not_called()  # gate holds


if __name__ == "__main__":
    unittest.main()
