from PIL import Image, ImageOps
import io

from worker.consumer.db import PgPool
from worker.storage.base import StorageX
from worker.utils.logger import get_logger

logger = get_logger(__name__)

IMAGE_VARIANTS = [
    ("thumbnail", 256, 256, True, "webp"),
    ("display_small", 512, None, False, "webp"),
    ("display_large", 1280, None, False, "webp"),
]


def _encode_image(img: Image.Image, fmt: str, quality=80):
    out = io.BytesIO()
    save_kwargs = {}
    if fmt == "webp":
        save_kwargs["quality"] = quality
        img.save(out, "WEBP", **save_kwargs)
    else:
        img.save(out, fmt.upper(), **save_kwargs)
    return out.getvalue()


def process_image_file(asset_id, local_raw_path, pg_pool: PgPool, storage: StorageX, cfg):
    logger.info("processing image asset %s", asset_id)
    # img = Image.open(local_raw_path)
    with Image.open(local_raw_path) as img:
        width, height = img.size

        logger.info("image %s opened: format=%s size=%dx%d", asset_id, img.format, width, height)

        # insert asset metadata (width/height) in DB
        with pg_pool.get_pg_conn() as conn:
            cur = conn.cursor()
            cur.execute("UPDATE assets SET width = %s, height = %s, mime_type = %s, updated_at=now() WHERE asset_id=%s",
                    (width, height, Image.MIME.get(img.format), asset_id))

        for role, w, h, crop, fmt in IMAGE_VARIANTS:
            try:
                # check if variant exists
                with pg_pool.get_pg_conn() as conn:
                    cur = conn.cursor()
                    cur.execute("SELECT 1 FROM variants.image WHERE asset_id=%s AND role=%s", (asset_id, role))
                    if cur.fetchone():
                        logger.info("variant %s exists for asset %s -> skip", role, asset_id)
                        continue

                if crop:
                    # center crop then resize
                    img_c = ImageOps.fit(img, (w, h), Image.LANCZOS, centering=(0.5, 0.5))
                else:
                    # resize preserving aspect
                    if w is None:
                        new_w = width
                    else:
                        new_w = w
                    ratio = new_w / float(width)
                    new_h = int(height * ratio) if height else None
                    img_c = img.resize((new_w, new_h), Image.LANCZOS)

                    logger.debug("resized image for role %s to %dx%d", role, img_c.width, img_c.height)

                data = _encode_image(img_c, fmt)

                key = f"media/processed/{asset_id}/img/{role}.{fmt}"
                storage.upload_bytes(key, data, content_type=f"image/{fmt}")

                url = "https://storage.googleapis.com/{bucket}/{key}".format(
                    bucket=cfg.bucket.bucket_name,
                    key=key,
                )

                logger.info("uploaded variant %s for asset %s to %s", role, asset_id, url)

                with pg_pool.get_pg_conn() as conn:
                    cur = conn.cursor()
                    cur.execute("""
                            INSERT INTO variants.image (variant_id, asset_id, role, format, width, height, url, size_bytes, created_at)
                            VALUES (gen_random_uuid(), %s, %s, %s, %s, %s, %s, %s, now())
                                ON CONFLICT (asset_id, role) DO NOTHING
                            """, (asset_id, role, fmt, img_c.width, img_c.height, url, len(data)))
            except Exception:
                logger.exception("failed processing variant %s for %s", role, asset_id)
                raise

    logger.info("completed processing image asset %s", asset_id)

    # mark asset ready
    with pg_pool.get_pg_conn() as conn:
        cur = conn.cursor()
        cur.execute("UPDATE assets SET status='ready', updated_at = now() WHERE asset_id = %s", (asset_id,))