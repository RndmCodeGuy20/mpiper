import os
import time
from enum import Enum

from opentelemetry import trace

from worker.consumer.config import WorkerConfig
from worker.consumer.db import PgPool
from worker.processing.images import process_image_file
from worker.processing.videos import process_video_file
from worker.storage.base import StorageX
from worker.utils.hash import compute_file_hash
from worker.utils.logger import get_logger
from worker.utils import metrics as wm

logger = get_logger(__name__)
tracer = trace.get_tracer("worker.processing")


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
    with tracer.start_as_current_span("process.dispatch") as span:
        # asset_id is fine as a span attribute (high cardinality is OK on traces),
        # but must NEVER become a metric label.
        span.set_attribute("asset_id", asset_id)

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

        span.set_attribute("asset.type", typ or "unknown")
        span.set_attribute("asset.status", status or "unknown")

        # 2. Early exit if already processed
        if status in (AssetStatus.READY.value, AssetStatus.DUPLICATE.value):
            logger.info("Asset %s already in final state: %s", asset_id, status)
            span.set_attribute("dispatch.short_circuit", "already_final")
            return

        # 3. Proceed with processing
        local_raw_file = None
        proc_start = time.time()
        try:
            # Mark as processing
            with pg_pool.get_pg_conn() as conn:
                cur = conn.cursor()
                cur.execute(
                    "UPDATE assets SET status = %s WHERE asset_id = %s",
                    (AssetStatus.PROCESSING.value, asset_id)
                )

            # Download raw file
            raw_key = f"media/raw/{asset_id}"
            tmp_dir = cfg.temp_dir
            os.makedirs(tmp_dir, exist_ok=True)
            local_raw_file = os.path.join(
                tmp_dir, f"{asset_id}-raw.{get_extension_for_mime(mime_type)}"
            )
            with tracer.start_as_current_span("process.download") as dl_span:
                dl_span.set_attribute("asset_id", asset_id)
                dl_span.set_attribute("storage.key", raw_key)
                storage.download_to_file(raw_key, local_raw_file)
                try:
                    dl_span.set_attribute(
                        "download.size_bytes", os.path.getsize(local_raw_file)
                    )
                except OSError:
                    pass

            content_hash = compute_file_hash(local_raw_file)

            # Check for duplicate using the actual downloaded file's hash
            if content_hash:
                with tracer.start_as_current_span("process.dedup_check") as dd_span:
                    dd_span.set_attribute("asset_id", asset_id)
                    dd_span.set_attribute("content_hash", content_hash)
                    dedup_result = check_for_duplicate(content_hash, asset_id, pg_pool)
                    dd_span.set_attribute("dedup.result", dedup_result.value)

                if dedup_result == DedupResult.DUPLICATE_READY:
                    logger.info("Asset %s deduplicated successfully", asset_id)
                    span.set_attribute("dispatch.short_circuit", "deduplicated")
                    return
                elif dedup_result == DedupResult.DUPLICATE_PENDING:
                    raise RetryableException(
                        f"Canonical asset for {asset_id} not ready yet"
                    )

            # Process based on type
            proc_start = time.time()
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

            # Asset processing duration, labelled by type only (low cardinality).
            # Feeds the image/video "ready latency" SLIs. asset_id stays a span
            # attribute, never a metric label.
            wm.record_asset(typ, time.time() - proc_start, success=True)

        except Exception as e:
            # Do not touch assets.status here. The consumer (_handle_job) owns the
            # asset state transition: it marks the asset failed only after the retry
            # cap is hit, and ready on success. Writing 'failed' on every exception
            # — including RetryableException — left the asset stuck failed across
            # retries even though the job was still pending. See DEV-34.
            logger.error("Failed to process asset %s: %s", asset_id, e, exc_info=True)
            wm.record_asset(typ, time.time() - proc_start, success=False)
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