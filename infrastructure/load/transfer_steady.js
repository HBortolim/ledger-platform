// SPEC.md §10.6: 50 RPS for 5 minutes. Asserts p95/p99 latency (NFR-PERF-1), no failed requests
// and no invariant violations. A constant ARRIVAL rate, not a fixed number of VUs: with VUs the
// request rate would be 50 / (latency + think time), not 50.
import { createPool, transfer, reconcile, invariantViolations, envInt, envStr } from './lib/pool.js';

export const options = {
  setupTimeout: '300s',
  scenarios: {
    steady: {
      executor: 'constant-arrival-rate',
      rate: envInt('RATE', 50),
      timeUnit: '1s',
      duration: envStr('DURATION', '5m'),
      preAllocatedVUs: 50,
      maxVUs: envInt('MAX_VUS', 300),
    },
  },
  thresholds: {
    'http_req_duration{endpoint:transfer}': ['p(95)<200', 'p(99)<500'],
    'http_req_failed{endpoint:transfer}': ['rate<0.001'],
    // If k6 runs out of VUs it silently sends LESS than the target rate; this makes that a failure.
    dropped_iterations: ['count==0'],
    invariant_violations: ['count==0'],
  },
};

export const setup = createPool;
export default function (wallets) { transfer(wallets); }
export function teardown() { invariantViolations.add(0); reconcile(); }
