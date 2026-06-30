from PIL import Image, ImageOps
import io
import logging
import os

from opentelemetry import trace

logger = logging.getLogger("images")
tracer = trace.get_tracer("worker.processing.images")

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

        # Update content_hash on the asset row.
        with pg_pool.get_pg_conn() as conn:
            conn.cursor().execute(
                "UPDATE assets SET content_hash=%s, updated_at=now() WHERE asset_id=%s",
                (content_hash, asset_id),
            )

        for v in IMAGE_VARIANTS:
            role = v["role"]
            logger.info("generating image variant %s for asset %s", role, asset_id)

            with tracer.start_as_current_span("image.variant") as span:
                span.set_attribute("asset_id", asset_id)
                span.set_attribute("variant.role", role)
                span.set_attribute("variant.format", v["format"])

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
                span.set_attribute("variant.width", out_img.width)
                span.set_attribute("variant.height", out_img.height)
                span.set_attribute("variant.size_bytes", len(data))

                key = f"media/processed/{asset_id}/{role}.{v['format']}"
                storage.upload_bytes(key, data, content_type=f"image/{v['format']}")
                url = storage.public_url(key)

                # Upsert into variants.image (PK is asset_id + role)
                with pg_pool.get_pg_conn() as conn:
                    conn.cursor().execute(
                        """
                        INSERT INTO variants.image (asset_id, url, role, width, height, size_bytes, format)
                        VALUES (%s, %s, %s, %s, %s, %s, %s)
                        ON CONFLICT (asset_id, role) DO UPDATE SET
                            url = EXCLUDED.url,
                            width = EXCLUDED.width,
                            height = EXCLUDED.height,
                            size_bytes = EXCLUDED.size_bytes,
                            format = EXCLUDED.format
                        """,
                        (asset_id, url, role, out_img.width, out_img.height, len(data), v["format"]),
                    )

    # Mark asset ready
    with pg_pool.get_pg_conn() as conn:
        conn.cursor().execute(
            "UPDATE assets SET status='ready', processed_at=now(), updated_at=now() WHERE asset_id=%s",
            (asset_id,),
        )

    logger.info("finished image %s", asset_id)
