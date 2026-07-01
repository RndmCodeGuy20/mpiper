# ADR 0003: Adaptive Bitrate Transcoding Pipeline

## Status
Accepted

## Context
Current pipeline produces single 720p H.264 output (`worker/processing/videos.py:transcode_720p`). Need multi-bitrate HLS/DASH for adaptive streaming across devices and network conditions.

## Decision

### ABR Ladder (H.264 baseline → high profiles)
| Variant | Resolution | Bitrate | Audio | Profile | Use Case |
|---------|------------|---------|-------|---------|----------|
| 1080p   | 1920×1080  | 5000k   | 192k  | high    | Desktop, TV |
| 720p    | 1280×720   | 2800k   | 128k  | high    | Tablet, Mobile WiFi |
| 480p    | 854×480    | 1400k   | 128k  | main    | Mobile 3G/4G |
| 360p    | 640×360    | 800k    | 96k   | baseline| Mobile 2G, fallback |
| 240p    | 426×240    | 400k    | 64k   | baseline| Audio-only fallback |

### Encoding Strategy
- **Single-pass filter_complex** — one ffmpeg invocation encodes all rungs simultaneously via `split` + parallel `scale` + `encode` filtergraph
- **Segment duration**: 6s (VOD), 2s (Live/LL-HLS)
- **Segment format**: fMP4 (CMAF) for HLS + DASH shared segments
- **Manifests**: HLS v7 (`#EXT-X-VERSION:7`) + DASH MPD (ISO BMFF)
- **Deduplication**: Content-hash based (already implemented in `processor.py:check_for_duplicate`)

### Hardware Acceleration
| Platform | Encoder | Detection |
|----------|---------|-----------|
| NVIDIA GPU | h264_nvenc | `nvidia-smi` / `ffmpeg -encoders` |
| Intel QSV | h264_qsv | `vainfo` / `ffmpeg -encoders` |
| AMD VCN | h264_amf | `rocminfo` / `ffmpeg -encoders` |
| Apple VT | h264_videotoolbox | `system_profiler` / `ffmpeg -encoders` |
| Fallback | libx264 | Always available |

Selection logic: prefer HW encoder → fallback to libx264 `preset=fast`/`crf=22`.

### Output Structure (Object Storage)
```
media/{owner_id}/processed/{asset_id}/
├── hls/
│   ├── master.m3u8
│   ├── 1080p/index.m3u8 + seg_*.m4s
│   ├── 720p/index.m3u8 + seg_*.m4s
│   ├── 480p/index.m3u8 + seg_*.m4s
│   ├── 360p/index.m3u8 + seg_*.m4s
│   └── 240p/index.m3u8 + seg_*.m4s
└── dash/
    ├── manifest.mpd
    └── (shared fMP4 segments via BaseURL)
```

### Database Schema Extensions
```sql
-- Add to variants.video
ALTER TABLE variants.video ADD COLUMN IF NOT EXISTS manifest_url TEXT;
ALTER TABLE variants.video ADD COLUMN IF NOT EXISTS abr_ladder JSONB;  -- stores ladder config used
ALTER TABLE variants.video ADD COLUMN IF NOT EXISTS segment_duration INT DEFAULT 6;
ALTER TABLE variants.video ADD COLUMN IF NOT EXISTS codec_profile TEXT; -- baseline/main/high
```

## Consequences

### Positive
- 3-5x faster than sequential encodes (single decode, single filtergraph)
- True ABR playback on all HLS/DASH clients
- CMAF single-storage for both HLS and DASH
- Content-hash deduplication works across ABR variants

### Negative
- Requires GPU instances for cost-effective HD+ encoding
- Manifest generation adds eventual consistency (segments appear before manifest updates)
- Higher storage: ~2.5× single 720p

### Risks
- ffmpeg filter_complex syntax is brittle; need integration tests per codec/hwaccel combo
- HW encoder availability varies by cloud provider/instance type

## Implementation Plan

| Phase | Task | Files |
|-------|------|-------|
| P0 | Single-pass ABR filtergraph generator | `worker/processing/abr.py` |
| P0 | HLS/DASH manifest writer (fMP4 segments) | `worker/processing/manifests.py` |
| P0 | HW acceleration detection + fallback | `worker/processing/hwaccel.py` |
| P0 | Integrate into `process_video_file` | `worker/processing/videos.py` |
| P1 | Per-title encoding (convex hull analysis) | `worker/processing/per_title.py` |
| P1 | Cost tracking per asset (CPU-sec, GB-egress) | `internal/metrics/cost.go` |
| P2 | DRM integration (Widevine/FairPlay/PlayReady) | `worker/processing/drm.py` |
| P2 | Thumbnail sprites + WebVTT for scrubbing | `worker/processing/sprites.py` |

## Related ADRs
- ADR 0001: Ingress Outbox & Idempotent Consumer (`ingress-outbox-and-idempotent-consumer.md`)
- ADR 0002: Reliability & Correctness (`reliability-and-correctness.md`)