import hashlib
import json
import logging
import os
import shutil
import subprocess
import tempfile
from typing import Optional, Dict, Any

logger = logging.getLogger("videos")


def run(cmd: list[str]) -> None:
    """Execute ffmpeg command with error handling."""
    logger.info("ffmpeg: %s", " ".join(cmd))
    res = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if res.returncode != 0:
        raise RuntimeError(f"FFmpeg failed: {res.stderr.decode()}")


def compute_variant_hash(content_hash: str, params: dict) -> str:
    """
    Compute deterministic hash for a variant.
    Same content + same params = same variant_hash = same output file.
    """
    payload = json.dumps(params, sort_keys=True)
    h = hashlib.sha256()
    h.update(content_hash.encode())
    h.update(payload.encode())
    return h.hexdigest()


def ensure_variant_exists(
        variant_hash: str,
        content_hash: str,
        role: str,
        params: dict,
        variant_type: str,  # "image" or "video"
        pg_pool,
        storage,
        cfg,
        generator_fn,  # Function that generates the file if missing
) -> str:
    """
    Ensure variant exists in storage and variants table.
    Returns the URL of the variant.

    This is the KEY function - it handles content-addressed storage correctly.
    """
    # Determine file extension and MIME type
    if variant_type == "image":
        ext = params.get("format", "jpg")
        mime_type = f"image/{ext}"
        table = "variants.image"
    else:
        ext = params.get("container", "mp4")
        mime_type = f"video/{ext}"
        table = "variants.video"

    # CORRECT: Storage key uses variant_hash, not content_hash in path
    # This way, identical variants from different content share the same file
    key = f"media/variants/{variant_hash[:2]}/{variant_hash}.{ext}"
    url = storage.public_url(key)

    with pg_pool.get_pg_conn() as conn:
        cur = conn.cursor()

        # Check if variant already exists
        cur.execute(
            f"SELECT url FROM {table} WHERE variant_hash = %s",
            (variant_hash,),
        )
        existing = cur.fetchone()

        if existing:
            logger.info("Variant %s already exists, reusing", variant_hash)
            return existing[0]

        # Variant doesn't exist - generate it
        logger.info("Generating new variant %s for role=%s", variant_hash, role)
        tmpdir = tempfile.mkdtemp(dir=cfg.temp_dir)
        try:
            tmp_path = os.path.join(tmpdir, f"{variant_hash}.{ext}")

            # Call the generator function (passed in by caller)
            metadata = generator_fn(tmp_path, params)

            # Upload to storage
            with open(tmp_path, "rb") as f:
                data = f.read()
            storage.upload_bytes(key, data, mime_type)
        finally:
            shutil.rmtree(tmpdir, ignore_errors=True)

        # Store variant metadata
        if variant_type == "image":
            cur.execute(
                """
                INSERT INTO variants.image (
                    variant_hash, content_hash, role,
                    format, width, height, size_bytes, url, params
                )
                VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s)
                ON CONFLICT (variant_hash) DO NOTHING
                """,
                (
                    variant_hash, content_hash, role,
                    params.get("format"), metadata.get("width"),
                    metadata.get("height"), len(data), url, json.dumps(params)
                ),
            )
        else:  # video
            cur.execute(
                """
                INSERT INTO variants.video (
                    variant_hash, content_hash, role,
                    codec, container, resolution,
                    bitrate_kbps, size_bytes, duration_seconds,
                    manifest_url, url, params
                )
                VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s)
                ON CONFLICT (variant_hash) DO NOTHING
                """,
                (
                    variant_hash, content_hash, role,
                    params.get("codec"), params.get("container"),
                    metadata.get("resolution"),
                    metadata.get("bitrate_kbps"), len(data),
                    metadata.get("duration_seconds"),
                    metadata.get("manifest_url"), url, json.dumps(params)
                ),
            )

        return url


def link_variant_to_asset(
        asset_id: str,
        role: str,
        variant_hash: str,
        variant_type: str,
        pg_pool
) -> None:
    """
    Create mapping from asset to variant.
    Multiple assets can point to the same variant (deduplication).
    """
    table = "asset_image_variants" if variant_type == "image" else "asset_video_variants"

    with pg_pool.get_pg_conn() as conn:
        cur = conn.cursor()
        cur.execute(
            f"""
            INSERT INTO {table} (asset_id, role, variant_hash)
            VALUES (%s, %s, %s)
            ON CONFLICT (asset_id, role)
                DO UPDATE SET variant_hash = EXCLUDED.variant_hash
            """,
            (asset_id, role, variant_hash),
        )


def generate_poster(
        asset_id: str,
        content_hash: str,
        local_raw_path: str,
        pg_pool,
        storage,
        cfg,
) -> None:
    """Generate video poster thumbnail."""
    params = {
        "role": "poster",
        "seek_seconds": 2,
        "width": 640,
        "format": "jpg",
        "encoder": "ffmpeg",
    }

    variant_hash = compute_variant_hash(content_hash, params)

    def generate_file(output_path: str, file_params: dict) -> dict:
        """Inner function to generate poster file."""
        run([
            "ffmpeg",
            "-ss", str(file_params["seek_seconds"]),
            "-i", local_raw_path,
            "-frames:v", "1",
            "-vf", f"scale={file_params['width']}:-1",
            output_path,
            "-y",
        ])

        # Could probe dimensions here if needed
        return {"width": file_params["width"], "height": None}

    # Ensure variant exists (creates if needed)
    ensure_variant_exists(
        variant_hash, content_hash, "poster", params,
        "image", pg_pool, storage, cfg, generate_file
    )

    # Link this asset to the variant
    link_variant_to_asset(asset_id, "poster", variant_hash, "image", pg_pool)


def transcode_720p(
        asset_id: str,
        content_hash: str,
        local_raw_path: str,
        pg_pool,
        storage,
        cfg,
) -> None:
    """Transcode video to 720p H.264."""
    params = {
        "role": "transcoded",
        "codec": "h264",
        "container": "mp4",
        "width": 1280,
        "crf": 23,
        "preset": "veryfast",
        "audio": "aac-128k",
    }

    variant_hash = compute_variant_hash(content_hash, params)

    def generate_file(output_path: str, file_params: dict) -> dict:
        """Inner function to transcode video."""
        run([
            "ffmpeg",
            "-i", local_raw_path,
            "-vf", f"scale='min({file_params['width']},iw)':-2",
            "-c:v", "libx264",
            "-preset", file_params["preset"],
            "-crf", str(file_params["crf"]),
            "-c:a", "aac",
            "-b:a", "128k",
            output_path,
            "-y",
        ])

        return {
            "resolution": "1280x720",
            "bitrate_kbps": 2500,
            "duration_seconds": None,
        }

    ensure_variant_exists(
        variant_hash, content_hash, "transcoded", params,
        "video", pg_pool, storage, cfg, generate_file
    )

    link_variant_to_asset(asset_id, "transcoded", variant_hash, "video", pg_pool)


def generate_preview(
        asset_id: str,
        content_hash: str,
        local_raw_path: str,
        pg_pool,
        storage,
        cfg,
) -> None:
    """Generate short preview clip."""
    params = {
        "role": "preview",
        "duration": 4,
        "width": 854,
        "muted": True,
        "codec": "h264",
        "seek_seconds": 2,
    }

    variant_hash = compute_variant_hash(content_hash, params)

    def generate_file(output_path: str, file_params: dict) -> dict:
        """Inner function to generate preview."""
        run([
            "ffmpeg",
            "-ss", str(file_params["seek_seconds"]),
            "-t", str(file_params["duration"]),
            "-i", local_raw_path,
            "-an",  # No audio (muted)
            "-vf", f"scale={file_params['width']}:-2",
            "-c:v", "libx264",
            "-crf", "25",
            output_path,
            "-y",
        ])

        return {
            "resolution": "854x480",
            "bitrate_kbps": 800,
            "duration_seconds": file_params["duration"],
        }

    ensure_variant_exists(
        variant_hash, content_hash, "preview", params,
        "video", pg_pool, storage, cfg, generate_file
    )

    link_variant_to_asset(asset_id, "preview", variant_hash, "video", pg_pool)


def probe_video(local_path: str) -> dict:
    """Extract video metadata using ffprobe."""
    cmd = [
        "ffprobe",
        "-v", "error",
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


def process_video_file(
        asset_id: str,
        local_raw_path: str,
        content_hash: str,
        pg_pool,
        storage,
        cfg,
) -> None:
    """
    Main video processing pipeline.

    This function:
    1. Probes video metadata
    2. Generates all required variants (reusing existing if available)
    3. Links variants to this asset
    4. Marks asset as ready
    """
    logger.info("Processing video asset %s (content_hash=%s)", asset_id, content_hash)

    # Extract metadata
    metadata = probe_video(local_raw_path)
    logger.info("Video metadata: %s", metadata)

    # Update asset with video properties
    with pg_pool.get_pg_conn() as conn:
        conn.cursor().execute(
            """
            UPDATE assets
            SET width = %s, height = %s, duration_seconds = %s, updated_at = NOW()
            WHERE asset_id = %s
            """,
            (metadata["width"], metadata["height"], metadata["duration"], asset_id),
        )

    # Generate/link all variants
    # These functions will reuse existing variants if they exist!
    try:
        generate_poster(asset_id, content_hash, local_raw_path, pg_pool, storage, cfg)
        transcode_720p(asset_id, content_hash, local_raw_path, pg_pool, storage, cfg)
        generate_preview(asset_id, content_hash, local_raw_path, pg_pool, storage, cfg)
    except Exception as e:
        logger.error("Failed to generate variants for %s: %s", asset_id, e)
        with pg_pool.get_pg_conn() as conn:
            conn.cursor().execute(
                "UPDATE assets SET status = 'failed', error_reason = %s WHERE asset_id = %s",
                (str(e), asset_id),
            )
        raise

    # Mark asset as ready
    with pg_pool.get_pg_conn() as conn:
        conn.cursor().execute(
            """
            UPDATE assets
            SET status = 'ready', processed_at = NOW(), updated_at = NOW()
            WHERE asset_id = %s
            """,
            (asset_id,),
        )

    logger.info("Successfully processed video asset %s", asset_id)


# ============================================================================
# BONUS: Alternative approach for even simpler deduplication
# ============================================================================

def quick_dedupe_variants(
        asset_id: str,
        content_hash: str,
        pg_pool
) -> bool:
    """
    Check if another asset with the same content_hash already has variants.
    If yes, just copy the mappings. Returns True if deduped.

    This can be called BEFORE expensive processing to avoid regenerating
    variants that already exist.
    """
    with pg_pool.get_pg_conn() as conn:
        with conn.transaction():
            cur = conn.cursor()

            # Find another asset with same content hash that's ready
            cur.execute(
                """
                SELECT asset_id
                FROM assets
                WHERE content_hash = %s
                  AND asset_id != %s
                  AND status = 'ready'
                ORDER BY created_at
                LIMIT 1
                    FOR UPDATE SKIP LOCKED
                """,
                (content_hash, asset_id),
            )
            canonical = cur.fetchone()

            if not canonical:
                return False

            canonical_id = canonical[0]
            logger.info("Fast dedupe: copying variant mappings from %s to %s",
                        canonical_id, asset_id)

            # Copy video variant mappings
            cur.execute(
                """
                INSERT INTO asset_video_variants (asset_id, role, variant_hash)
                SELECT %s, role, variant_hash
                FROM asset_video_variants
                WHERE asset_id = %s
                ON CONFLICT (asset_id, role) DO NOTHING
                """,
                (asset_id, canonical_id),
            )

            # Copy image variant mappings (thumbnails)
            cur.execute(
                """
                INSERT INTO asset_image_variants (asset_id, role, variant_hash)
                SELECT %s, role, variant_hash
                FROM asset_image_variants
                WHERE asset_id = %s
                ON CONFLICT (asset_id, role) DO NOTHING
                """,
                (asset_id, canonical_id),
            )

            # Copy metadata
            cur.execute(
                """
                UPDATE assets a
                SET width = b.width,
                    height = b.height,
                    duration_seconds = b.duration_seconds,
                    status = 'ready',
                    canonical_asset_id = %s,
                    processed_at = NOW()
                FROM assets b
                WHERE a.asset_id = %s AND b.asset_id = %s
                """,
                (canonical_id, asset_id, canonical_id),
            )

            conn.commit()
            return True