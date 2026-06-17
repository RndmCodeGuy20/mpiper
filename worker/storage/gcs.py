from worker.storage.base import StorageX
from typing import Any, Dict, List, Optional
from google.cloud import storage
from google.oauth2 import service_account


def _create_gcs_client(sa_path: str) -> storage.Client:
    creds = service_account.Credentials.from_service_account_file(
        sa_path,
        scopes=["https://www.googleapis.com/auth/cloud-platform"],
    )

    return storage.Client(credentials=creds, project=creds.project_id)


class GCSStorage(StorageX):
    def __init__(self, bucket_name: str, sa_path: str):
        self.client = _create_gcs_client(sa_path)
        self.bucket = self.client.bucket(bucket_name)
        self.bucket_name = bucket_name

    def upload_bytes(
        self, key: str, data: bytes, content_type: Optional[Any] = None
    ) -> None:
        blob = self.bucket.blob(key)
        blob.upload_from_string(data, content_type=content_type)

    def download_bytes(self, key: str) -> bytes:
        blob = self.bucket.blob(key)
        return blob.download_as_bytes()

    def download_to_file(self, key: str, file_path: str) -> None:
        blob = self.bucket.blob(key)
        blob.download_to_filename(file_path)

    def delete(self, key: str) -> None:
        blob = self.bucket.blob(key)
        blob.delete()

    def list_keys(self) -> List[str]:
        return [blob.name for blob in self.client.list_blobs(self.bucket)]

    def get_metadata(self, key: str) -> Dict[str, Any]:
        blob = self.bucket.blob(key)
        blob.reload()
        return blob.metadata or {}

    def exists(self, key: str) -> bool:
        blob = self.bucket.blob(key)
        return blob.exists()

    def public_url(self, key: str) -> str:
        return f"https://storage.googleapis.com/{self.bucket_name}/{key}"
