// NFR-PERF-4: sustained throughput of at least 100 TPS for 5 minutes with an error rate under 0.1%.
// Latency is reported but not gated here: NFR-PERF-4 is about throughput and errors (the latency
// SLOs are NFR-PERF-1 at 50 RPS, in transfer_steady.js).
import { createPool, transfer, reconcile, invariantViolations, envInt, envStr } from './lib/pool.js';

export const options = {
  setupTimeout: '300s',
  scenarios: {
    throughput: {
      executor: 'constant-arrival-rate',
      rate: envInt('RATE', 100),
      timeUnit: '1s',
      duration: envStr('DURATION', '5m'),
      preAllocatedVUs: 100,
      maxVUs: envInt('MAX_VUS', 500),
    },
  },
  thresholds: {
    'http_req_failed{endpoint:transfer}': ['rate<0.001'],
    dropped_iterations: ['count==0'],
    invariant_violations: ['count==0'],
  },
};

export const setup = createPool;
export default function (wallets) { transfer(wallets); }
export function teardown() { invariantViolations.add(0); reconcile(); }
