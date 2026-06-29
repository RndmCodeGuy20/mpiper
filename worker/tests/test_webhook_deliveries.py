import json
import unittest
from unittest.mock import MagicMock

from worker.webhooks import insert_webhook_deliveries


class TestInsertWebhookDeliveries(unittest.TestCase):
    """Unit tests for the webhook delivery insertion helper."""

    def test_executes_correct_sql_with_params(self):
        cur = MagicMock()
        asset_id = "550e8400-e29b-41d4-a716-446655440000"
        job_id = 42
        event = "job.done"

        insert_webhook_deliveries(cur, asset_id, job_id, event)

        cur.execute.assert_called_once()
        sql, params = cur.execute.call_args[0]

        # Verify SQL structure
        self.assertIn("INSERT INTO webhook_deliveries", sql)
        self.assertIn("webhook_registrations wr", sql)
        self.assertIn("JOIN assets a ON a.owner_id = wr.user_id", sql)
        self.assertIn("wr.events @>", sql)

        # Verify params
        self.assertEqual(params[0], "job.done")       # event
        self.assertEqual(params[1], asset_id)          # asset_id
        self.assertEqual(params[2], "42")              # job_id as str
        self.assertEqual(params[4], asset_id)          # WHERE asset_id

        # Verify payload JSON
        payload = json.loads(params[3])
        self.assertEqual(payload["event"], "job.done")
        self.assertEqual(payload["asset_id"], asset_id)
        self.assertEqual(payload["job_id"], 42)
        self.assertEqual(payload["status"], "done")
        self.assertIn("timestamp", payload)

        # Verify events filter JSON
        events_filter = json.loads(params[5])
        self.assertEqual(events_filter, ["job.done"])

    def test_status_extracted_from_event(self):
        """Status field should be the part after the dot."""
        cur = MagicMock()

        for event, expected_status in [
            ("job.starting", "starting"),
            ("job.started", "started"),
            ("job.done", "done"),
            ("job.failed", "failed"),
        ]:
            cur.reset_mock()
            insert_webhook_deliveries(cur, "abc", 1, event)
            _, params = cur.execute.call_args[0]
            payload = json.loads(params[3])
            self.assertEqual(payload["status"], expected_status, f"event={event}")

    def test_handles_none_job_id(self):
        """Should not crash if job_id is None."""
        cur = MagicMock()
        insert_webhook_deliveries(cur, "abc", None, "job.starting")
        cur.execute.assert_called_once()
        _, params = cur.execute.call_args[0]
        payload = json.loads(params[3])
        self.assertIsNone(payload["job_id"])


if __name__ == "__main__":
    unittest.main()
