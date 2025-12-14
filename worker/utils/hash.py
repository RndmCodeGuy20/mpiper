import hashlib

def compute_file_hash(file_path: str, chunk_size: int = 8 * 1024 * 1024) -> str:
    """Compute SHA256 hash of a file in chunks to handle large files."""
    hasher = hashlib.sha256()
    with open(file_path, "rb") as f:
        while chunk := f.read(chunk_size):
            hasher.update(chunk)
    return hasher.hexdigest()