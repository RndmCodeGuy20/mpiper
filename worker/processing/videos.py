import hashlib
import json
import subprocess
import os
import logging

logger = logging.getLogger("videos")


def run(cmd: list[str]) -> None:
    logger.info("ffmpeg: %s", " ".join(cmd))
    res = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if res.returncode != 0:
        raise RuntimeError(res.stderr.decode())


def compute_variant_hash(content_hash: str, params: dict) -> str:
    payload = json.dumps(params, sort_keys=True)
    h = hashlib.sha256()
    h.update(content_hash.encode())
    h.update(payload.encode())
    return h.hexdigest()


def generate_poster(
    asset_id: str,
    content_hash: str,
    local_raw_path: str,
    pg_pool,
    storage,
    cfg,
):
    params = {
        "role": "poster",
        "seek_seconds": 2,
        "width": 640,
        "format": "jpg",
        "encoder": "ffmpeg",
    }

    variant_hash = compute_variant_hash(content_hash, params)
    key = f"media/processed/{content_hash}/{variant_hash}.jpg"
    url = f"https://storage.googleapis.com/{cfg.bucket.bucket_name}/{key}"

    # 1. Check if variant already exists
    with pg_pool.get_pg_conn() as conn:
        cur = conn.cursor()
        cur.execute(
            "SELECT 1 FROM variants.image WHERE variant_hash=%s",
            (variant_hash,),
        )
        if cur.fetchone():
            logger.info("poster variant exists, reusing")
        else:
            tmp_path = os.path.join(cfg.temp_dir, f"{variant_hash}.jpg")
            run(
                [
                    "ffmpeg",
                    "-ss",
                    "2",
                    "-i",
                    local_raw_path,
                    "-frames:v",
                    "1",
                    "-vf",
                    "scale=640:-1",
                    tmp_path,
                    "-y",
                ]
            )

            with open(tmp_path, "rb") as f:
                data = f.read()
                storage.upload_bytes(key, data, "image/jpeg")

            cur.execute(
                """
                INSERT INTO variants.image (
                    variant_hash, content_hash, role,
                    format, width, height, size_bytes, url, params
                )
                VALUES (%s,%s,'poster','jpg',640,'',%s,%s,%s)
                ON CONFLICT (variant_hash) DO NOTHING
                """,
                (variant_hash, content_hash, len(data), url, json.dumps(params)),
            )

        # 2. Map asset → variant
        cur.execute(
            """
            INSERT INTO asset_image_variants (asset_id, role, variant_hash)
            VALUES (%s,'poster',%s)
            ON CONFLICT (asset_id, role)
                DO UPDATE SET variant_hash=EXCLUDED.variant_hash
            """,
            (asset_id, variant_hash),
        )


def transcode_720p(
    asset_id: str,
    content_hash: str,
    local_raw_path: str,
    pg_pool,
    storage,
    cfg,
):
    params = {
        "role": "transcoded",
        "codec": "h264",
        "container": "mp4",
        "width": 1280,
        "crf": 23,
        "preset": "veryfast",
        "audio": "aac-128k",
        "ffmpeg": "default",
    }

    variant_hash = compute_variant_hash(content_hash, params)
    key = f"media/processed/{content_hash}/{variant_hash}.mp4"
    url = f"https://storage.googleapis.com/{cfg.bucket.bucket_name}/{key}"

    with pg_pool.get_pg_conn() as conn:
        cur = conn.cursor()
        cur.execute(
            "SELECT 1 FROM variants.video WHERE variant_hash=%s",
            (variant_hash,),
        )
        exists = cur.fetchone() is not None

        if not exists:
            out = os.path.join(cfg.temp_dir, f"{variant_hash}.mp4")
            run(
                [
                    "ffmpeg",
                    "-i",
                    local_raw_path,
                    "-vf",
                    "scale='min(1280,iw)':-2",
                    "-c:v",
                    "libx264",
                    "-preset",
                    "veryfast",
                    "-crf",
                    "23",
                    "-c:a",
                    "aac",
                    "-b:a",
                    "128k",
                    out,
                    "-y",
                ]
            )

            with open(out, "rb") as f:
                data = f.read()
                storage.upload_bytes(key, data, "video/mp4")

            cur.execute(
                """
                INSERT INTO variants.video (
                    variant_hash, content_hash, role,
                    codec, container, resolution,
                    bitrate_kbps, size_bytes, url, params
                )
                VALUES (%s,%s,'transcoded','h264','mp4','1280x720',
                        2500,%s,%s,%s)
                ON CONFLICT (variant_hash) DO NOTHING
                """,
                (variant_hash, content_hash, len(data), url, json.dumps(params)),
            )

        cur.execute(
            """
            INSERT INTO asset_video_variants (asset_id, role, variant_hash)
            VALUES (%s,'transcoded',%s)
            ON CONFLICT (asset_id, role)
                DO UPDATE SET variant_hash=EXCLUDED.variant_hash
            """,
            (asset_id, variant_hash),
        )


def generate_preview(
    asset_id: str,
    content_hash: str,
    local_raw_path: str,
    pg_pool,
    storage,
    cfg,
):
    params = {
        "role": "preview",
        "duration": 4,
        "width": 854,
        "muted": True,
        "codec": "h264",
    }

    variant_hash = compute_variant_hash(content_hash, params)
    key = f"media/processed/{content_hash}/{variant_hash}.mp4"
    url = f"https://storage.googleapis.com/{cfg.bucket.bucket_name}/{key}"

    with pg_pool.get_pg_conn() as conn:
        cur = conn.cursor()
        cur.execute(
            "SELECT 1 FROM variants.video WHERE variant_hash=%s",
            (variant_hash,),
        )
        if not cur.fetchone():
            out = os.path.join(cfg.temp_dir, f"{variant_hash}.mp4")
            run(
                [
                    "ffmpeg",
                    "-ss",
                    "2",
                    "-t",
                    "4",
                    "-i",
                    local_raw_path,
                    "-an",
                    "-vf",
                    "scale=854:-2",
                    "-c:v",
                    "libx264",
                    "-crf",
                    "25",
                    out,
                    "-y",
                ]
            )

            with open(out, "rb") as f:
                data = f.read()
                storage.upload_bytes(key, data, "video/mp4")

            cur.execute(
                """
                INSERT INTO variants.video (
                    variant_hash, content_hash, role,
                    codec, container, resolution,
                    bitrate_kbps, size_bytes,
                    duration_seconds, url, params
                )
                VALUES (%s,%s,'preview','h264','mp4','854x480',
                        800,%s,4,%s,%s)
                ON CONFLICT (variant_hash) DO NOTHING
                """,
                (variant_hash, content_hash, len(data), url, json.dumps(params)),
            )

        cur.execute(
            """
            INSERT INTO asset_video_variants (asset_id, role, variant_hash)
            VALUES (%s,'preview',%s)
            ON CONFLICT (asset_id, role)
                DO UPDATE SET variant_hash=EXCLUDED.variant_hash
            """,
            (asset_id, variant_hash),
        )


def probe_video(local_path: str) -> dict:
    cmd = [
        "ffprobe",
        "-v",
        "error",
        "-select_streams",
        "v:0",
        "-show_entries",
        "stream=width,height",
        "-show_entries",
        "format=duration",
        "-of",
        "json",
        local_path,
    ]
    p = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if p.returncode != 0:
        raise RuntimeError(p.stderr.decode())

    info = json.loads(p.stdout)
    stream = info["streams"][0]
    return {
        "width": stream["width"],
        "height": stream["height"],
        "duration": int(float(info["format"]["duration"])),
    }


def process_video_file(
    asset_id: str,
    local_raw_path: str,
    content_hash: str,
    pg_pool,
    storage,
    cfg,
):
    logger.info("processing video %s", asset_id)

    metadata = probe_video(local_raw_path)
    logger.info("video metadata: %s", metadata)

    with pg_pool.get_pg_conn() as conn:
        conn.cursor().execute(
            """
            UPDATE assets
            SET width = %s, height = %s, duration_seconds = %s, updated_at=now()
            WHERE asset_id=%s
            """,
            (metadata["width"], metadata["height"], metadata["duration"], asset_id),
        )

    generate_poster(asset_id, content_hash, local_raw_path, pg_pool, storage, cfg)
    transcode_720p(asset_id, content_hash, local_raw_path, pg_pool, storage, cfg)
    generate_preview(asset_id, content_hash, local_raw_path, pg_pool, storage, cfg)

    # Mark asset ready
    with pg_pool.get_pg_conn() as conn:
        conn.cursor().execute(
            "UPDATE assets SET status='ready', updated_at=now() WHERE asset_id=%s",
            (asset_id,),
        )
