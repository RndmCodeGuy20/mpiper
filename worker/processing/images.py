from PIL import Image, ImageOps
import io
import hashlib
import json
import logging
import os

logger = logging.getLogger("images")

IMAGE_VARIANTS = [
    {
        "role": "thumbnail",
        "width": 256,
        "height": 256,
        "crop": True,
        "format": "webp",
        "quality": 80,
    },
    {
        "role": "display_small",
        "width": 512,
        "height": None,
        "crop": False,
        "format": "webp",
        "quality": 80,
    },
    {
        "role": "display_large",
        "width": 1280,
        "height": None,
        "crop": False,
        "format": "webp",
        "quality": 80,
    },
]


def compute_variant_hash(content_hash: str, params: dict) -> str:
    payload = json.dumps(params, sort_keys=True)
    h = hashlib.sha256()
    h.update(content_hash.encode())
    h.update(payload.encode())
    return h.hexdigest()


def encode_image(img: Image.Image, fmt: str, quality: int = 80) -> bytes:
    buf = io.BytesIO()
    save_args = {}
    if fmt == "webp":
        save_args["quality"] = quality
        img.save(buf, "WEBP", **save_args)
    else:
        img.save(buf, fmt.upper(), **save_args)
    return buf.getvalue()


def process_image_file(
    asset_id: str,
    local_raw_path: str,
    content_hash: str,
    pg_pool,
    storage,
    cfg,
):
    logger.info("processing image %s", asset_id)

    with Image.open(local_raw_path) as img:
        src_width, src_height = img.size
        mime = Image.MIME.get(img.format)

        with pg_pool.get_pg_conn() as conn:
            conn.cursor().execute(
                """
                UPDATE assets
                SET width=%s, height=%s, mime_type=%s, updated_at=now()
                WHERE asset_id=%s
                """,
                (src_width, src_height, mime, asset_id),
            )

        for v in IMAGE_VARIANTS:
            role = v["role"]

            params = {
                "role": role,
                "width": v["width"],
                "height": v["height"],
                "crop": v["crop"],
                "format": v["format"],
                "quality": v["quality"],
                "resample": "lanczos",
                "encoder": "pillow",
            }

            variant_hash = compute_variant_hash(content_hash, params)
            key = f"media/processed/{content_hash}/{variant_hash}.{v['format']}"
            url = f"https://storage.googleapis.com/{cfg.bucket.bucket_name}/{key}"

            with pg_pool.get_pg_conn() as conn:
                cur = conn.cursor()

                cur.execute(
                    "SELECT 1 FROM variants.image WHERE variant_hash=%s",
                    (variant_hash,),
                )
                exists = cur.fetchone() is not None

                if not exists:
                    logger.info("generating image variant %s", role)

                    if v["crop"]:
                        out_img = ImageOps.fit(
                            img,
                            (v["width"], v["height"]),
                            Image.LANCZOS,
                            centering=(0.5, 0.5),
                        )
                    else:
                        target_w = v["width"] or src_width
                        ratio = target_w / float(src_width)
                        target_h = int(src_height * ratio)
                        out_img = img.resize((target_w, target_h), Image.LANCZOS)

                    data = encode_image(out_img, v["format"], v["quality"])
                    storage.upload_bytes(key, data, content_type=f"image/{v['format']}")

                    cur.execute(
                        """
                        INSERT INTO variants.image (
                            variant_hash,
                            content_hash,
                            role,
                            format,
                            width,
                            height,
                            size_bytes,
                            url,
                            params
                        )
                        VALUES (%s,%s,%s,%s,%s,%s,%s,%s,%s)
                        ON CONFLICT (variant_hash) DO NOTHING
                        """,
                        (
                            variant_hash,
                            content_hash,
                            role,
                            v["format"],
                            out_img.width,
                            out_img.height,
                            len(data),
                            url,
                            json.dumps(params),
                        ),
                    )

                cur.execute(
                    """
                    INSERT INTO asset_image_variants (asset_id, role, variant_hash)
                    VALUES (%s,%s,%s)
                    ON CONFLICT (asset_id, role)
                        DO UPDATE SET variant_hash=EXCLUDED.variant_hash
                    """,
                    (asset_id, role, variant_hash),
                )

    # Mark asset ready
    with pg_pool.get_pg_conn() as conn:
        conn.cursor().execute(
            "UPDATE assets SET status='ready', updated_at=now() WHERE asset_id=%s",
            (asset_id,),
        )

    logger.info("finished image %s", asset_id)
