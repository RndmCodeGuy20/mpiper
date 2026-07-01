// loadtest/closed_model.js
//
// CLOSED model: a fixed pool of VUs, each looping the upload flow as fast as
// the system allows. Good for finding max throughput and the saturation point
// of the (single-threaded) worker.
//
//   VUS=10 DURATION=2m k6 run loadtest/closed_model.js
//   STAGES=1            -> ramping profile (override via env, see below)
//
// Prefer the wrapper: ./loadtest/run.sh closed --vus 10 --duration 2m

import { runUploadFlow, sloThresholds } from "./lib.js";

const VUS = parseInt(__ENV.VUS || "10", 10);
const DURATION = __ENV.DURATION || "2m";

export const options = {
  scenarios: {
    closed: __ENV.RAMP === "1"
      ? {
          executor: "ramping-vus",
          startVUs: 1,
          stages: [
            { duration: "30s", target: VUS },
            { duration: DURATION, target: VUS },
            { duration: "30s", target: 0 },
          ],
          gracefulStop: "30s",
        }
      : {
          executor: "constant-vus",
          vus: VUS,
          duration: DURATION,
          gracefulStop: "30s",
        },
  },
  thresholds: sloThresholds,
};

export default function () {
  runUploadFlow();
}
