import unittest
from unittest.mock import MagicMock, patch

from opentelemetry.propagate import set_global_textmap
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import SimpleSpanProcessor
from opentelemetry.sdk.trace.export.in_memory_span_exporter import (
    InMemorySpanExporter,
)
from opentelemetry.trace.propagation.tracecontext import (
    TraceContextTextMapPropagator,
)

from worker.consumer.consumer import Consumer


TRACE_ID_HEX = "0af7651916cd43dd8448eb211c80319c"
SPAN_ID_HEX = "b7ad6b7169203331"
TRACEPARENT = f"00-{TRACE_ID_HEX}-{SPAN_ID_HEX}-01"


def _make_consumer():
    cfg = MagicMock()
    cfg.stream_name = "media:jobs"
    cfg.consumer_group = "media-workers"
    with patch("worker.consumer.consumer.redis.Redis.from_url") as from_url:
        from_url.return_value = MagicMock()
        consumer = Consumer(
            pg_pool=MagicMock(), redis_url="redis://x", storage=MagicMock(), cfg=cfg
        )
    return consumer, from_url.return_value


class TestConsumeSpanPropagation(unittest.TestCase):
    """Phase 1d: the worker continues the producer trace across the queue."""

    def setUp(self):
        set_global_textmap(TraceContextTextMapPropagator())
        self.exporter = InMemorySpanExporter()
        provider = TracerProvider()
        provider.add_span_processor(SimpleSpanProcessor(self.exporter))
        self.tracer = provider.get_tracer("test")

    def _run_consume_with(self, fields):
        consumer, client = _make_consumer()
        client.xreadgroup.return_value = [("media:jobs", [("1-0", fields)])]
        consumer._recover_stuck_pending = MagicMock()
        consumer._handle_job = MagicMock()
        consumer._handle_asset_message = MagicMock()
        with patch(
            "worker.consumer.consumer.get_tracer", return_value=self.tracer
        ):
            consumer.consume("worker-1")
        return consumer

    def test_consume_span_is_child_and_linked_to_producer(self):
        consumer = self._run_consume_with({"job_id": "42", "traceparent": TRACEPARENT})
        consumer._handle_job.assert_called_once_with("42", "1-0")

        spans = self.exporter.get_finished_spans()
        consume = next(s for s in spans if s.name == "worker.consume")

        expected_trace_id = int(TRACE_ID_HEX, 16)
        expected_span_id = int(SPAN_ID_HEX, 16)

        # Child: parent is the producer context, and the span continues the trace.
        self.assertIsNotNone(consume.parent)
        self.assertEqual(consume.parent.trace_id, expected_trace_id)
        self.assertEqual(consume.parent.span_id, expected_span_id)
        self.assertEqual(consume.context.trace_id, expected_trace_id)

        # Link: queue fan-in primitive points at the same producer context.
        self.assertTrue(consume.links)
        self.assertEqual(consume.links[0].context.trace_id, expected_trace_id)

    def test_consume_without_traceparent_starts_new_trace(self):
        consumer = self._run_consume_with({"job_id": "42"})
        consumer._handle_job.assert_called_once_with("42", "1-0")
        spans = self.exporter.get_finished_spans()
        consume = next(s for s in spans if s.name == "worker.consume")
        # No producer context -> root span, no link.
        self.assertIsNone(consume.parent)
        self.assertFalse(consume.links)

    def test_traceparent_merged_from_body(self):
        import json

        body = json.dumps({"job_id": "42"})
        consumer = self._run_consume_with({"body": body, "traceparent": TRACEPARENT})
        consumer._handle_job.assert_called_once_with("42", "1-0")
        spans = self.exporter.get_finished_spans()
        consume = next(s for s in spans if s.name == "worker.consume")
        self.assertEqual(consume.parent.trace_id, int(TRACE_ID_HEX, 16))


class TestTracerInit(unittest.TestCase):
    def test_init_tracing_sets_tracer(self):
        import worker.utils.tracing as tracing

        # Reset module state for a clean init.
        tracing._tracer = None
        tracing._provider = None
        tracing.init_tracing(endpoint="otel-collector:4317", deployment_env="local")
        self.assertIsNotNone(tracing.get_tracer())
        tracing.shutdown_tracing()


if __name__ == "__main__":
    unittest.main()
