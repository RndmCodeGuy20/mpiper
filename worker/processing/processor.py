import os
from enum import Enum

from worker.consumer.config import WorkerConfig
from worker.consumer.db import PgPool
from worker.processing.images import process_image_file
from worker.processing.videos import process_video_file
from worker.storage.base import StorageX
from worker.utils.hash import compute_file_hash
from worker.utils.logger import get_logger

logger = get_logger(__name__)


class AssetStatus(Enum):
    PENDING = "pending"
    PROCESSING = "processing"
    READY = "ready"
    FAILED = "failed"
    DUPLICATE = "duplicate"


class DedupResult(Enum):
    NO_DUPLICATE = "no_duplicate"
    DUPLICATE_READY = "duplicate_ready"
    DUPLICATE_PENDING = "duplicate_pending"


class RetryableException(Exception):
    """Raised when operation should be retried later"""
    pass


def get_extension_for_mime(mime_type: str) -> str:
    mapping = {
        "image/jpeg": "jpg",
        "image/png": "png",
        "image/webp": "webp",
        "video/mp4": "mp4",
        "video/quicktime": "mov",
    }
    return mapping.get(mime_type, "bin")


def check_for_duplicate(
        content_hash: str,
        new_asset_id: str,
        pg_pool: PgPool
) -> DedupResult:
    """
    Check if content_hash already exists and handle appropriately.

    Returns:
        - NO_DUPLICATE: No existing asset, proceed with processing
        - DUPLICATE_READY: Found ready duplicate, cloned variants
        - DUPLICATE_PENDING: Found pending duplicate, retry later
    """
    with pg_pool.get_pg_conn() as conn:
        with conn.transaction():
            cur = conn.cursor()

            # Find canonical asset (oldest ready asset with this hash)
            cur.execute(
                """
                SELECT asset_id, type, status
                FROM assets
                WHERE content_hash = %s
                  AND asset_id != %s
                ORDER BY created_at ASC
                    FOR UPDATE SKIP LOCKED
                            LIMIT 1
                """,
                (content_hash, new_asset_id),
            )
            row = cur.fetchone()

            if not row:
                return DedupResult.NO_DUPLICATE

            canonical_id, typ, status = row

            if status != AssetStatus.READY.value:
                logger.info(
                    "Duplicate found (asset_id=%s) but status=%s, will retry",
                    canonical_id, status
                )
                return DedupResult.DUPLICATE_PENDING

            # Clone variants from canonical to new asset
            logger.info(
                "Deduplicating asset %s -> canonical %s",
                new_asset_id, canonical_id
            )

            if typ == "image":
                variants_cloned = clone_image_variants(cur, canonical_id, new_asset_id)
            elif typ == "video":
                variants_cloned = clone_video_variants(cur, canonical_id, new_asset_id)
            else:
                raise ValueError(f"Unknown asset type: {typ}")

            if variants_cloned == 0:
                logger.warning(
                    "Canonical asset %s has no variants, marking as failed",
                    canonical_id
                )
                cur.execute(
                    "UPDATE assets SET status = %s, error_reason = %s WHERE asset_id = %s",
                    (AssetStatus.FAILED.value, "Canonical asset has no variants", new_asset_id)
                )
            else:
                # Mark new asset as ready and link to canonical
                cur.execute(
                    """
                    UPDATE assets
                    SET status = %s,
                        canonical_asset_id = %s,
                        processed_at = NOW()
                    WHERE asset_id = %s
                    """,
                    (AssetStatus.READY.value, canonical_id, new_asset_id)
                )

            conn.commit()
            return DedupResult.DUPLICATE_READY


def process_asset_dispatch(
        asset_id: str,
        pg_pool: PgPool,
        storage: StorageX,
        cfg: WorkerConfig
) -> None:
    """
    Main entry point for asset processing with deduplication.
    """
    # 1. Load asset metadata
    with pg_pool.get_pg_conn() as conn:
        cur = conn.cursor()
        cur.execute(
            """
            SELECT asset_id, type, status, original_url, mime_type, content_hash
            FROM assets
            WHERE asset_id = %s
            """,
            (asset_id,),
        )
        row = cur.fetchone()
        if not row:
            raise RuntimeError(f"Asset not found: {asset_id}")

        _, typ, status, original_url, mime_type, content_hash = row

    # 2. Early exit if already processed
    if status in (AssetStatus.READY.value, AssetStatus.DUPLICATE.value):
        logger.info("Asset %s already in final state: %s", asset_id, status)
        return

    # 3. Proceed with processing
    local_raw_file = None
    try:
        # Mark as processing
        with pg_pool.get_pg_conn() as conn:
            cur = conn.cursor()
            cur.execute(
                "UPDATE assets SET status = %s WHERE asset_id = %s",
                (AssetStatus.PROCESSING.value, asset_id)
            )
            conn.commit()

        # Download raw file
        raw_key = f"media/raw/{asset_id}"
        tmp_dir = cfg.temp_dir
        os.makedirs(tmp_dir, exist_ok=True)
        local_raw_file = os.path.join(
            tmp_dir, f"{asset_id}-raw.{get_extension_for_mime(mime_type)}"
        )
        storage.download_to_file(raw_key, local_raw_file)

        content_hash = compute_file_hash(local_raw_file)

        # Check for duplicate using the actual downloaded file's hash
        if content_hash:
            dedup_result = check_for_duplicate(content_hash, asset_id, pg_pool)

            if dedup_result == DedupResult.DUPLICATE_READY:
                logger.info("Asset %s deduplicated successfully", asset_id)
                return
            elif dedup_result == DedupResult.DUPLICATE_PENDING:
                raise RetryableException(
                    f"Canonical asset for {asset_id} not ready yet"
                )

        # Process based on type
        if typ == "image":
            process_image_file(
                asset_id, local_raw_file, content_hash, pg_pool, storage, cfg
            )
        elif typ == "video":
            process_video_file(
                asset_id, local_raw_file, content_hash, pg_pool, storage, cfg
            )
        else:
            raise ValueError(f"Unknown asset type: {typ}")

    except Exception as e:
        logger.error("Failed to process asset %s: %s", asset_id, e, exc_info=True)
        with pg_pool.get_pg_conn() as conn:
            cur = conn.cursor()
            cur.execute(
                "UPDATE assets SET status = %s, error_reason = %s WHERE asset_id = %s",
                (AssetStatus.FAILED.value, str(e), asset_id)
            )
            conn.commit()
        raise
    finally:
        if local_raw_file and os.path.exists(local_raw_file):
            try:
                os.unlink(local_raw_file)
            except OSError:
                logger.warning("Failed to delete temp file %s", local_raw_file)


def clone_image_variants(cur, canonical_asset_id: str, new_asset_id: str) -> int:
    """
    Clone image variants from canonical to new asset.
    Returns number of variants cloned.
    """
    cur.execute(
        """
        SELECT url, role, width, height, size_bytes, format
        FROM variants.image
        WHERE asset_id = %s
        """,
        (canonical_asset_id,),
    )
    rows = cur.fetchall()

    if not rows:
        logger.warning("No image variants found for canonical %s", canonical_asset_id)
        return 0

    # Batch insert for efficiency
    cur.executemany(
        """
        INSERT INTO variants.image
            (asset_id, url, role, width, height, size_bytes, format)
        VALUES (%s, %s, %s, %s, %s, %s, %s)
            ON CONFLICT (asset_id, role) DO UPDATE SET
            url = EXCLUDED.url,
                                                width = EXCLUDED.width,
                                                height = EXCLUDED.height,
                                                size_bytes = EXCLUDED.size_bytes,
                                                format = EXCLUDED.format
        """,
        [(new_asset_id, r[0], r[1], r[2], r[3], r[4], r[5]) for r in rows]
    )

    logger.info("Cloned %d image variants from %s to %s",
                len(rows), canonical_asset_id, new_asset_id)
    return len(rows)


def clone_video_variants(cur, canonical_asset_id: str, new_asset_id: str) -> int:
    """
    Clone video variants and thumbnails from canonical to new asset.
    Returns total number of items cloned.
    """
    # Clone video variants
    cur.execute(
        """
        SELECT url, role, codec, container, resolution, bitrate_kbps,
               size_bytes, manifest_url, duration_seconds
        FROM variants.video
        WHERE asset_id = %s
        """,
        (canonical_asset_id,),
    )
    video_rows = cur.fetchall()

    if video_rows:
        cur.executemany(
            """
            INSERT INTO variants.video
            (asset_id, url, role, codec, container, resolution,
             bitrate_kbps, size_bytes, manifest_url, duration_seconds)
            VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s)
                ON CONFLICT (asset_id, role) DO UPDATE SET
                url = EXCLUDED.url,
                                                    codec = EXCLUDED.codec,
                                                    container = EXCLUDED.container,
                                                    resolution = EXCLUDED.resolution,
                                                    bitrate_kbps = EXCLUDED.bitrate_kbps,
                                                    size_bytes = EXCLUDED.size_bytes,
                                                    manifest_url = EXCLUDED.manifest_url,
                                                    duration_seconds = EXCLUDED.duration_seconds
            """,
            [(new_asset_id, *r) for r in video_rows]
        )

    # Clone video thumbnails (stored in image variants table)
    cur.execute(
        """
        SELECT url, role, format, height, width, size_bytes
        FROM variants.image
        WHERE asset_id = %s
        """,
        (canonical_asset_id,),
    )
    thumb_rows = cur.fetchall()

    if thumb_rows:
        cur.executemany(
            """
            INSERT INTO variants.image
                (asset_id, url, role, format, height, width, size_bytes)
            VALUES (%s, %s, %s, %s, %s, %s, %s)
                ON CONFLICT (asset_id, role) DO UPDATE SET
                url = EXCLUDED.url,
                                                    format = EXCLUDED.format,
                                                    height = EXCLUDED.height,
                                                    width = EXCLUDED.width,
                                                    size_bytes = EXCLUDED.size_bytes
            """,
            [(new_asset_id, *r) for r in thumb_rows]
        )

    total_cloned = len(video_rows) + len(thumb_rows)
    logger.info("Cloned %d video variants + %d thumbnails from %s to %s",
                len(video_rows), len(thumb_rows), canonical_asset_id, new_asset_id)
    return total_cloned