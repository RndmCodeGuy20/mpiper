import logging
import unittest

from opentelemetry.sdk.trace import TracerProvider

from worker.utils.logger import TraceContextFilter


class TestTraceContextFilter(unittest.TestCase):
    """Phase 2b: log records carry the active span's trace_id/span_id."""

    def _record(self):
        return logging.LogRecord(
            name="t", level=logging.INFO, pathname=__file__, lineno=1,
            msg="hello", args=(), exc_info=None,
        )

    def test_no_span_emits_empty(self):
        f = TraceContextFilter()
        rec = self._record()
        f.filter(rec)
        self.assertEqual(rec.trace_id, "")
        self.assertEqual(rec.span_id, "")

    def test_active_span_stamps_ids(self):
        provider = TracerProvider()
        tracer = provider.get_tracer("test")
        f = TraceContextFilter()
        with tracer.start_as_current_span("s"):
            rec = self._record()
            f.filter(rec)
            self.assertEqual(len(rec.trace_id), 32)
            self.assertEqual(len(rec.span_id), 16)
            self.assertRegex(rec.trace_id, r"^[0-9a-f]{32}$")


if __name__ == "__main__":
    unittest.main()
