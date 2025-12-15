import subprocess
import os
import logging

from worker.consumer.db import PgPool
from worker.storage.base import StorageX

logger = logging.getLogger("videos")


def _run(cmd):
    logger.info("running ffmpeg: %s", " ".join(cmd))
    res = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if res.returncode != 0:
        logger.error("ffmpeg failed: %s", res.stderr.decode())
        raise RuntimeError("ffmpeg failed")


def process_video_file(
    asset_id, local_raw_path, content_hash: str, pg_pool: PgPool, storage: StorageX, cfg
):
    logger.info("processing video asset %s", asset_id)
    tmp_dir = os.path.dirname(local_raw_path)
    # probe width/height/duration using ffprobe
    probe_cmd = [
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
        local_raw_path,
    ]
    p = subprocess.run(probe_cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if p.returncode != 0:
        logger.error("ffprobe failed: %s", p.stderr.decode())
        raise RuntimeError("ffprobe failed")
    import json

    info = json.loads(p.stdout)
    # extract dims/duration
    streams = info.get("streams", [])
    width = streams[0].get("width") if streams else None
    height = streams[0].get("height") if streams else None
    duration = float(info.get("format", {}).get("duration", 0.0))

    with pg_pool.get_pg_conn() as conn:
        cur = conn.cursor()
        cur.execute(
            "UPDATE assets SET width = %s, height = %s, duration_seconds = %s, mime_type = %s, updated_at=now() WHERE asset_id=%s",
            (width, height, int(duration) if duration else None, "video", asset_id),
        )

    # 1) create poster
    poster_path = os.path.join(tmp_dir, f"{asset_id}-poster.jpg")
    try:
        _run(
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
                poster_path,
                "-y",
            ]
        )
        with open(poster_path, "rb") as f:
            data = f.read()
            key = f"media/processed/{asset_id}/video/poster_640.jpg"
            storage.upload_bytes(key, data, content_type="image/jpeg")
            url = "https://storage.googleapis.com/{bucket}/{key}".format(
                bucket=cfg.bucket.bucket_name,
                key=key,
            )
            with pg_pool.get_pg_conn() as conn:
                cur = conn.cursor()
                cur.execute(
                    """
                            INSERT INTO variants.image (variant_id, asset_id, role, format, width, height, url, size_bytes, created_at)
                            VALUES (gen_random_uuid(), %s, 'poster', 'jpg', %s, %s, %s, %s, now())
                                ON CONFLICT (asset_id, role) DO NOTHING
                            """,
                    (asset_id, width or 640, height or 360, url, len(data)),
                )
    except Exception:
        logger.exception("poster failed for %s", asset_id)
        # poster failure shouldn't kill the whole pipeline; continue

    # 2) transcode single mp4 720p
    out_720 = os.path.join(tmp_dir, f"{asset_id}-720p.mp4")
    scale_filter = "scale='min(1280,iw)':'-2'"
    try:
        _run(
            [
                "ffmpeg",
                "-i",
                local_raw_path,
                "-c:v",
                "libx264",
                "-preset",
                "veryfast",
                "-crf",
                "23",
                "-vf",
                scale_filter,
                "-c:a",
                "aac",
                "-b:a",
                "128k",
                out_720,
                "-y",
            ]
        )
        with open(out_720, "rb") as f:
            data = f.read()
            key = f"media/processed/{asset_id}/video/stream_720p.mp4"
            storage.upload_bytes(key, data, content_type="video/mp4")
            url = "https://storage.googleapis.com/{bucket}/{key}".format(
                bucket=cfg.bucket.bucket_name,
                key=key,
            )
            duration = int(duration) if duration else None
            with pg_pool.get_pg_conn() as conn:
                cur = conn.cursor()
                cur.execute(
                    """
                            INSERT INTO variants.video (variant_id, asset_id, role, codec, container, resolution, bitrate_kbps, url, size_bytes, duration_seconds, created_at)
                            VALUES (gen_random_uuid(), %s, 'stream', 'h264', 'mp4', %s, %s, %s, %s, %s, now())
                                ON CONFLICT (asset_id, role) DO NOTHING
                            """,
                    (asset_id, f"{1280}x{720}", 2500, url, duration, len(data)),
                )
    except Exception:
        logger.exception("720p transcode failed for %s", asset_id)
        raise

    # 3) preview clip 4s muted 480p
    preview_out = os.path.join(tmp_dir, f"{asset_id}-preview.mp4")
    try:
        _run(
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
                "-preset",
                "veryfast",
                "-crf",
                "25",
                preview_out,
                "-y",
            ]
        )
        with open(preview_out, "rb") as f:
            data = f.read()
            key = f"media/processed/{asset_id}/video/preview_4s_480p.mp4"
            storage.upload_bytes(key, data, content_type="video/mp4")
            url = "https://storage.googleapis.com/{bucket}/{key}".format(
                bucket=cfg.bucket.bucket_name,
                key=key,
            )
            with pg_pool.get_pg_conn() as conn:
                cur = conn.cursor()
                cur.execute(
                    """
                            INSERT INTO variants.video (variant_id, asset_id, role, codec, container, resolution, bitrate_kbps, url, size_bytes, duration_seconds, created_at)
                            VALUES (gen_random_uuid(), %s, 'preview', 'h264', 'mp4', %s, %s, %s, %s,%s, now())
                                ON CONFLICT (asset_id, role) DO NOTHING
                            """,
                    (asset_id, "854x480", 800, url, len(data), 4),
                )
    except Exception:
        logger.exception("preview generation failed for %s", asset_id)
        # not fatal

    logger.info("finished processing video asset %s", asset_id)

    # finally, mark asset ready in DB
    with pg_pool.get_pg_conn() as conn:
        cur = conn.cursor()
        cur.execute(
            "UPDATE assets SET status='ready', updated_at = now() WHERE asset_id = %s",
            (asset_id,),
        )
