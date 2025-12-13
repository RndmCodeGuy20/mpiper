import boto3
from botocore.exceptions import ClientError
from typing import Optional, List, Dict, Any
from worker.storage.base import StorageX


class S3Storage(StorageX):
    def __init__(self, bucket: str, region: str = "us-east-1"):
        self.bucket = bucket
        self.s3 = boto3.client("s3", region_name=region)

    def upload_bytes(self, key: str, data: bytes, content_type: Optional[str] = None) -> str:
        params = {"Bucket": self.bucket, "Key": key, "Body": data}
        if content_type:
            params["ContentType"] = content_type

        self.s3.put_object(**params)

        # simplest deterministic public URL (works if bucket is public)
        return f"https://{self.bucket}.s3.amazonaws.com/{key}"

    def download_bytes(self, key: str) -> bytes:
        resp = self.s3.get_object(Bucket=self.bucket, Key=key)
        return resp["Body"].read()

    def delete(self, key: str) -> None:
        self.s3.delete_object(Bucket=self.bucket, Key=key)

    def list_keys(self) -> List[str]:
        paginator = self.s3.get_paginator("list_objects_v2")
        page_iterator = paginator.paginate(Bucket=self.bucket)

        keys = []
        for page in page_iterator:
            for obj in page.get("Contents", []):
                keys.append(obj["Key"])
        return keys

    def get_metadata(self, key: str) -> Dict[str, Any]:
        resp = self.s3.head_object(Bucket=self.bucket, Key=key)
        return resp.get("Metadata", {})

    def exists(self, key: str) -> bool:
        try:
            self.s3.head_object(Bucket=self.bucket, Key=key)
            return True
        except ClientError as e:
            if e.response["Error"]["Code"] in ("404", "NoSuchKey"):
                return False
            raise
