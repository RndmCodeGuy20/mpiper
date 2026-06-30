// loadtest/lib.js
//
// Shared helpers for the MPiper k6 load harness. Each iteration performs the
// REAL client flow from the host, exactly like scripts/demo-e2e.sh:
//
//   1. POST /api/v1/storage/presign           -> uploadUrl + assetId
//   2. PUT <uploadUrl> (bytes straight to MinIO at the public endpoint)
//   3. GET /api/v1/assets/{assetId}/complete  -> enqueues processing
//
// Dedup defeat: the worker dedups by content hash, so identical bytes do ~no
// work after the first asset. We append per-iteration unique bytes AFTER the
// JPEG end-of-image marker (decoders ignore trailing bytes), giving a valid but
// unique-hash image so we measure real per-job cost. See track-03 §7.

import http from "k6/http";
import { check } from "k6";
import { Trend, Rate, Counter } from "k6/metrics";

// --- Config (host-run; see run.sh) ---------------------------------------
export const BASE_URL = __ENV.BASE_URL || "http://localhost:5010";
const TOKEN = __ENV.K6_TOKEN || ""; // minted by run.sh (AES-GCM auth token)

// --- Custom metrics mapped to the SLOs (§4.2) ----------------------------
export const presignLatency = new Trend("mpiper_presign_latency_ms", true);
export const uploadLatency = new Trend("mpiper_upload_latency_ms", true);
export const completeLatency = new Trend("mpiper_complete_latency_ms", true);
export const flowErrors = new Rate("mpiper_flow_errors");
export const assetsSubmitted = new Counter("mpiper_assets_submitted");

// --- Fixture (loaded once at init) ---------------------------------------
// open() resolves relative to this script. 'b' returns an ArrayBuffer.
const FIXTURE_PATH =
  __ENV.FIXTURE_PATH || "../worker/tests/test_assets/image.jpg";
const baseFixture = new Uint8Array(open(FIXTURE_PATH, "b"));

function authHeaders(extra) {
  return Object.assign({ Authorization: `Bearer ${TOKEN}` }, extra || {});
}

// Build a unique-hash image: base JPEG + a unique trailer (VU/iter/random).
function uniqueImageBytes() {
  const tag = `\nMPIPER-LOADTEST-${__VU}-${__ITER}-${Math.random()}`;
  // k6's JS runtime has no TextEncoder; the tag is ASCII so charCodeAt suffices.
  const suffix = new Uint8Array(tag.length);
  for (let i = 0; i < tag.length; i++) suffix[i] = tag.charCodeAt(i) & 0xff;
  const out = new Uint8Array(baseFixture.length + suffix.length);
  out.set(baseFixture, 0);
  out.set(suffix, baseFixture.length);
  return out.buffer;
}

// Run one full presign -> upload -> complete flow. Returns true on success.
export function runUploadFlow() {
  const bytes = uniqueImageBytes();
  const contentType = "image/jpeg";

  // 1. presign
  const presignRes = http.post(
    `${BASE_URL}/api/v1/storage/presign`,
    JSON.stringify({
      fileName: `loadtest-${__VU}-${__ITER}.jpg`,
      contentType,
      size: bytes.byteLength,
    }),
    { headers: authHeaders({ "Content-Type": "application/json" }), tags: { step: "presign" } }
  );
  presignLatency.add(presignRes.timings.duration);
  const presignOk = check(presignRes, {
    "presign 2xx": (r) => r.status >= 200 && r.status < 300,
  });
  if (!presignOk) {
    flowErrors.add(1);
    return false;
  }

  const data = presignRes.json("data");
  if (!data || !data.uploadUrl || !data.assetId) {
    flowErrors.add(1);
    return false;
  }

  // 2. upload bytes straight to object storage (public endpoint)
  const uploadRes = http.put(data.uploadUrl, bytes, {
    headers: { "Content-Type": contentType },
    tags: { step: "upload" },
  });
  uploadLatency.add(uploadRes.timings.duration);
  const uploadOk = check(uploadRes, {
    "upload 2xx": (r) => r.status >= 200 && r.status < 300,
  });
  if (!uploadOk) {
    flowErrors.add(1);
    return false;
  }

  // 3. complete -> enqueue processing
  const completeRes = http.get(
    `${BASE_URL}/api/v1/assets/${data.assetId}/complete`,
    { headers: authHeaders(), tags: { step: "complete" } }
  );
  completeLatency.add(completeRes.timings.duration);
  const completeOk = check(completeRes, {
    "complete 2xx": (r) => r.status >= 200 && r.status < 300,
  });
  if (!completeOk) {
    flowErrors.add(1);
    return false;
  }

  flowErrors.add(0);
  assetsSubmitted.add(1);
  return true;
}

// Thresholds shared by both models, derived from the §4.2 SLOs.
export const sloThresholds = {
  // Presign SLO: p95 < 150ms.
  mpiper_presign_latency_ms: ["p(95)<150"],
  // End-to-end client errors must stay under 1% (job success SLO > 99%).
  mpiper_flow_errors: ["rate<0.01"],
  // Overall check pass rate.
  checks: ["rate>0.99"],
};
