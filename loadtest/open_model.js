// loadtest/open_model.js
//
// OPEN model: a fixed arrival rate of new uploads/sec, independent of how fast
// the system responds. Good for finding the latency knee and watching queue
// lag grow when arrival rate > service rate — a live demonstration of Little's
// Law (L = λW). When the worker can't keep up, the Redis stream depth climbs
// even though the API keeps accepting work.
//
//   RATE=5 DURATION=3m k6 run loadtest/open_model.js
//
// Prefer the wrapper: ./loadtest/run.sh open --rate 5/s --duration 3m

import { runUploadFlow, sloThresholds } from "./lib.js";

const RATE = parseInt(__ENV.RATE || "5", 10); // iterations/sec
const DURATION = __ENV.DURATION || "3m";
const MAX_VUS = parseInt(__ENV.MAX_VUS || String(RATE * 20), 10);

export const options = {
  scenarios: {
    open: {
      executor: "constant-arrival-rate",
      rate: RATE,
      timeUnit: "1s",
      duration: DURATION,
      preAllocatedVUs: Math.max(10, RATE * 2),
      maxVUs: MAX_VUS,
    },
  },
  thresholds: sloThresholds,
};

export default function () {
  runUploadFlow();
}
