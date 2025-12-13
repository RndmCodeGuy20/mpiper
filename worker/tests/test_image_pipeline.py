import unittest
from unittest.mock import MagicMock, patch
from PIL import Image
import io

from worker.processing.images import process_image_file


class StorageMock:
    def __init__(self):
        self.calls = []

    def upload_bytes(self, key, data, content_type=None):
        self.calls.append((key, len(data), content_type))
        # return fake URL
        return f"https://mock/{key}"


class TestProcessImageFile(unittest.TestCase):
    def setUp(self):
        # create an in-memory image (no file IO at all)
        self.test_img = Image.new("RGB", (800, 600), color="blue")
        self.raw_img_bytes = io.BytesIO()
        self.test_img.save(self.raw_img_bytes, format="JPEG")
        self.raw_img_bytes.seek(0)

    @patch("worker.processing.images.Image.open")
    def test_process_image_file(self, mock_image_open):
        """Fully isolated test: no DB, no filesystem, no real storage."""

        # ---------------------
        # Mock Image.open
        # ---------------------
        mock_image_open.return_value.__enter__.return_value = self.test_img

        # ---------------------
        # Mock PgPool
        # ---------------------
        # mock connection cursor
        mock_cursor = MagicMock()
        mock_cursor.fetchone.return_value = None  # means no variant exists yet

        # mock connection
        mock_conn = MagicMock()
        mock_conn.cursor.return_value = mock_cursor

        # mock pg_pool.get_pg_conn() context manager
        mock_pg_pool = MagicMock()
        mock_pg_pool.get_pg_conn.return_value.__enter__.return_value = mock_conn

        # ---------------------
        # Mock storage
        # ---------------------
        storage = StorageMock()

        # ---------------------
        # Dummy cfg (not used in function)
        # ---------------------
        cfg = MagicMock()

        # ---------------------
        # Call function under test
        # ---------------------
        process_image_file(
            asset_id="test-123",
            local_raw_path="dummy.jpg",
            pg_pool=mock_pg_pool,
            storage=storage,
            cfg=cfg
        )

        # ---------------------
        # Assertions
        # ---------------------

        # 1. Image.open was used correctly
        mock_image_open.assert_called_once_with("dummy.jpg")

        # 2. DB UPDATE for metadata was called
        self.assertTrue(mock_cursor.execute.called)

        # 3. Storage upload was called for all 3 variants
        self.assertEqual(len(storage.calls), 3)
        for key, size, content_type in storage.calls:
            self.assertIn("media/processed/test-123/img/", key)
            self.assertTrue(size > 0)
            self.assertIn("image/", content_type)

        # 4. Correct roles
        uploaded_roles = [call[0].split("/")[-1].split(".")[0] for call in storage.calls]
        self.assertCountEqual(uploaded_roles, ["thumbnail", "display_small", "display_large"])


if __name__ == "__main__":
    unittest.main()
