from abc import ABC, abstractmethod
from typing import Any, Dict, List, Optional


class StorageX(ABC):
    @abstractmethod
    def upload_bytes(
        self, key: str, data: bytes, content_type: Optional[Any] = None
    ) -> None:
        """Upload bytes data to storage with the given key and optional content type."""
        pass

    @abstractmethod
    def download_bytes(self, key: str) -> bytes:
        """Download bytes data from storage by its key."""
        pass

    @abstractmethod
    def download_to_file(self, key: str, file_path: str) -> None:
        """Download data from storage by its key to a local file."""
        pass

    @abstractmethod
    def delete(self, key: str) -> None:
        """Delete a value by its key."""
        pass

    @abstractmethod
    def list_keys(self) -> List[str]:
        """List all keys stored."""
        pass

    @abstractmethod
    def get_metadata(self, key: str) -> Dict[str, Any]:
        """Get metadata for a given key."""
        pass

    @abstractmethod
    def exists(self, key: str) -> bool:
        """Check if a key exists in storage."""
        pass
