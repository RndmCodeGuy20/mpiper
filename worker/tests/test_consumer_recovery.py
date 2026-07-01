import time
import unittest
from unittest.mock import MagicMock, patch

from worker.consumer.consumer import Consumer


def _make_consumer():
    """Build a Consumer with redis/pg mocked out (no real connections)."""
    cfg = MagicMock()
    cfg.stream_name = "media:jobs"
    cfg.consumer_group = "media-workers"
    cfg.max_concurrent_jobs = 2
    cfg.redis.max_retries = 5
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
        consumer._await_inflight(timeout=5)

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


class TestXAutoClaimRecovery(unittest.TestCase):
    """Recovery now reclaims idle PEL messages via XAUTOCLAIM and re-dispatches."""

    def test_reclaims_idle_messages_and_redispatches(self):
        consumer, client = _make_consumer()
        consumer._handle_job = MagicMock()
        # (next_cursor, claimed_messages, deleted_ids)
        client.xautoclaim.return_value = (
            "0-0",
            [("1-0", {"job_id": "5"})],
            [],
        )

        consumer._recover_stuck_pending("worker-1")
        consumer._await_inflight(timeout=5)

        # Claimed with the configured min-idle and bounded by free capacity.
        kwargs = client.xautoclaim.call_args.kwargs
        self.assertEqual(kwargs["min_idle_time"], consumer._recovery_min_idle_ms)
        self.assertEqual(kwargs["consumername"], "worker-1")
        self.assertEqual(kwargs["count"], 2)  # free capacity
        # Reclaimed message dispatched through the pool.
        consumer._handle_job.assert_called_once_with("5", "1-0")

    def test_skips_when_no_free_capacity(self):
        consumer, client = _make_consumer()
        # Saturate in-flight so there is no capacity to reclaim into.
        with consumer._inflight_lock:
            consumer._inflight = consumer._max_workers

        consumer._recover_stuck_pending("worker-1")

        client.xautoclaim.assert_not_called()

    def test_acks_tombstoned_entries(self):
        consumer, client = _make_consumer()
        # A claimed entry whose data was deleted from the stream (fields None).
        client.xautoclaim.return_value = ("0-0", [("7-0", None)], [])

        consumer._recover_stuck_pending("worker-1")
        consumer._await_inflight(timeout=5)

        client.xack.assert_called_once_with("media:jobs", "media-workers", "7-0")


if __name__ == "__main__":
    unittest.main()
