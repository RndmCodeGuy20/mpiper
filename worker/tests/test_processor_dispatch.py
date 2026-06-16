import unittest
from unittest.mock import MagicMock, patch

from worker.processing.processor import process_asset_dispatch, AssetStatus


class TestDispatchFailureDoesNotTouchAssetState(unittest.TestCase):
    """DEV-34: the processor must NOT mark assets.status=failed on exception.

    The consumer (_handle_job) owns the asset state transition — it only marks
    the asset failed after the retry cap. If the processor stamps 'failed' on
    every exception, retryable failures leave the asset stuck failed mid-retry.
    """

    def _make_pg_pool(self, asset_row):
        cursor = MagicMock()
        cursor.fetchone.return_value = asset_row
        conn = MagicMock()
        conn.cursor.return_value = cursor
        pg_pool = MagicMock()
        pg_pool.get_pg_conn.return_value.__enter__.return_value = conn
        return pg_pool, cursor

    @patch("worker.processing.processor.get_extension_for_mime", return_value="jpg")
    @patch("worker.processing.processor.compute_file_hash", return_value="")
    @patch("worker.processing.processor.process_image_file")
    def test_processing_failure_leaves_asset_status_untouched(
        self, mock_process_image, _mock_hash, _mock_ext
    ):
        mock_process_image.side_effect = RuntimeError("boom")

        # (asset_id, type, status, original_url, mime_type, content_hash)
        asset_row = ("asset-1", "image", "uploaded", "gs://raw/asset-1", "image/jpeg", None)
        pg_pool, cursor = self._make_pg_pool(asset_row)
        storage = MagicMock()
        cfg = MagicMock()
        cfg.temp_dir = "/tmp"

        # The exception must propagate so the consumer can decide retry vs. fail.
        with self.assertRaises(RuntimeError):
            process_asset_dispatch("asset-1", pg_pool, storage, cfg)

        # No executed statement may set the asset to 'failed'.
        for call in cursor.execute.call_args_list:
            params = call.args[1] if len(call.args) > 1 else ()
            self.assertNotIn(
                AssetStatus.FAILED.value,
                tuple(params),
                msg=f"processor wrote a failed-status update: {call.args}",
            )


if __name__ == "__main__":
    unittest.main()
