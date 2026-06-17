from typing import Any, Dict, List, Optional

import boto3
from botocore.client import Config
from botocore.exceptions import ClientError

from worker.storage.base import StorageX


class S3Storage(StorageX):
    """S3 / S3-compatible (MinIO) storage backed by boto3.

    When ``endpoint_url`` is set, path-style addressing is used so the same
    client works against MinIO and other S3-compatible stores.
    """

    def __init__(
        self,
        bucket_name: str,
        region: str,
        access_key: str,
        secret_key: str,
        endpoint_url: Optional[str] = None,
    ):
        self.bucket_name = bucket_name
        self.region = region
        self.endpoint_url = endpoint_url or None

        self.client = boto3.client(
            "s3",
            region_name=region or None,
            aws_access_key_id=access_key or None,
            aws_secret_access_key=secret_key or None,
            endpoint_url=self.endpoint_url,
            config=Config(s3={"addressing_style": "path"}) if self.endpoint_url else None,
        )

    def upload_bytes(
        self, key: str, data: bytes, content_type: Optional[Any] = None
    ) -> None:
        extra = {"ContentType": content_type} if content_type else {}
        self.client.put_object(Bucket=self.bucket_name, Key=key, Body=data, **extra)

    def download_bytes(self, key: str) -> bytes:
        resp = self.client.get_object(Bucket=self.bucket_name, Key=key)
        return resp["Body"].read()

    def download_to_file(self, key: str, file_path: str) -> None:
        self.client.download_file(self.bucket_name, key, file_path)

    def delete(self, key: str) -> None:
        self.client.delete_object(Bucket=self.bucket_name, Key=key)

    def list_keys(self) -> List[str]:
        keys: List[str] = []
        paginator = self.client.get_paginator("list_objects_v2")
        for page in paginator.paginate(Bucket=self.bucket_name):
            keys.extend(obj["Key"] for obj in page.get("Contents", []))
        return keys

    def get_metadata(self, key: str) -> Dict[str, Any]:
        resp = self.client.head_object(Bucket=self.bucket_name, Key=key)
        return resp.get("Metadata", {})

    def exists(self, key: str) -> bool:
        try:
            self.client.head_object(Bucket=self.bucket_name, Key=key)
            return True
        except ClientError as e:
            if e.response["Error"]["Code"] in ("404", "NoSuchKey", "NotFound"):
                return False
            raise

    def public_url(self, key: str) -> str:
        if self.endpoint_url:
            # path-style for MinIO / S3-compatible endpoints
            return f"{self.endpoint_url.rstrip('/')}/{self.bucket_name}/{key}"
        return f"https://{self.bucket_name}.s3.{self.region}.amazonaws.com/{key}"
