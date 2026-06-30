from worker.consumer.config import WorkerConfig
from worker.storage.base import StorageX
from worker.storage.gcs import GCSStorage
from worker.storage.s3 import S3Storage


def get_storage(cfg: WorkerConfig) -> StorageX:
    """Construct the storage backend for the configured provider."""
    b = cfg.bucket
    if b.provider == "gcs":
        return GCSStorage(b.bucket_name, b.sa_path)
    if b.provider == "s3":
        return S3Storage(
            bucket_name=b.bucket_name,
            region=b.region,
            access_key=b.access_key,
            secret_key=b.secret_key,
            endpoint_url=b.endpoint_url,
            public_endpoint_url=b.public_endpoint_url,
        )
    raise ValueError(f"unknown storage provider: {b.provider}")
