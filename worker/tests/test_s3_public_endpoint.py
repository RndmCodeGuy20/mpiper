import unittest

from worker.consumer.config import BucketConfig
from worker.storage.s3 import S3Storage


class TestS3PublicEndpoint(unittest.TestCase):
    """The worker writes variant URLs to the DB; those must use the
    client-facing public endpoint so they resolve from the host/browser,
    while object I/O keeps targeting the internal endpoint.
    """

    def _make(self, endpoint_url, public_endpoint_url):
        return S3Storage(
            bucket_name="mpiper",
            region="us-east-1",
            access_key="minioadmin",
            secret_key="minioadmin",
            endpoint_url=endpoint_url,
            public_endpoint_url=public_endpoint_url,
        )

    def test_public_url_uses_public_endpoint(self):
        st = self._make("http://minio:9000", "http://localhost:9000")
        url = st.public_url("media/tenant-abc/processed/abc/thumbnail.webp")
        self.assertEqual(
            url, "http://localhost:9000/mpiper/media/tenant-abc/processed/abc/thumbnail.webp"
        )
        # Internal I/O still targets the private endpoint.
        self.assertEqual(st.client.meta.endpoint_url, "http://minio:9000")

    def test_public_url_falls_back_to_internal_when_unset(self):
        st = self._make("http://minio:9000", None)
        url = st.public_url("media/tenant-xyz/raw/xyz")
        self.assertEqual(url, "http://minio:9000/mpiper/media/tenant-xyz/raw/xyz")

    def test_bucket_config_defaults_public_to_internal(self, ):
        import os
        from unittest.mock import patch

        env = {
            "BUCKET_PROVIDER": "s3",
            "S3_BUCKET_NAME": "mpiper",
            "S3_ENDPOINT_URL": "http://minio:9000",
        }
        with patch.dict(os.environ, env, clear=True):
            b = BucketConfig.from_env()
        self.assertEqual(b.endpoint_url, "http://minio:9000")
        self.assertEqual(b.public_endpoint_url, "http://minio:9000")

    def test_bucket_config_reads_public_endpoint(self):
        import os
        from unittest.mock import patch

        env = {
            "BUCKET_PROVIDER": "s3",
            "S3_BUCKET_NAME": "mpiper",
            "S3_ENDPOINT_URL": "http://minio:9000",
            "S3_PUBLIC_ENDPOINT_URL": "http://localhost:9000",
        }
        with patch.dict(os.environ, env, clear=True):
            b = BucketConfig.from_env()
        self.assertEqual(b.public_endpoint_url, "http://localhost:9000")


if __name__ == "__main__":
    unittest.main()
