# ADR 0004: Image Pipeline Advancements

## Status
Proposed

## Context
Current pipeline (`worker/processing/images.py`) generates 3 fixed WebP variants (thumbnail 256², display_small 512w, display_large 1280w). This wastes bandwidth on simple images, upscales small images, and lacks modern formats (AVIF, JPEG XL).

## Decision

### Phase P0 — Immediate Wins (2-3 days)

#### 1. Modern Format Stack
Encode each variant as AVIF → JPEG XL → WebP → JPEG (progressive), serve via `Accept` header negotiation.

| Format | MIME | Pillow/lib | Quality/Speed | Browser Support |
|--------|------|------------|---------------|-----------------|
| AVIF   | image/avif | pillow-avif-plugin / libavif | q=50, speed=6 | Chrome 85+, Firefox 93+, Safari 16.4+ |
| JPEG XL| image/jxl | libjxl / pillow-jxl | q=50, effort=7 | Chrome 110+ (flag), Safari 17+ (flag) |
| WebP   | image/webp | Pillow (built-in) | q=75, method=6 | Universal |
| JPEG   | image/jpeg | Pillow (built-in) | q=82, progressive, optimize | Universal |

#### 2. Adaptive Variant Ladder (Per-Image)
Replace fixed widths with content-aware targets:

```python
def compute_adaptive_variants(src_width: int, src_height: int, mime: str) -> list[dict]:
    aspect = src_width / src_height
    mpx = (src_width * src_height) / 1_000_000
    
    # Target megapixels per role (no upscale > 1.2x)
    targets = [0.07, 0.3, 1.0, 3.0]  # thumb, small, medium, large
    variants = []
    
    for i, tgt in enumerate(targets):
        if tgt > mpx * 1.2:
            break
        w = int((tgt * 1_000_000 * aspect) ** 0.5)
        h = int(w / aspect)
        variants.append({
            "role": ["thumbnail", "small", "medium", "large"][i],
            "width": w, "height": h,
            "format": "avif",  # primary; others generated as fallbacks
            "quality": 55 + i * 5,
        })
    return variants
```

#### 3. Parallel Variant Generation
```python
from concurrent.futures import ThreadPoolExecutor

def process_image_file_parallel(asset_id, owner_id, path, content_hash, pg_pool, storage, cfg):
    with Image.open(path) as img:
        variants = compute_adaptive_variants(img.width, img.height, mime)
        
        with ThreadPoolExecutor(max_workers=cfg.image_workers) as pool:
            futures = {
                pool.submit(encode_and_upload, img, v, asset_id, owner_id, storage, pg_pool): v
                for v in variants
            }
            for fut in as_completed(futures):
                fut.result()  # propagate exceptions
```

### Phase P1 — Smart Operations (1-2 weeks)

#### 4. Smart Crop (Saliency / Focal Point)
- **Option A**: OpenCV spectral residual saliency (fast, no model)
- **Option B**: Client-provided focal point `{x: 0.5, y: 0.3}` in upload metadata
- Apply to `thumbnail` and `small` variants (center-crop → content-aware crop)

#### 5. Animated Image Support
- Detect GIF/WebP/APNG animation via `Image.is_animated`
- Re-encode as animated WebP (method=6, lossless=false) → 70-80% size reduction vs GIF
- Extract first frame as static poster (WebP/AVIF)
- Store frame count, loop count, duration in `variants.image` metadata

#### 6. Color Space Normalization
- `ImageOps.exif_transpose()` for orientation
- Convert to sRGB ICC profile; preserve Display P3 if source has it
- Optional: soft-proof for sRGB gamut mapping

### Phase P2 — Frontend Integration & Quality Assurance (1 week)

#### 7. Responsive HTML Generator
```python
def generate_picture_html(variants: list[dict], base_url: str, alt: str = "") -> str:
    """Generate <picture> with format fallbacks + srcset."""
    by_role = {}
    for v in variants:
        by_role.setdefault(v["role"], []).append(v)
    
    sources = []
    for role, fmts in by_role.items():
        for fmt in fmts:
            sources.append(
                f'<source type="image/{fmt["format"]}" '
                f'srcset="{base_url}/{role}.{fmt["format"]}" '
                f'media="(min-width: {fmt["width"]}px)">'
            )
    sources.append(f'<img src="{base_url}/medium.webp" alt="{alt}" loading="lazy">')
    return "<picture>\n" + "\n".join(sources) + "\n</picture>"
```

#### 8. Perceptual Quality Metrics
- Integrate SSIMULACRA2 (Google) or Butteraugli for automated QA
- Fail build if variant quality drops below threshold
- Store metric scores in `variants.image.quality_score`

## Database Schema Extensions

```sql
ALTER TABLE variants.image ADD COLUMN IF NOT EXISTS format_set TEXT[];  -- ['avif','webp','jpeg']
ALTER TABLE variants.image ADD COLUMN IF NOT EXISTS quality_score REAL; -- SSIMULACRA2 score
ALTER TABLE variants.image ADD COLUMN IF NOT EXISTS is_animated BOOLEAN DEFAULT FALSE;
ALTER TABLE variants.image ADD COLUMN IF NOT EXISTS frame_count INT;
ALTER TABLE variants.image ADD COLUMN IF NOT EXISTS focal_point_x REAL;
ALTER TABLE variants.image ADD COLUMN IF NOT EXISTS focal_point_y REAL;
```

## Storage Key Structure (Backward Compatible)

```
media/{owner_id}/processed/{asset_id}/
├── thumbnail.avif
├── thumbnail.webp
├── thumbnail.jpg
├── small.avif
├── small.webp
├── small.jpg
├── medium.avif
├── medium.webp
├── medium.jpg
├── large.avif
├── large.webp
├── large.jpg
├── poster.avif          -- for animated sources
├── poster.webp
└── animation.webp       -- animated WebP (replaces GIF)
```

## Consequences

### Positive
- 30-50% bandwidth reduction (AVIF vs WebP)
- No wasted upscales (adaptive ladder)
- 3-4x faster processing (parallel Pillow)
- Animated GIF → WebP: 80% size reduction
- Drop-in `<picture>` HTML for frontend

### Negative
- AVIF encoding slower (~3x WebP); mitigate with `speed=6` + parallel workers
- JPEG XL browser support still behind flags
- More storage variants (4 formats × 4 roles = 16 files vs 3)
- OpenCV dependency for saliency (optional)

### Risks
- Pillow AVIF plugin requires `libavif` system package
- ThreadPoolExecutor GIL contention on CPU-heavy Pillow ops (mitigated: C extensions release GIL)
- Accept header parsing complexity at CDN edge

## Implementation Plan

| Phase | Task | Files | Effort |
|-------|------|-------|--------|
| P0 | AVIF/WebP/JXL encoder + format negotiation | `worker/processing/formats.py` | 1 day |
| P0 | Adaptive variant ladder | `worker/processing/adaptive_images.py` | 1 day |
| P0 | Parallel processing + integrate | `worker/processing/images.py` | 0.5 day |
| P1 | Smart crop (saliency/focal) | `worker/processing/smart_crop.py` | 2 days |
| P1 | Animated image support | `worker/processing/animated.py` | 1 day |
| P1 | Color space normalization | `worker/processing/color.py` | 1 day |
| P2 | `<picture>` HTML generator | `worker/processing/responsive.py` | 0.5 day |
| P2 | SSIMULACRA2 quality gate | `worker/processing/quality.py` | 2 days |

## Related ADRs
- ADR 0003: ABR Transcoding Pipeline (`abr-transcoding-pipeline.md`)
- ADR 0001: Ingress Outbox & Idempotent Consumer (`ingress-outbox-and-idempotent-consumer.md`)