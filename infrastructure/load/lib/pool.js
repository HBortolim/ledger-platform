// Shared pieces of the transfer load profiles (SPEC.md §10.6, NFR-PERF-4).
//
// Every URL and credential comes from the environment, so the same scripts run against a local
// `make up` stack and against a deployment: BASE_URL, USER_TOKEN, ADMIN_TOKEN, and optionally
// LEDGER_URL (the Ledger Service's internal admin API, used for the end-of-run invariant check).
import http from 'k6/http';
import { check, fail } from 'k6';
import { Counter } from 'k6/metrics';

export const invariantViolations = new Counter('invariant_violations');

export const config = {
  baseUrl: __ENV.BASE_URL || 'http://wallet-service:8080',
  ledgerUrl: __ENV.LEDGER_URL || '',
  userToken: __ENV.USER_TOKEN || '',
  adminToken: __ENV.ADMIN_TOKEN || '',
  poolSize: parseInt(__ENV.POOL_SIZE || '200', 10),
  funding: __ENV.FUNDING || '10000.00',
  amount: __ENV.AMOUNT || '1.00',
};

// Tag every measured request so thresholds see transfers only, not the setup and teardown traffic.
const TRANSFER_TAGS = { endpoint: 'transfer' };
const BATCH = 25;

function headers(token, extra) {
  return Object.assign({ 'Content-Type': 'application/json', Authorization: `Bearer ${token}` }, extra);
}

export function requireEnv() {
  if (!config.userToken || !config.adminToken) {
    fail('USER_TOKEN and ADMIN_TOKEN are required (scripts/load.sh mints them for a local run)');
  }
}

// createPool creates `poolSize` wallets owned by the load-test user and funds each through the
// admin deposit endpoint (FR-6). A half-funded pool would turn the run into an INSUFFICIENT_FUNDS
// test, so any non-201 aborts the setup.
export function createPool() {
  requireEnv();
  const wallets = [];
  for (let start = 0; start < config.poolSize; start += BATCH) {
    const n = Math.min(BATCH, config.poolSize - start);
    const reqs = [];
    for (let i = 0; i < n; i++) {
      reqs.push(['POST', `${config.baseUrl}/wallets`, JSON.stringify({ currency: 'BRL' }),
        { headers: headers(config.userToken), tags: { endpoint: 'setup' } }]);
    }
    for (const res of http.batch(reqs)) {
      if (res.status !== 201) fail(`create wallet: ${res.status} ${res.body}`);
      wallets.push(res.json('walletId'));
    }
  }

  for (let start = 0; start < wallets.length; start += BATCH) {
    const reqs = wallets.slice(start, start + BATCH).map((id) => ['POST', `${config.baseUrl}/deposits`,
      JSON.stringify({ destinationWalletId: id, amount: config.funding }),
      { headers: headers(config.adminToken, { 'Idempotency-Key': crypto.randomUUID() }), tags: { endpoint: 'setup' } }]);
    for (const res of http.batch(reqs)) {
      if (res.status !== 201) fail(`fund wallet: ${res.status} ${res.body}`);
    }
  }
  return wallets;
}

// transfer sends one POST /transfers between two distinct random wallets of the pool.
export function transfer(wallets) {
  const a = Math.floor(Math.random() * wallets.length);
  let b = Math.floor(Math.random() * (wallets.length - 1));
  if (b >= a) b += 1; // distinct from a, uniform over the rest
  const res = http.post(`${config.baseUrl}/transfers`,
    JSON.stringify({ sourceWalletId: wallets[a], destinationWalletId: wallets[b], amount: config.amount }),
    { headers: headers(config.userToken, { 'Idempotency-Key': crypto.randomUUID() }), tags: TRANSFER_TAGS });
  check(res, { 'status 201': (r) => r.status === 201 }, TRANSFER_TAGS);
  return res;
}

// reconcile runs one Ledger Service reconciliation pass and counts a violation for
// each check that is not clean. Only the checks that hold at any instant are counted: the
// invariant scan and the money supply are single-snapshot, whereas projection drift ignores
// wallets that moved in the last 10 s, and a run ends with traffic still settling. A pass already
// running answers 409 and is retried.
export function reconcile() {
  if (!config.ledgerUrl) {
    console.warn('LEDGER_URL not set: skipping the end-of-run invariant check');
    invariantViolations.add(0);
    return;
  }
  let res;
  for (let attempt = 0; attempt < 10; attempt++) {
    res = http.post(`${config.ledgerUrl}/admin/reconciliation/run`, null, { tags: { endpoint: 'reconcile' } });
    if (res.status !== 409) break;
  }
  if (res.status !== 200) {
    console.error(`reconciliation returned ${res.status}: ${res.body}`);
    invariantViolations.add(1);
    return;
  }
  const report = res.json();
  const bad = (report.invariant.clean ? 0 : 1) + (report.moneySupply.clean ? 0 : 1);
  invariantViolations.add(bad);
  console.log(`reconciliation: invariant=${report.invariant.clean} moneySupply=${report.moneySupply.clean} ` +
    `(ledgerTotal=${report.moneySupply.ledgerTotal} cachedTotal=${report.moneySupply.cachedTotal}) ` +
    `projectionFindings=${report.projection.findingCount}`);
}

// Overridable by env so other tasks can reuse a profile (the chaos drills run the steady profile
// at a lower rate as background load).
export function envInt(name, fallback) {
  return parseInt(__ENV[name] || String(fallback), 10);
}
export function envStr(name, fallback) {
  return __ENV[name] || fallback;
}
