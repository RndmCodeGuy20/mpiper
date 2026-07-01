import unittest
from unittest.mock import MagicMock, patch

from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import SimpleSpanProcessor
from opentelemetry.sdk.trace.export.in_memory_span_exporter import (
    InMemorySpanExporter,
)
from opentelemetry import trace

import worker.processing.processor as processor


class TestPipelineStageSpans(unittest.TestCase):
    """Phase 2a: dispatch emits download + dedup (+ delegate) spans."""

    def setUp(self):
        self.exporter = InMemorySpanExporter()
        provider = TracerProvider()
        provider.add_span_processor(SimpleSpanProcessor(self.exporter))
        # Point the module-level proxy tracer at our in-memory provider.
        self._tracer = provider.get_tracer("test")
        self._orig = processor.tracer
        processor.tracer = self._tracer

    def tearDown(self):
        processor.tracer = self._orig

    def _pg_pool_returning(self, asset_row):
        cursor = MagicMock()
        cursor.fetchone.return_value = asset_row
        conn = MagicMock()
        conn.cursor.return_value = cursor
        pg = MagicMock()
        pg.get_pg_conn.return_value.__enter__.return_value = conn
        return pg

    @patch("worker.processing.processor.get_extension_for_mime", return_value="jpg")
    @patch("worker.processing.processor.compute_file_hash", return_value="")
    @patch("worker.processing.processor.process_image_file")
    @patch("worker.processing.processor.os.path.exists", return_value=False)
    def test_image_dispatch_emits_stage_spans(
        self, _exists, mock_img, _hash, _ext
    ):
        asset_row = ("a1", "image", "uploaded", "u", "image/jpeg", None, "tenant-1")
        pg = self._pg_pool_returning(asset_row)
        storage = MagicMock()
        cfg = MagicMock()
        cfg.temp_dir = "/tmp"

        processor.process_asset_dispatch("a1", pg, storage, cfg)

        names = {s.name for s in self.exporter.get_finished_spans()}
        self.assertIn("process.dispatch", names)
        self.assertIn("process.download", names)
        mock_img.assert_called_once()

    @patch("worker.processing.processor.get_extension_for_mime", return_value="jpg")
    @patch("worker.processing.processor.compute_file_hash", return_value="abc123")
    @patch("worker.processing.processor.check_for_duplicate")
    @patch("worker.processing.processor.process_image_file")
    @patch("worker.processing.processor.os.path.exists", return_value=False)
    def test_dedup_span_emitted_when_hash_present(
        self, _exists, mock_img, mock_dedup, _hash, _ext
    ):
        mock_dedup.return_value = processor.DedupResult.NO_DUPLICATE
        asset_row = ("a1", "image", "uploaded", "u", "image/jpeg", None, "tenant-1")
        pg = self._pg_pool_returning(asset_row)

        processor.process_asset_dispatch("a1", pg, MagicMock(), MagicMock(temp_dir="/tmp"))

        names = {s.name for s in self.exporter.get_finished_spans()}
        self.assertIn("process.dedup_check", names)


if __name__ == "__main__":
    unittest.main()
