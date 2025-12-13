import logging
import os

from worker.consumer.config import WorkerConfig
from worker.consumer.db import PgPool
from worker.processing.images import process_image_file
from worker.processing.videos import process_video_file
from worker.storage.base import StorageX
from worker.utils.logger import get_logger

logger = get_logger(__name__)

def get_extension_for_mime(mime_type: str) -> str:
    mapping = {
        'image/jpeg': 'jpg',
        'image/png': 'png',
        'image/webp': 'webp',
        'video/mp4': 'mp4',
        'video/quicktime': 'mov',
    }
    return mapping.get(mime_type, 'bin')

def process_asset_dispatch(asset_id, pg_pool: PgPool, storage: StorageX, cfg: WorkerConfig):
    # load asset metadata
    mime_type = None
    with pg_pool.get_pg_conn() as conn:
        cur = conn.cursor()
        cur.execute("SELECT asset_id, type, status, original_url, mime_type FROM assets WHERE asset_id = %s", (asset_id,))
        row = cur.fetchone()
        if not row:
            raise RuntimeError("asset not found: %s" % asset_id)
        _, typ, status, original_url, mime_type = row

    # early exit guard: if already ready, skip
    if status == 'ready':
        logger.info("asset %s already ready -> skipping", asset_id)
        return

    # compute keys and temp paths
    raw_key = f"media/raw/{asset_id}"
    tmp_dir = cfg.temp_dir
    os.makedirs(tmp_dir, exist_ok=True)
    local_raw_file = os.path.join(tmp_dir, f"{asset_id}-raw.{get_extension_for_mime(mime_type)}")
    storage.download_to_file(raw_key, local_raw_file)

    if typ == 'image':
        process_image_file(asset_id, local_raw_file, pg_pool, storage, cfg)
    elif typ == 'video':
        process_video_file(asset_id, local_raw_file, pg_pool, storage, cfg)
    else:
        raise ValueError("unknown asset type %s" % typ)
