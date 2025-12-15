import logging
import os

import psycopg.errors

from worker.consumer.config import WorkerConfig
from worker.consumer.db import PgPool
from worker.processing.images import process_image_file
from worker.processing.videos import process_video_file
from worker.storage.base import StorageX
from worker.utils.logger import get_logger

logger = get_logger(__name__)


def get_extension_for_mime(mime_type: str) -> str:
    mapping = {
        "image/jpeg": "jpg",
        "image/png": "png",
        "image/webp": "webp",
        "video/mp4": "mp4",
        "video/quicktime": "mov",
    }
    return mapping.get(mime_type, "bin")


def process_asset_dispatch(
    asset_id, pg_pool: PgPool, storage: StorageX, cfg: WorkerConfig
):
    # load asset metadata
    mime_type = None
    with pg_pool.get_pg_conn() as conn:
        cur = conn.cursor()
        cur.execute(
            "SELECT asset_id, type, status, original_url, mime_type, content_hash FROM assets WHERE asset_id = %s",
            (asset_id,),
        )
        row = cur.fetchone()
        if not row:
            raise RuntimeError("asset not found: %s" % asset_id)
        _, typ, status, original_url, mime_type, content_hash = row

    # early exit guard: if already ready, skip
    if status == "ready":
        logger.info("asset %s already ready -> skipping", asset_id)
        return

    # compute keys and temp paths
    raw_key = f"media/raw/{asset_id}"
    tmp_dir = cfg.temp_dir
    os.makedirs(tmp_dir, exist_ok=True)
    local_raw_file = os.path.join(
        tmp_dir, f"{asset_id}-raw.{get_extension_for_mime(mime_type)}"
    )
    storage.download_to_file(raw_key, local_raw_file)

    if content_hash is None:
        # compute and store content hash if missing
        from worker.utils.hash import compute_file_hash

        computed_hash = compute_file_hash(local_raw_file)
        try:
            with pg_pool.get_pg_conn() as conn:
                cur = conn.cursor()
                cur.execute(
                    "UPDATE assets SET content_hash = %s WHERE asset_id = %s",
                    (computed_hash, asset_id),
                )
            logger.info(
                "computed and stored content hash for asset %s: %s",
                asset_id,
                computed_hash,
            )
        except psycopg.errors.UniqueViolation:
            logger.info(
                "duplicate content hash detected for asset %s, deduplicating", asset_id
            )
            handle_deduplication(computed_hash, pg_pool, asset_id)
            return

    if typ == "image":
        process_image_file(asset_id, local_raw_file, pg_pool, storage, cfg)
    elif typ == "video":
        process_video_file(asset_id, local_raw_file, pg_pool, storage, cfg)
    else:
        raise ValueError("unknown asset type %s" % typ)


class RetryableDedupException(Exception):
    pass


def handle_deduplication(content_hash: str, pg_pool: PgPool, new_asset_id: str):
    with pg_pool.get_pg_conn() as conn:
        cur = conn.cursor()

        # Lock canonical asset to prevent races
        cur.execute(
            """
                    SELECT asset_id, type
                    FROM assets
                    WHERE content_hash = %s
                      AND status = 'ready'
                        FOR UPDATE
                        LIMIT 1
                    """,
            (content_hash,),
        )
        row = cur.fetchone()

        if not row:
            logger.info(
                "dedupe hit for asset %s but no ready canonical asset yet; retry later",
                new_asset_id,
            )
            raise RetryableDedupException()

        canonical_asset_id, typ = row

        if typ == "image":
            clone_image_variants(cur, canonical_asset_id, new_asset_id)
        elif typ == "video":
            clone_video_variants(cur, canonical_asset_id, new_asset_id)

        cur.execute(
            "UPDATE assets SET status = 'ready' WHERE asset_id = %s", (new_asset_id,)
        )


def clone_image_variants(cur, canonical_asset_id: str, new_asset_id: str):
    cur.execute(
        """
            SELECT url, role, width, height, size_bytes, format
            FROM variants.image
            WHERE asset_id = %s
            """,
        (canonical_asset_id,),
    )
    rows = cur.fetchall()
    variants = [
        {
            "url": r[0],
            "role": r[1],
            "width": r[2],
            "height": r[3],
            "size_bytes": r[4],
            "format": r[5],
        }
        for r in rows
    ]

    for variant in variants:
        cur.execute(
            """
                INSERT INTO variants.image (asset_id, url, role, width, height, size_bytes, format)
                VALUES (%s, %s, %s, %s, %s, %s, %s)
                """,
            (
                new_asset_id,
                variant["url"],
                variant["role"],
                variant["width"],
                variant["height"],
                variant["size_bytes"],
                variant["format"],
            ),
        )


def clone_video_variants(cur, canonical_asset_id: str, new_asset_id: str):
    cur.execute(
        """
            SELECT url, role, codec, container, resolution, bitrate_kbps, size_bytes, manifest_url, duration_seconds
            FROM variants.video
            WHERE asset_id = %s
            """,
        (canonical_asset_id,),
    )
    rows = cur.fetchall()
    variants = [
        {
            "url": r[0],
            "role": r[1],
            "codec": r[2],
            "container": r[3],
            "resolution": r[4],
            "bitrate_kbps": r[5],
            "size_bytes": r[6],
            "manifest_url": r[7],
            "duration_seconds": r[8],
        }
        for r in rows
    ]

    for variant in variants:
        cur.execute(
            """
                INSERT INTO variants.video (asset_id, url, role, codec, container, resolution, bitrate_kbps, size_bytes, manifest_url, duration_seconds)
                VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s)
                """,
            (
                new_asset_id,
                variant["url"],
                variant["role"],
                variant["codec"],
                variant["container"],
                variant["resolution"],
                variant["bitrate_kbps"],
                variant["size_bytes"],
                variant["manifest_url"],
                variant["duration_seconds"],
            ),
        )

    cur.execute(
        """
    SELECT url, role, format, height, width, size_bytes
    FROM variants.image
    WHERE asset_id = %s
    """,
        (canonical_asset_id,),
    )
    rows = cur.fetchall()
    thumbnails = [
        {
            "url": r[0],
            "role": r[1],
            "format": r[2],
            "height": r[3],
            "width": r[4],
            "size_bytes": r[5],
        }
        for r in rows
    ]

    for thumb in thumbnails:
        cur.execute(
            """
        INSERT INTO variants.image (asset_id, url, role, format, height, width, size_bytes)
        VALUES (%s, %s, %s, %s, %s, %s, %s)
        """,
            (
                new_asset_id,
                thumb["url"],
                thumb["role"],
                thumb["format"],
                thumb["height"],
                thumb["width"],
                thumb["size_bytes"],
            ),
        )
