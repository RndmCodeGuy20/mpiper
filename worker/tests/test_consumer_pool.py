import threading
import time
import unittest
from unittest.mock import MagicMock, patch

from worker.consumer.consumer import Consumer


def _make_consumer(max_concurrent_jobs=2):
    cfg = MagicMock()
    cfg.stream_name = "media:jobs"
    cfg.consumer_group = "media-workers"
    cfg.max_concurrent_jobs = max_concurrent_jobs
    with patch("worker.consumer.consumer.redis.Redis.from_url") as from_url:
        client = MagicMock()
        from_url.return_value = client
        consumer = Consumer(
            pg_pool=MagicMock(), redis_url="redis://x", storage=MagicMock(), cfg=cfg
        )
    # Recovery is exercised elsewhere; stub it out here.
    consumer._recover_stuck_pending = MagicMock()
    return consumer, client


class TestBoundedPool(unittest.TestCase):
    def test_reads_only_free_capacity_and_caps_inflight(self):
        consumer, client = _make_consumer(max_concurrent_jobs=2)

        # Two messages available; both handlers block so they stay in-flight.
        client.xreadgroup.return_value = [
            ("media:jobs", [("1-0", {"job_id": "1"}), ("2-0", {"job_id": "2"})])
        ]
        release = threading.Event()
        consumer._handle_job = MagicMock(side_effect=lambda *_: release.wait(5))

        self.assertTrue(consumer.consume("w"))
        # Both slots taken -> no free capacity.
        self.assertEqual(consumer._free_capacity(), 0)
        # The read requested exactly the free capacity (2).
        self.assertEqual(client.xreadgroup.call_args.kwargs["count"], 2)

        # At capacity, consume() returns False WITHOUT issuing another read.
        client.xreadgroup.reset_mock()
        self.assertFalse(consumer.consume("w"))
        client.xreadgroup.assert_not_called()

        # Release the handlers and confirm capacity is restored.
        release.set()
        consumer._await_inflight(timeout=5)
        self.assertEqual(consumer._free_capacity(), 2)

    def test_failed_task_leaves_message_unacked(self):
        consumer, client = _make_consumer()
        client.xreadgroup.return_value = [("media:jobs", [("9-0", {"job_id": "7"})])]
        consumer._handle_job = MagicMock(side_effect=RuntimeError("boom"))

        consumer.consume("w")
        consumer._await_inflight(timeout=5)

        client.xack.assert_not_called()

    def test_malformed_message_acked_by_msg_id(self):
        consumer, client = _make_consumer()
        # Neither job_id nor asset_id -> malformed; wrapper acks to drop it.
        client.xreadgroup.return_value = [("media:jobs", [("5-0", {"foo": "bar"})])]

        consumer.consume("w")
        consumer._await_inflight(timeout=5)

        client.xack.assert_called_once_with("media:jobs", "media-workers", "5-0")

    def test_graceful_drain_waits_for_inflight(self):
        consumer, client = _make_consumer()
        client.xreadgroup.return_value = [("media:jobs", [("1-0", {"job_id": "1"})])]
        done = threading.Event()

        def slow(*_):
            time.sleep(0.3)
            done.set()

        consumer._handle_job = MagicMock(side_effect=slow)
        consumer.consume("w")

        consumer.shutdown(timeout=5)
        # A bounded-but-sufficient drain lets the in-flight job finish.
        self.assertTrue(done.is_set())


if __name__ == "__main__":
    unittest.main()
