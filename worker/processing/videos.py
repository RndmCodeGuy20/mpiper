import json
import logging
import os
import shutil
import subprocess
import tempfile

from opentelemetry import trace

logger = logging.getLogger("videos")
tracer = trace.get_tracer("worker.processing.videos")


def run(cmd: list[str]) -> None:
    """Execute ffmpeg command with error handling."""
    logger.info("ffmpeg: %s", " ".join(cmd))
    with tracer.start_as_current_span("ffmpeg.exec") as span:
        # cmd[0] is the binary (ffmpeg/ffprobe); record it without the full
        # argv to avoid leaking paths as high-cardinality span attributes.
        span.set_attribute("ffmpeg.binary", cmd[0] if cmd else "")
        res = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        span.set_attribute("ffmpeg.returncode", res.returncode)
        if res.returncode != 0:
            err = res.stderr.decode()
            span.set_status(trace.StatusCode.ERROR, "ffmpeg failed")
            raise RuntimeError(f"FFmpeg failed: {err}")


def probe_video(local_path: str) -> dict:
    """Extract video metadata using ffprobe."""
    cmd = [
        "ffprobe", "-v", "error",
        "-select_streams", "v:0",
        "-show_entries", "stream=width,height",
        "-show_entries", "format=duration",
        "-of", "json",
        local_path,
    ]
    p = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if p.returncode != 0:
        raise RuntimeError(f"ffprobe failed: {p.stderr.decode()}")

    info = json.loads(p.stdout)
    if not info.get("streams"):
        raise RuntimeError("No video stream found")

    stream = info["streams"][0]
    return {
        "width": stream["width"],
        "height": stream["height"],
        "duration": int(float(info["format"]["duration"])),
    }


def generate_poster(asset_id, local_raw_path, storage, cfg, pg_pool):
    """Generate video poster thumbnail and store as image variant."""
    tmpdir = tempfile.mkdtemp(dir=cfg.temp_dir)
    try:
        out_path = os.path.join(tmpdir, "poster.jpg")
        run([
            "ffmpeg", "-ss", "2", "-i", local_raw_path,
            "-frames:v", "1", "-vf", "scale=640:-1",
            out_path, "-y",
        ])

        with open(out_path, "rb") as f:
            data = f.read()

        key = f"media/processed/{asset_id}/poster.jpg"
        storage.upload_bytes(key, data, content_type="image/jpeg")
        url = storage.public_url(key)

        with pg_pool.get_pg_conn() as conn:
            conn.cursor().execute(
                """
                INSERT INTO variants.image (asset_id, url, role, width, height, size_bytes, format)
                VALUES (%s, %s, 'poster', 640, NULL, %s, 'jpeg')
                ON CONFLICT (asset_id, role) DO UPDATE SET
                    url = EXCLUDED.url, size_bytes = EXCLUDED.size_bytes
                """,
                (asset_id, url, len(data)),
            )
    finally:
        shutil.rmtree(tmpdir, ignore_errors=True)


def transcode_720p(asset_id, local_raw_path, storage, cfg, pg_pool):
    """Transcode video to 720p H.264."""
    tmpdir = tempfile.mkdtemp(dir=cfg.temp_dir)
    try:
        out_path = os.path.join(tmpdir, "transcoded.mp4")
        run([
            "ffmpeg", "-i", local_raw_path,
            "-vf", "scale='min(1280,iw)':-2",
            "-c:v", "libx264", "-preset", "veryfast", "-crf", "23",
            "-c:a", "aac", "-b:a", "128k",
            out_path, "-y",
        ])

        with open(out_path, "rb") as f:
            data = f.read()

        key = f"media/processed/{asset_id}/transcoded.mp4"
        storage.upload_bytes(key, data, content_type="video/mp4")
        url = storage.public_url(key)

        with pg_pool.get_pg_conn() as conn:
            conn.cursor().execute(
                """
                INSERT INTO variants.video (asset_id, url, role, codec, container, resolution, bitrate_kbps, size_bytes)
                VALUES (%s, %s, 'transcoded', 'h264', 'mp4', '1280x720', 2500, %s)
                ON CONFLICT (asset_id, role) DO UPDATE SET
                    url = EXCLUDED.url, size_bytes = EXCLUDED.size_bytes
                """,
                (asset_id, url, len(data)),
            )
    finally:
        shutil.rmtree(tmpdir, ignore_errors=True)


def generate_preview(asset_id, local_raw_path, storage, cfg, pg_pool):
    """Generate short muted preview clip."""
    tmpdir = tempfile.mkdtemp(dir=cfg.temp_dir)
    try:
        out_path = os.path.join(tmpdir, "preview.mp4")
        run([
            "ffmpeg", "-ss", "2", "-t", "4",
            "-i", local_raw_path, "-an",
            "-vf", "scale=854:-2", "-c:v", "libx264", "-crf", "25",
            out_path, "-y",
        ])

        with open(out_path, "rb") as f:
            data = f.read()

        key = f"media/processed/{asset_id}/preview.mp4"
        storage.upload_bytes(key, data, content_type="video/mp4")
        url = storage.public_url(key)

        with pg_pool.get_pg_conn() as conn:
            conn.cursor().execute(
                """
                INSERT INTO variants.video (asset_id, url, role, codec, container, resolution, bitrate_kbps, size_bytes, duration_seconds)
                VALUES (%s, %s, 'preview', 'h264', 'mp4', '854x480', 800, %s, 4)
                ON CONFLICT (asset_id, role) DO UPDATE SET
                    url = EXCLUDED.url, size_bytes = EXCLUDED.size_bytes
                """,
                (asset_id, url, len(data)),
            )
    finally:
        shutil.rmtree(tmpdir, ignore_errors=True)


def process_video_file(asset_id, local_raw_path, content_hash, pg_pool, storage, cfg):
    """Main video processing pipeline."""
    logger.info("Processing video asset %s", asset_id)

    # Update content hash
    with pg_pool.get_pg_conn() as conn:
        conn.cursor().execute(
            "UPDATE assets SET content_hash=%s, updated_at=now() WHERE asset_id=%s",
            (content_hash, asset_id),
        )

    for stage_name, fn in (
        ("video.poster", generate_poster),
        ("video.transcode_720p", transcode_720p),
        ("video.preview", generate_preview),
    ):
        with tracer.start_as_current_span(stage_name) as span:
            span.set_attribute("asset_id", asset_id)
            fn(asset_id, local_raw_path, storage, cfg, pg_pool)

    # Mark asset ready
    with pg_pool.get_pg_conn() as conn:
        conn.cursor().execute(
            "UPDATE assets SET status='ready', processed_at=now(), updated_at=now() WHERE asset_id=%s",
            (asset_id,),
        )

    logger.info("Successfully processed video asset %s", asset_id)
