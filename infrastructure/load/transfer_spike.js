// SPEC.md §10.6: ramp from 10 to 200 RPS over 30 s. The system must either hold its SLOs or fail
// fast: no request may take longer than 5 s (note the Wallet Service's own ledger read timeout is
// 5 s, so this line is exactly where a stuck request would surface). Gated on max, not a percentile.
import { createPool, transfer, reconcile, invariantViolations, envInt } from './lib/pool.js';

export const options = {
  setupTimeout: '300s',
  scenarios: {
    spike: {
      executor: 'ramping-arrival-rate',
      startRate: 10,
      timeUnit: '1s',
      preAllocatedVUs: 100,
      maxVUs: envInt('MAX_VUS', 1000),
      stages: [
        { target: 200, duration: '30s' },
        { target: 200, duration: '1m' },
        { target: 0, duration: '30s' },
      ],
    },
  },
  thresholds: {
    'http_req_duration{endpoint:transfer}': ['max<5000'],
    invariant_violations: ['count==0'],
  },
};

export const setup = createPool;
export default function (wallets) { transfer(wallets); }
export function teardown() { invariantViolations.add(0); reconcile(); }
