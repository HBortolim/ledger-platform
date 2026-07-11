# Distributed Ledger & Wallet Platform — Specification

**Status:** Draft v1
**Audience:** Engineers building the system; reviewers evaluating it as a portfolio artifact.
**Reading order:** §1–§3 set context. §4 is the contract (functional + non-functional). §5–§8 are the build. §9 is how you prove it works. §10 is the timeline.

---

## 1. Purpose & Positioning

A polyglot, event-driven simulation of the core infrastructure behind a digital wallet — the kind of system a fintech, payment institution, or neobank runs in production. The project exists to demonstrate, in code that runs, the patterns that make financial software trustworthy: immutable double-entry accounting, atomic ledger posting, the outbox pattern, idempotency, eventually-consistent projections, and operational observability.

It is deliberately **not** a real payment system. It will not connect to banks, will not hold real money, and will not implement KYC, AML, fraud detection, or regulatory reporting. Those omissions are called out in §2.3.

### 1.1 What success looks like

The project is done when a reviewer can:

1. Clone the repo, run `docker compose up`, and have all services healthy within 60 seconds.
2. Execute a transfer via `curl`, observe the ledger entries in Postgres, the Kafka event, and the updated projection — all within 2 seconds.
3. Read the `docs/` folder and understand every architectural decision without having to read source code.
4. Run `make test` and see the full test suite (unit + integration + end-to-end) pass.
5. Kill any single service mid-transfer and observe the system recover without financial inconsistency.

---

## 2. Scope

### 2.1 In scope for v1

- Single-currency wallets (BRL).
- Wallet lifecycle: create, freeze, unfreeze, close.
- Internal wallet-to-wallet transfers.
- External deposits (simulated; no real PSP integration).
- External withdrawals (simulated).
- Double-entry ledger with append-only entries.
- Asynchronous balance projections.
- Idempotent transfer creation.
- Outbox-based event publication.
- JWT authentication.
- Structured logging, metrics, distributed tracing.

### 2.2 Explicitly out of scope

- Real money movement, real PSP integration, real KYC/AML.
- Multi-currency and FX.
- Fees, interest, scheduled transfers.
- User registration UI; users are seeded via SQL.
- Mobile or web frontends.
- Fraud detection, transaction limits beyond a hardcoded daily cap.
- Multi-region deployment, HA Postgres, Kafka replication tuning.
- Production-grade secrets management (env vars are sufficient).

---

## 3. Domain model

### 3.1 Entities

| Entity | Role | Mutability |
|---|---|---|
| Wallet | Container of funds, owned by a user. | Mutable status; balance is derived. |
| Ledger Transaction | A grouping of entries representing one financial operation. | Append-only. |
| Ledger Entry | The atomic accounting fact: a debit or credit against one account. | **Immutable.** Never updated, never deleted. |
| Balance Projection | Materialized view of a wallet's current balance. | Mutable; can be rebuilt from entries. |
| Idempotency Record | The cached response for a previously-seen idempotency key. | TTL-bound. |
| Outbox Event | A pending event awaiting publication to Kafka. | Deleted after successful publish. |

### 3.2 The core invariant

For every ledger transaction `T`:

```
sum(entry.signed_amount for entry in T.entries) == 0
```

This invariant is enforced in **three** places — defense in depth:

1. **Application code** in the Ledger Service before opening the DB transaction.
2. **A trigger or check constraint** on `ledger_transactions` that runs at commit time.
3. **A reconciliation job** (§7.5) that scans historical transactions and alerts on any imbalance.

If any of these three ever fires, it is a P0 incident in the simulated operational model.

### 3.3 Sign convention (decision)

Amounts are stored **unsigned** in `ledger_entries.amount` as `NUMERIC(19, 2)`. The direction is carried by `entry_type` (`DEBIT` | `CREDIT`). The signed amount used in the invariant check is computed:

```
signed = amount if entry_type = 'CREDIT' else -amount
```

Rationale: matches accounting convention, avoids ambiguity around negative zero, and makes the schema readable to anyone with an accounting background. The alternative (signed amounts, no `entry_type`) was rejected because it makes ad-hoc SQL queries harder to read.

### 3.4 Money representation

- Storage: `NUMERIC(19, 2)` in Postgres. Sufficient for BRL amounts up to ~10¹⁷.
- Wire format: JSON number with exactly 2 decimal places, e.g. `100.00`. Clients sending `100` or `100.5` get a 400.
- In Java: `BigDecimal` with `RoundingMode.UNNECESSARY` — any operation that would round throws.
- In Go: a custom `Money` type wrapping `decimal.Decimal` from `shopspring/decimal`. No `float64` for money, anywhere, ever.

---

## 4. Requirements

Requirements use the keywords MUST, SHOULD, MAY per RFC 2119. Each functional requirement has acceptance criteria phrased as testable assertions.

### 4.1 Functional requirements

#### FR-1: Create wallet

**User story:** As an authenticated user, I can create a new wallet so that I can hold funds.

**Endpoint:** `POST /wallets`

**Acceptance criteria:**

- AC-1.1: A valid request returns `201 Created` with the wallet ID and a `Location` header.
- AC-1.2: A request without a valid JWT returns `401`.
- AC-1.3: A request where `ownerId` does not match the JWT subject returns `403`.
- AC-1.4: A request with `currency != "BRL"` returns `422` with code `UNSUPPORTED_CURRENCY`.
- AC-1.5: The created wallet has `status = "ACTIVE"` and a balance projection of `0.00`.
- AC-1.6: The same user MAY hold multiple wallets; no uniqueness constraint on `(owner_id, currency)`.

#### FR-2: Retrieve wallet balance

**Endpoint:** `GET /wallets/{walletId}/balance`

**Acceptance criteria:**

- AC-2.1: Returns `200` with `{walletId, balance, currency, asOf}` where `asOf` is the projection's `updated_at`.
- AC-2.2: A caller who does not own the wallet returns `403`. (No "wallet not found" leak.)
- AC-2.3: Balance reflects all ledger entries posted **before or at** `asOf`. The response MUST surface `asOf` so callers can detect staleness.
- AC-2.4: If the projection is more than 5 seconds behind the ledger (see §4.2 NFR-CONS-2), the response includes `"stale": true`. This requires the Wallet Service to compare `asOf` to a freshness signal — see §6.3.

#### FR-3: Freeze / unfreeze wallet

**Endpoints:** `POST /wallets/{walletId}/freeze`, `POST /wallets/{walletId}/unfreeze`

**Acceptance criteria:**

- AC-3.1: Freezing an `ACTIVE` wallet sets status to `FROZEN`; returns `200`.
- AC-3.2: Freezing a `FROZEN` wallet is idempotent: returns `200` with no state change.
- AC-3.3: Freezing a `CLOSED` wallet returns `409 Conflict` with code `WALLET_CLOSED`.
- AC-3.4: A `FROZEN` wallet rejects new transfers as source **and** destination (see FR-5, AC-5.7).
- AC-3.5: Freeze/unfreeze actions are recorded in an audit log table with `actor_id`, `wallet_id`, `action`, `at`.

#### FR-4: Close wallet

**Endpoint:** `POST /wallets/{walletId}/close`

**Acceptance criteria:**

- AC-4.1: A wallet with a non-zero balance cannot be closed; returns `409` with code `NONZERO_BALANCE`.
- AC-4.2: A closed wallet rejects all transfers and cannot be reopened.
- AC-4.3: Historical ledger entries for a closed wallet remain queryable.

#### FR-5: Create transfer

**Endpoint:** `POST /transfers`

This is the heart of the system. The acceptance criteria below are exhaustive.

**Acceptance criteria — happy path:**

- AC-5.1: A valid request returns `201` with `{transferId, status: "COMPLETED", postedAt}`.
- AC-5.2: After completion, the ledger contains exactly two entries: one `DEBIT` on source, one `CREDIT` on destination, with equal amounts and the same `transaction_id`.
- AC-5.3: Within the SLO defined in NFR-CONS-2, both wallet projections reflect the new balances.
- AC-5.4: Exactly one `LEDGER_POSTED` event is published to Kafka per completed transfer.

**Acceptance criteria — validation:**

- AC-5.5: `sourceWalletId == destinationWalletId` returns `422` code `SELF_TRANSFER`.
- AC-5.6: `amount <= 0` returns `422` code `INVALID_AMOUNT`.
- AC-5.7: If either wallet is not `ACTIVE`, returns `422` code `WALLET_NOT_ACTIVE` with which wallet failed.
- AC-5.8: If currencies of source and destination differ, returns `422` code `CURRENCY_MISMATCH`. (In v1 this can't actually happen since only BRL exists, but the check is implemented.)
- AC-5.9: Insufficient available balance returns `422` code `INSUFFICIENT_FUNDS`. The response MUST NOT reveal the current balance to a caller who doesn't own the source wallet — but since transfers require source-wallet ownership (AC-5.11), this is moot in v1.
- AC-5.10: Amount above the daily per-wallet cap (hardcoded to 100,000.00 BRL) returns `422` code `DAILY_LIMIT_EXCEEDED`.
- AC-5.11: The JWT subject MUST own the source wallet, else `403`.

**Acceptance criteria — idempotency:**

- AC-5.12: A request with header `Idempotency-Key: <key>` is recorded. A subsequent request with the same key from the same user MUST return the original response, byte-for-byte (same `transferId`, same status, same body), regardless of whether the second request body is identical.
- AC-5.13: If a second request arrives with the same key but a **different** request body (different amount, different wallets), the service returns `422` with code `IDEMPOTENCY_KEY_MISMATCH`. This is detected via a fingerprint hash stored alongside the key.
- AC-5.14: Idempotency records have a TTL of 24 hours. After expiry, a key may be reused.
- AC-5.15: Requests without an `Idempotency-Key` header are accepted but the burden of avoiding duplicates is on the client. The service MUST log a warning.
- AC-5.16: While a transfer is in flight (key recorded, response not yet returned), a duplicate request waits up to 5 seconds for the original to complete, then returns the result. After 5 seconds, returns `409` code `IN_PROGRESS`.

**Acceptance criteria — concurrency:**

- AC-5.17: Two concurrent transfers from the same source wallet, each individually valid, where the sum would overdraw the wallet: exactly one succeeds with `201`, the other returns `422 INSUFFICIENT_FUNDS`. Never both succeed. Never both fail with a server error. This is the **money-cannot-be-created-from-nothing** test (§9.4).
- AC-5.18: Concurrent transfers between disjoint wallet pairs MUST NOT block each other. (No global lock.)

#### FR-6: External deposit (simulated)

**Endpoint:** `POST /deposits` — admin-only.

**Acceptance criteria:**

- AC-6.1: Posts a single transaction with a `DEBIT` on the system "external funding" account and a `CREDIT` on the destination wallet. The invariant holds because the external account is also tracked in the ledger.
- AC-6.2: Requires an admin JWT (claim `role: admin`).
- AC-6.3: Idempotent on `Idempotency-Key` (same rules as FR-5).

#### FR-7: External withdrawal (simulated)

**Endpoint:** `POST /withdrawals`

Symmetric to FR-6. Debits the user wallet, credits the external account.

### 4.2 Non-functional requirements

Each NFR has a metric, a target, and how it's measured. "Should be fast" is not an NFR.

#### Performance

| ID | Requirement | Target | Measured by |
|---|---|---|---|
| NFR-PERF-1 | Wallet Service `POST /transfers` end-to-end latency | p50 < 80 ms, p95 < 200 ms, p99 < 500 ms | Prometheus histogram, k6 load test at 50 RPS |
| NFR-PERF-2 | Ledger Service `POST /ledger/postings` latency | p95 < 50 ms, p99 < 150 ms | Prometheus histogram |
| NFR-PERF-3 | Projection lag (time from ledger commit to projection update) | p95 < 2 s, p99 < 5 s | Custom metric `projection_lag_seconds` |
| NFR-PERF-4 | Sustained transfer throughput on a single laptop (8 CPU, 16 GB) | ≥ 100 TPS for 5 minutes with error rate < 0.1% | k6 load test profile in `tests/load/` |

#### Consistency

| ID | Requirement |
|---|---|
| NFR-CONS-1 | Ledger entries within a single transaction commit atomically. A partial transaction is never observable. |
| NFR-CONS-2 | Balance projections are eventually consistent with p95 lag < 2s. Stale reads MUST be detectable by the client (AC-2.4). |
| NFR-CONS-3 | The double-entry invariant (§3.2) holds for 100% of committed transactions. A reconciliation job verifies this hourly in the simulated environment. |
| NFR-CONS-4 | Once `LEDGER_POSTED` is published, the corresponding ledger entries are durable in Postgres. (Outbox pattern guarantees this.) |
| NFR-CONS-5 | The system never creates or destroys money. The sum of all wallet balances + the external funding account = 0 at all times after every committed transaction. |

#### Availability & reliability

| ID | Requirement |
|---|---|
| NFR-AVAIL-1 | Each service exposes `GET /health/live` (process is up) and `GET /health/ready` (dependencies reachable) per the Kubernetes convention. |
| NFR-AVAIL-2 | A Ledger Service crash mid-transaction leaves no partial state. Postgres transaction handles this. |
| NFR-AVAIL-3 | A Projection Service crash does not lose events. Kafka offsets are committed only after the projection write commits. |
| NFR-AVAIL-4 | The Wallet Service degrades gracefully if the Ledger Service is unreachable: returns `503 Service Unavailable` with `Retry-After`, never `500`. |
| NFR-AVAIL-5 | The outbox worker recovers automatically after a Kafka outage. Events accumulate in the outbox table and drain when Kafka returns. |

#### Security

| ID | Requirement |
|---|---|
| NFR-SEC-1 | All endpoints except `/health/*` and `/metrics` require a valid JWT. |
| NFR-SEC-2 | JWTs are signed with RS256. The public key is loaded from disk at startup. v1 does not implement key rotation. |
| NFR-SEC-3 | JWT subject MUST match wallet ownership for all wallet-scoped operations. |
| NFR-SEC-4 | All input is validated: type, range, length, format. Validation happens at the Wallet Service edge — the Ledger Service trusts its caller. |
| NFR-SEC-5 | No PII in logs. Wallet IDs and user IDs are UUIDs and are loggable; emails and names are not. |
| NFR-SEC-6 | SQL is parameterized everywhere. Code review checklist enforces this; `grep` for string concatenation in queries is part of CI. |
| NFR-SEC-7 | Idempotency keys are scoped per-user. User A's key `abc` and User B's key `abc` are distinct records. |

#### Observability

| ID | Requirement |
|---|---|
| NFR-OBS-1 | Logs are structured JSON, written to stdout, include `trace_id`, `span_id`, `service`, `level`, `msg`. |
| NFR-OBS-2 | Every request carries a `trace_id` propagated via W3C `traceparent` header across HTTP and as a Kafka header across events. |
| NFR-OBS-3 | Each service exposes `/metrics` in Prometheus format. Required metrics per service listed in §7.3. |
| NFR-OBS-4 | The Grafana dashboard `Transfers Overview` shows: transfer RPS, p50/p95/p99 latency, error rate by code, projection lag, Kafka consumer lag, outbox depth. |
| NFR-OBS-5 | A single transfer can be traced end-to-end in Jaeger from `POST /transfers` through to the projection write. |

#### Auditability

| ID | Requirement |
|---|---|
| NFR-AUDIT-1 | Every ledger entry is immutable. There is no `UPDATE` or `DELETE` permission on `ledger_entries` for the application DB user. |
| NFR-AUDIT-2 | Corrections happen only via compensating transactions. The original transaction stays. A reversal links to its origin via `reverses_transaction_id`. |
| NFR-AUDIT-3 | Wallet status changes are logged in `wallet_audit_log` with actor, timestamp, before/after status. |

---

## 5. Architecture

### 5.1 Service map

```
                ┌──────────────────────┐
                │ API Clients          │
                └──────────┬───────────┘
                           │ HTTPS + JWT
                           ▼
                ┌──────────────────────┐
                │ Wallet Service       │  Java 21, Spring Boot 3
                │ - validation         │
                │ - idempotency        │
                │ - orchestration      │
                └──────────┬───────────┘
                           │ gRPC or HTTP/JSON (decision §5.5)
                           ▼
                ┌──────────────────────┐
                │ Ledger Service       │  Go 1.22
                │ - posting            │
                │ - invariants         │
                │ - outbox             │
                └──────────┬───────────┘
                           │
                    ┌──────┴──────┐
                    ▼             ▼
                 Postgres       Kafka
                                  │
                ┌─────────────────┴────────────────┐
                ▼                                  ▼
       ┌────────────────────┐          ┌────────────────────┐
       │ Projection Service │          │ Notification       │
       │ Go 1.22            │          │ Service (optional) │
       └────────┬───────────┘          └────────────────────┘
                ▼
            Postgres
            (read model)
```

### 5.2 Service responsibilities

Each service owns a clear slice of the system. The rule of thumb: **if you can't say what a service is responsible for in one sentence, the boundary is wrong.**

A responsibility belongs to one service. Everything else is either *delegated to* another service or *out of scope*. The "Does not own" rows are as important as the "Owns" rows — they're how you keep the boundaries from leaking under pressure.

#### Wallet Service

**One-line role:** the public-facing edge that validates requests, enforces idempotency, and orchestrates calls to the Ledger Service.

| Owns | Does not own |
|---|---|
| HTTP API surface (every public endpoint) | Ledger persistence |
| JWT authentication and authorization | Balance computation |
| Request schema validation | Event publication |
| Idempotency lifecycle (records, fingerprints, TTL) | Projection updates |
| Wallet lifecycle (create, freeze, unfreeze, close) | Money movement (delegated to Ledger) |
| Wallet audit log | The double-entry invariant (defended by Ledger) |
| Business rules that don't require ledger state (daily caps, self-transfer check, currency match) | |
| Generating deterministic `transactionId` from idempotency key | |
| Translating Ledger Service responses into HTTP responses | |

**Key behavior:** the Wallet Service is the *only* service clients talk to directly. It is also the *only* service that talks to the Ledger Service synchronously. This concentration is intentional — it gives one place to enforce auth, rate limits, and idempotency.

**Does not store balances.** Reads them from the projection store. If projections are stale, it surfaces that to the caller (AC-2.4).

#### Ledger Service

**One-line role:** the authoritative bookkeeper — accepts posting requests, enforces accounting invariants, persists immutable entries, and writes the outbox.

| Owns | Does not own |
|---|---|
| `ledger_transactions` and `ledger_entries` tables | HTTP-facing auth (trusts the Wallet Service) |
| Double-entry invariant enforcement (app code + DB trigger) | Idempotency at the HTTP-key level |
| Atomic posting (entries + outbox row in one DB transaction) | Balance projections |
| Available-balance check via account-lock row | Knowing *why* a transfer is happening |
| Outbox table writes | Event delivery to consumers (worker handles that) |
| Account lock rows (`SELECT ... FOR UPDATE` target) | |
| Deterministic rejection of unbalanced postings | |

**Key behavior:** the Ledger Service is **the only service allowed to write to the ledger tables.** This is enforced at the Postgres role level, not just by convention. Even an emergency manual fix must go through a compensating transaction posted via the API.

**Does not trust input.** Even though the Wallet Service has already validated, the Ledger Service re-validates the invariant. The same posting submitted directly via internal tooling must be rejected if unbalanced. Validation at the edge is for UX; validation at the ledger is for correctness.

#### Outbox Worker (lives inside the Ledger Service process)

**One-line role:** drains pending outbox rows to Kafka, exactly-once from the database's perspective, at-least-once from the consumer's.

| Owns | Does not own |
|---|---|
| Polling the outbox table (100 ms tick) | Outbox row creation (Ledger Service handlers do that) |
| Publishing to Kafka and marking rows published | Event consumption |
| Retention cleanup (delete published rows > 7 days) | |
| Handling Kafka outages (rows stay, retried on next tick) | |

**Why it's listed separately:** it has a different failure mode than the rest of the Ledger Service. The HTTP handlers can be healthy while the outbox worker is stuck on Kafka, and vice versa. Treating it as a distinct component makes the metrics, alerts, and operational story clearer.

#### Projection Service

**One-line role:** consumes ledger events and maintains the materialized balance view that powers fast reads.

| Owns | Does not own |
|---|---|
| `wallet_balances` and `projection_offsets` tables | Authoritative truth (ledger has that) |
| `LEDGER_POSTED` consumption | Posting decisions |
| Idempotent application of entries (via `last_entry_id`) | Reads served to clients (Wallet Service serves them) |
| Publishing `BALANCE_UPDATED` events | |
| Projection rebuild endpoint (admin-only) | |
| Tracking and exposing projection lag | |

**Key behavior:** the projection is **derivable, not authoritative.** If it gets corrupted, deleted, or drifts, the recovery procedure is to rebuild it from the ledger — not to manually fix the numbers. This is the property that makes the whole architecture safe to operate.

**Eventually consistent by design.** The Projection Service makes no promise about being up-to-date the instant a ledger entry commits. It only promises to converge.

#### Notification Service (optional in v1)

**One-line role:** consumes events and emits human-facing notifications. Build it only after everything else works.

| Owns | Does not own |
|---|---|
| Consuming `LEDGER_POSTED` and `BALANCE_UPDATED` | Any financial state |
| Rendering notification payloads | Delivery infrastructure (logs to stdout in v1) |
| Deduplication of notifications | |

**Why optional:** it has zero impact on financial correctness. Cutting it does not weaken the portfolio story — keeping it stretches scope. Include it only if Milestones 0–6 finish ahead of schedule.

#### Cross-service ownership map

When you're not sure where a piece of logic belongs, this table is the tiebreaker.

| Concern | Owner | Why |
|---|---|---|
| "Is the user authenticated?" | Wallet Service | Edge concern; ledger trusts its caller |
| "Is the request well-formed?" | Wallet Service | Same |
| "Has this idempotency key been seen?" | Wallet Service | Public API concern |
| "Does this posting balance to zero?" | Ledger Service | Accounting invariant |
| "Does the source wallet have enough?" | Ledger Service | Requires lock-protected read of ledger state |
| "What is wallet X's current balance?" | Projection Service (data) → Wallet Service (delivery) | Read model + auth |
| "Has this Kafka event already been applied?" | Projection Service | Local idempotency on `last_entry_id` |
| "Did the outbox publish reliably?" | Outbox Worker | Owns the publish lifecycle |
| "Is the ledger consistent with projections?" | Reconciliation job (Ledger Service) | The job that catches drift |

If a future requirement doesn't fit any row above, that's a signal a new responsibility — possibly a new service — is being added. Don't smuggle it into an existing service silently.

### 5.3 Why these technology choices

- **Java + Spring Boot for the Wallet Service:** the Wallet Service is mostly orchestration, validation, and HTTP. Spring's ecosystem (Spring Security for JWT, Spring Data JDBC, Bean Validation) accelerates this kind of work.
- **Go for the Ledger and Projection services:** these are simpler in shape but performance-sensitive and high-concurrency. Go's runtime and goroutine model fit the workload, and Go's strictness about errors maps well to financial logic where every error must be handled.
- **Postgres for everything:** transactional guarantees, mature, well-understood. No reason to introduce another store in v1.
- **Kafka for events:** durable, replayable, partition-ordered. The replay capability is specifically what we need for projection rebuilds (§7.5).

### 5.4 Database topology

Three logical databases, deployed as three schemas in one Postgres instance for the portfolio version:

- `wallet_db` — wallets, idempotency records, wallet audit log.
- `ledger_db` — ledger transactions, ledger entries, outbox.
- `projection_db` — wallet balances.

In a real deployment these would be separate Postgres clusters. The schema separation makes that migration straightforward.

This decision is recorded as ADR-004 in `docs/decisions/`, along with how each service applies its own migrations against the shared instance.

### 5.5 Wallet → Ledger transport (decision)

**HTTP/JSON over a local network.** Rejected gRPC for v1 because:

- The added complexity (proto compilation, dual-language tooling) doesn't pay for itself at this scale.
- HTTP/JSON is trivially debuggable with `curl` and the browser network tab.
- Spring's `RestClient` and Go's `net/http` are first-class.

This decision is recorded as ADR-001 in `docs/decisions/`.

---

## 6. Data model

Full DDL lives in each service's own migration directory — `services/wallet-service/src/main/resources/db/migration/`, `services/ledger-service/migrations/`, `services/projection-service/migrations/` — per ADR-004. The shapes are below; constraints and indexes are called out where they matter.

### 6.1 Wallet schema

```sql
CREATE TABLE wallets (
    id           UUID PRIMARY KEY,
    owner_id     UUID NOT NULL,
    currency     VARCHAR(3) NOT NULL CHECK (currency = 'BRL'),
    status       VARCHAR(16) NOT NULL CHECK (status IN ('ACTIVE','FROZEN','CLOSED')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_wallets_owner ON wallets(owner_id);

CREATE TABLE idempotency_records (
    key                VARCHAR(128) NOT NULL,
    user_id            UUID NOT NULL,
    request_fingerprint CHAR(64) NOT NULL,  -- sha256 of normalized request body
    response_status    INT NOT NULL,
    response_body      JSONB NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at         TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (user_id, key)
);
CREATE INDEX idx_idempotency_expires ON idempotency_records(expires_at);

CREATE TABLE wallet_audit_log (
    id          BIGSERIAL PRIMARY KEY,
    wallet_id   UUID NOT NULL,
    actor_id    UUID NOT NULL,
    action      VARCHAR(32) NOT NULL,
    before_state JSONB,
    after_state  JSONB,
    at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

### 6.2 Ledger schema

```sql
CREATE TABLE ledger_transactions (
    id                      UUID PRIMARY KEY,
    type                    VARCHAR(32) NOT NULL,  -- TRANSFER, DEPOSIT, WITHDRAWAL, REVERSAL
    reverses_transaction_id UUID REFERENCES ledger_transactions(id),
    description             TEXT,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE ledger_entries (
    id              UUID PRIMARY KEY,
    transaction_id  UUID NOT NULL REFERENCES ledger_transactions(id),
    account_id      UUID NOT NULL,
    entry_type      VARCHAR(8) NOT NULL CHECK (entry_type IN ('DEBIT','CREDIT')),
    amount          NUMERIC(19, 2) NOT NULL CHECK (amount > 0),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_entries_account_created ON ledger_entries(account_id, created_at);
CREATE INDEX idx_entries_transaction ON ledger_entries(transaction_id);

-- Enforce double-entry invariant per transaction
CREATE OR REPLACE FUNCTION check_transaction_balance() RETURNS TRIGGER AS $$
DECLARE
    net NUMERIC(19,2);
BEGIN
    SELECT COALESCE(SUM(CASE WHEN entry_type = 'CREDIT' THEN amount ELSE -amount END), 0)
      INTO net
      FROM ledger_entries
     WHERE transaction_id = NEW.id;
    IF net <> 0 THEN
        RAISE EXCEPTION 'ledger transaction % is unbalanced (net = %)', NEW.id, net;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Trigger fires at end of statement, after all entries inserted
CREATE CONSTRAINT TRIGGER trg_check_balance
AFTER INSERT ON ledger_transactions
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION check_transaction_balance();

CREATE TABLE outbox (
    id          BIGSERIAL PRIMARY KEY,
    topic       VARCHAR(64) NOT NULL,
    key         VARCHAR(128) NOT NULL,
    payload     JSONB NOT NULL,
    headers     JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ
);
CREATE INDEX idx_outbox_unpublished ON outbox(id) WHERE published_at IS NULL;
```

Application user has `INSERT, SELECT` on `ledger_entries` and `ledger_transactions`. **No `UPDATE`, no `DELETE`.** This is enforced via Postgres role grants.

### 6.3 Projection schema

```sql
CREATE TABLE wallet_balances (
    wallet_id        UUID PRIMARY KEY,
    balance          NUMERIC(19, 2) NOT NULL DEFAULT 0,
    last_entry_id    UUID,                          -- id of the last ledger_entry applied
    last_applied_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE projection_offsets (
    consumer_group   VARCHAR(64) PRIMARY KEY,
    topic            VARCHAR(64) NOT NULL,
    partition        INT NOT NULL,
    offset_committed BIGINT NOT NULL,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

`last_entry_id` enables idempotent projection updates: if the consumer sees an event whose entry it has already applied, it skips. This is how AC-2.4's freshness check works — the Wallet Service can join `wallet_balances.last_applied_at` against the latest `ledger_entries.created_at` for the wallet.

---

## 7. Internal contracts

### 7.1 Ledger Service API

`POST /ledger/postings`

```json
{
  "transactionId": "550e8400-e29b-41d4-a716-446655440000",
  "type": "TRANSFER",
  "description": "transfer from wallet-a to wallet-b",
  "entries": [
    {"accountId": "wallet-a", "entryType": "DEBIT",  "amount": "100.00"},
    {"accountId": "wallet-b", "entryType": "CREDIT", "amount": "100.00"}
  ]
}
```

Responses:

- `201` — posted. Body includes `postedAt` and the persisted entry IDs.
- `409` — `transactionId` already exists. Body includes the original posting for client comparison.
- `422` — invariant violation, insufficient balance, invalid account. Body includes `code` and `message`.
- `503` — DB unavailable.

The `transactionId` is generated by the **Wallet Service** and passed in. This makes the call idempotent at the Ledger layer for free: a retry with the same ID either hits the existing record (409) or — if the original never committed — creates it fresh.

### 7.2 Event schemas

All events are JSON, schema versioned via a `schemaVersion` field. Kafka key is the wallet ID for entries that pertain to a specific wallet, the transaction ID otherwise. Partitioning by wallet ID ensures all events for one wallet land on the same partition and are processed in order.

#### `LEDGER_POSTED`

Topic: `ledger.posted.v1`
Key: `transactionId`
Published by: Ledger Service (via outbox)
Consumed by: Projection Service, Notification Service

```json
{
  "schemaVersion": 1,
  "eventId": "uuid",
  "eventType": "LEDGER_POSTED",
  "occurredAt": "2026-05-19T14:23:00.123Z",
  "transactionId": "uuid",
  "transactionType": "TRANSFER",
  "entries": [
    {"entryId": "uuid", "accountId": "uuid", "entryType": "DEBIT",  "amount": "100.00"},
    {"entryId": "uuid", "accountId": "uuid", "entryType": "CREDIT", "amount": "100.00"}
  ],
  "traceparent": "00-<trace>-<span>-01"
}
```

#### `BALANCE_UPDATED`

Topic: `projection.balance.updated.v1`
Key: `walletId`
Published by: Projection Service
Consumed by: Notification Service

```json
{
  "schemaVersion": 1,
  "eventId": "uuid",
  "walletId": "uuid",
  "balance": "1100.00",
  "lastEntryId": "uuid",
  "occurredAt": "2026-05-19T14:23:00.456Z"
}
```

### 7.3 Required metrics

| Service | Metric | Type | Labels |
|---|---|---|---|
| Wallet | `wallet_requests_total` | counter | endpoint, method, status |
| Wallet | `wallet_request_duration_seconds` | histogram | endpoint, method |
| Wallet | `wallet_idempotency_hits_total` | counter | result (replay/mismatch/new) |
| Ledger | `ledger_postings_total` | counter | type, status |
| Ledger | `ledger_posting_duration_seconds` | histogram | type |
| Ledger | `ledger_outbox_depth` | gauge | — |
| Ledger | `ledger_outbox_publish_failures_total` | counter | reason |
| Projection | `projection_lag_seconds` | gauge | wallet_id (low cardinality? see note) |
| Projection | `projection_events_processed_total` | counter | result (applied/skipped/error) |
| Projection | `projection_consumer_lag` | gauge | topic, partition |

Note on cardinality: `projection_lag_seconds` per wallet would explode. Track it as a histogram across all wallets and a separate `max_projection_lag_seconds` gauge.

### 7.4 Outbox worker behavior

Polling loop in the Ledger Service:

1. Every 100 ms, `SELECT id, topic, key, payload, headers FROM outbox WHERE published_at IS NULL ORDER BY id LIMIT 100 FOR UPDATE SKIP LOCKED`.
2. For each row, publish to Kafka. On success, `UPDATE outbox SET published_at = now() WHERE id = ?`.
3. On failure, leave the row alone; the next iteration retries.
4. `SKIP LOCKED` allows multiple worker instances to run safely.
5. A background job deletes published rows older than 7 days.

Ordering: within a partition, Kafka preserves order. Since key is `transactionId` or `walletId`, ordering is preserved per wallet — which is what matters.

### 7.5 Reconciliation job

Runs hourly. Three checks:

1. **Invariant scan:** for every transaction in the last hour, verify `sum(signed entries) = 0`. (Defensive — the trigger should catch this, but a second pair of eyes is cheap.)
2. **Projection drift:** for every wallet, recompute balance from `ledger_entries` and compare to `wallet_balances`. If they differ, log a critical error and emit a `RECONCILIATION_DRIFT` metric.
3. **Money supply:** sum all `wallet_balances`. Should equal the negative of the external funding account's balance. (System holds zero net.)

This job's existence is the main artifact that proves to a reviewer that the project takes financial correctness seriously.

---

## 8. Transfer flow — annotated

The single most important flow. Numbered for cross-reference from §9.

```
[1] Client → Wallet Service: POST /transfers + Idempotency-Key
[2]   Wallet Service: validate JWT, schema, ownership, wallet status
[3]   Wallet Service: check idempotency_records
        - hit + matching fingerprint → return cached response  [STOP]
        - hit + different fingerprint → 422 IDEMPOTENCY_KEY_MISMATCH  [STOP]
        - in-progress (no response yet) → wait 5s, then 409 IN_PROGRESS  [STOP]
        - miss → INSERT record with status PENDING, continue
[4]   Wallet Service: generate transactionId (UUID v7)
[5]   Wallet Service → Ledger Service: POST /ledger/postings
[6]     Ledger Service: BEGIN
[7]     Ledger Service: SELECT ... FOR UPDATE on source wallet balance
                        (computed from entries — see note below)
[8]     Ledger Service: check available balance + daily cap
[9]     Ledger Service: INSERT ledger_transactions, INSERT ledger_entries
[10]    Ledger Service: INSERT outbox row (LEDGER_POSTED payload)
[11]    Ledger Service: COMMIT  (deferred trigger fires; invariant verified)
[12]  Ledger Service → Wallet Service: 201 with posted entries
[13] Wallet Service: UPDATE idempotency_records with response, status COMPLETED
[14] Wallet Service → Client: 201

Asynchronously:
[15] Ledger Outbox Worker → Kafka: publish LEDGER_POSTED
[16] Projection Service ← Kafka: consume LEDGER_POSTED
[17]   Projection Service: BEGIN
[18]   Projection Service: UPDATE wallet_balances for each affected wallet
                            (idempotent via last_entry_id check)
[19]   Projection Service: COMMIT, then commit Kafka offset
[20] Projection Service → Kafka: publish BALANCE_UPDATED
```

**Concurrency note for step 7:** "computed from entries" means the lock is on the account's row in a separate `account_balances_locked` table that the Ledger Service maintains alongside entries, also updated within the same transaction. This row exists purely as a lock target and as a cached current balance for fast availability checks. It is **not** authoritative — entries are. But it gives us the row to `SELECT ... FOR UPDATE` on, which is how we serialize concurrent transfers on the same wallet (resolving AC-5.17) without serializing the entire ledger.

If you prefer to lock differently — `SERIALIZABLE` isolation, or advisory locks keyed on wallet ID — that's a viable alternative. The decision goes in ADR-002.

---

## 9. Failure modes & how each is handled

Every distributed system has a finite list of ways it can fail. The portfolio value of this project is showing that you've enumerated them.

### 9.1 Wallet Service crashes between [3] and [4]

State: idempotency record exists as PENDING; no transactionId yet generated; client got no response.

Recovery: client retries with same idempotency key. Step [3] sees the PENDING record. Per AC-5.16, it waits up to 5 seconds and either picks up the result (if another instance completed it) or — since no instance is working on it — returns `409 IN_PROGRESS`. A janitor job sweeps PENDING records older than 60s and marks them FAILED, freeing the key.

### 9.2 Wallet Service crashes between [12] and [13]

State: ledger has committed; idempotency record still PENDING; client got no response.

Recovery: client retries. The Wallet Service now sees the PENDING idempotency record. It re-derives the transactionId (deterministic from the idempotency key — see below) and queries the Ledger Service with `GET /ledger/transactions/{id}`. Finds the committed transaction, updates the idempotency record, returns 201 to the client.

**Implementation requirement:** `transactionId` MUST be deterministic from `(user_id, idempotency_key)`, e.g., UUIDv5 with a fixed namespace. Otherwise this recovery path can't work.

### 9.3 Ledger Service crashes between [9] and [11]

State: Postgres transaction never committed. No entries, no outbox row, no nothing.

Recovery: nothing to do. Wallet Service times out on its HTTP call, gets back a 503 path (NFR-AVAIL-4), returns 503 to client. Client retries per Idempotency-Key.

### 9.4 Concurrent transfers from the same wallet (the canonical race)

Two requests, each for 60.00, from a wallet with 100.00. Both pass the Wallet Service. Both arrive at the Ledger Service.

Resolution: step [7]'s `SELECT ... FOR UPDATE` on the account's lock row serializes them. The first transaction proceeds; the second blocks until the first commits, then re-reads the now-updated balance (40.00), fails the availability check, returns 422 INSUFFICIENT_FUNDS.

Test for this is in §10 — TST-CONCURRENCY-1.

### 9.5 Kafka is down when outbox tries to publish

State: ledger row committed; outbox row written; publish fails.

Recovery: outbox worker leaves the row, retries on the next tick. Outbox depth metric climbs. An alert fires when `ledger_outbox_depth > 1000`. When Kafka returns, the worker drains.

Downstream effect: projections lag. The Wallet Service's `GET /balance` starts returning `"stale": true`. Clients can see the system is degraded.

### 9.6 Projection Service crashes mid-event

State: Kafka offset not committed; projection write either committed or not.

Recovery: on restart, the consumer re-reads from the last committed offset. The projection write is idempotent (uses `last_entry_id`), so re-applying the same event is a no-op. No double-counting.

### 9.7 Projection drift (the silent killer)

State: projection somehow ends up out of sync with the ledger (bug, manual DB intervention, anything).

Detection: reconciliation job (§7.5).

Recovery: replay. `POST /admin/projections/{walletId}/rebuild` clears the projection row, scans all `ledger_entries` for the wallet in order, and rebuilds. Endpoint is admin-only and emits a clear audit event.

### 9.8 Duplicate event delivery

State: Kafka delivers the same event twice (it's at-least-once, this is normal).

Recovery: `last_entry_id` check in the projection update. Second delivery is detected as already-applied and skipped. `projection_events_processed_total{result="skipped"}` increments.

### 9.9 Postgres goes down

Wallet Service: returns 503. No partial state.
Ledger Service: returns 503. Any in-flight transaction is rolled back by Postgres on reconnect.
Projection Service: consumer pauses, doesn't commit offsets. When DB returns, resumes.

### 9.10 Clock skew between services

A risk for any logic that compares timestamps across services. Mitigation: all timestamp-based logic uses Postgres `now()`, not service-local clocks. Latency measurements use trace spans, which carry their own time base.

### 9.11 Out-of-order events on a partition

Should not happen given key-based partitioning, but if it does (e.g., a producer retry after a partition rebalance): the projection's `last_entry_id` check protects against applying an older entry on top of a newer one — but we'd need a strict ordering field. For v1, accept the risk and document it.

---

## 10. Testing strategy

Tests are organized by what they prove, not where they live.

### 10.1 Unit tests (per service)

Cover pure logic with no I/O:

- Wallet Service: request validation, JWT parsing, idempotency fingerprint computation.
- Ledger Service: invariant calculation, balance derivation, sign logic.
- Projection Service: idempotent apply logic.

Target: ≥ 80% line coverage on domain packages. Coverage on infrastructure code (HTTP handlers, DB adapters) is not gated.

### 10.2 Integration tests

Use Testcontainers (Java) and `dockertest` (Go). Each test spins up real Postgres and real Kafka.

Mandatory scenarios:

| ID | Scenario | Asserts |
|---|---|---|
| TST-INT-1 | Wallet Service → Ledger Service successful transfer | Ledger rows exist; outbox row exists; HTTP 201 returned |
| TST-INT-2 | Outbox worker publishes pending events | After worker tick, outbox row has `published_at`, Kafka has message |
| TST-INT-3 | Projection consumer applies LEDGER_POSTED | wallet_balances updated; offset committed |
| TST-INT-4 | Projection consumer skips duplicate event | No double-apply; metric increments `skipped` |
| TST-INT-5 | Invariant trigger rejects unbalanced transaction | Direct DB insert of unbalanced entries fails |
| TST-INT-6 | Append-only enforcement | App user lacks UPDATE/DELETE on ledger_entries |

### 10.3 End-to-end tests

Run against the full `docker-compose` stack. Use real HTTP, real Kafka, real Postgres.

| ID | Scenario |
|---|---|
| TST-E2E-1 | Happy path transfer: source debited, destination credited, projections updated within 2s, event observed on Kafka |
| TST-E2E-2 | Idempotent retry: same key returns same response; ledger has exactly one transaction |
| TST-E2E-3 | Idempotency key mismatch: same key + different body → 422 |
| TST-E2E-4 | Insufficient funds: 422 with code, no ledger entries created |
| TST-E2E-5 | Frozen wallet rejection: 422 WALLET_NOT_ACTIVE |
| TST-E2E-6 | Projection rebuild: corrupt projection, rebuild, balance matches |
| TST-E2E-7 | Self-transfer rejected |
| TST-E2E-8 | Daily limit enforcement |

### 10.4 Concurrency tests

These are the highest-value tests in the whole suite. A reviewer who sees these passing knows the project is serious.

| ID | Scenario |
|---|---|
| TST-CONCURRENCY-1 | **Money cannot be created.** 100 concurrent transfers from a wallet with insufficient cumulative funds. Some succeed, some fail with INSUFFICIENT_FUNDS, sum of successes ≤ initial balance. Run 50 iterations. |
| TST-CONCURRENCY-2 | **Money cannot be destroyed.** 100 concurrent transfers between two wallets. Sum of (source_initial - source_final) MUST equal sum of (dest_final - dest_initial). |
| TST-CONCURRENCY-3 | **Disjoint pairs don't block.** 50 concurrent transfers across 50 distinct wallet pairs complete in time T. Then 50 concurrent transfers from the same source complete in time T' >> T. Asserts no global serialization. |
| TST-CONCURRENCY-4 | **Idempotency under concurrency.** Same idempotency key submitted 20 times in parallel. Exactly one ledger transaction created. All 20 responses identical. |

### 10.5 Chaos tests (stretch goal)

Run during a recorded demo, not in CI:

- Kill the Ledger Service mid-transfer; assert no orphan state.
- Kill Kafka; assert outbox queues; restart; assert drain.
- Kill the Projection Service; assert no event loss after restart.
- Add 200 ms network latency between Wallet and Ledger; assert SLO violations are observed in metrics.

### 10.6 Load tests

k6 scripts in `tests/load/`:

- `transfer_steady.js` — 50 RPS for 5 minutes. Asserts p95 latency, zero invariant violations.
- `transfer_spike.js` — ramp from 10 to 200 RPS over 30s. Asserts the system either holds SLOs or fails fast (no requests >5s).

---

## 11. Roadmap

The build order matters. Build vertical slices, not horizontal layers. Each milestone produces something demonstrable.

### Milestone 0 — Skeleton (3 days)

- Repo layout per the structure in §12.
- `docker-compose.yml` with Postgres + Kafka + Zookeeper (or KRaft) + Jaeger + Prometheus + Grafana.
- Empty Wallet Service and Ledger Service that expose `/health/live`.
- CI pipeline (GitHub Actions): lint + format + build for both services.
- README that explains what the project is and how to run the skeleton.

**Demonstrable:** `docker compose up`, hit `/health/live` on both services.

### Milestone 1 — Wallets exist (3 days)

- Wallet schema + migrations.
- `POST /wallets`, `GET /wallets/{id}`, JWT validation.
- Unit tests for validation; integration test for create + retrieve.

**Demonstrable:** create a wallet via curl, see it in Postgres.

### Milestone 2 — The ledger works (5 days)

- Ledger schema with invariant trigger and append-only grants.
- `POST /ledger/postings` in the Ledger Service.
- Integration test TST-INT-5 (invariant) and TST-INT-6 (append-only).
- Outbox table; outbox worker; integration test TST-INT-2.

**Demonstrable:** post a balanced ledger transaction directly; see entries, see outbox row, see Kafka message. Post an unbalanced one; see it rejected.

### Milestone 3 — Transfers end-to-end (5 days)

- Wallet Service `POST /transfers` calling Ledger Service.
- Idempotency table + logic.
- Projection Service consuming `LEDGER_POSTED`, writing `wallet_balances`.
- `GET /wallets/{id}/balance` reading projections, including the `stale` flag.
- E2E tests TST-E2E-1, TST-E2E-2, TST-E2E-3, TST-E2E-4.

**Demonstrable:** the README's "Quick Demo" works.

### Milestone 4 — Concurrency correctness (4 days)

- Account lock row + `SELECT ... FOR UPDATE`.
- Concurrency tests TST-CONCURRENCY-1 through TST-CONCURRENCY-4.
- Fix whatever those tests find. (They will find things.)

**Demonstrable:** the concurrency test suite passes 100 iterations green.

### Milestone 5 — Observability (3 days)

- Structured logging with `trace_id` propagation across HTTP and Kafka.
- Prometheus metrics per §7.3.
- Jaeger spans across services.
- Grafana dashboard `Transfers Overview`.

**Demonstrable:** run a transfer, open Jaeger, see the trace span from API ingress through projection write.

### Milestone 6 — Operational maturity (3 days)

- Deposits and withdrawals (FR-6, FR-7).
- Freeze/unfreeze/close (FR-3, FR-4).
- Wallet audit log.
- Reconciliation job.
- Projection rebuild endpoint.
- E2E test TST-E2E-6.

**Demonstrable:** intentionally corrupt a projection in Postgres, run the rebuild, watch the reconciliation alert fire then clear.

### Milestone 7 — Load + chaos + documentation (3 days)

- k6 load test scripts.
- Run them, capture results, paste into `docs/results/`.
- Recorded chaos demo (asciinema or screen capture).
- Architecture diagrams in `docs/architecture/`.
- ADRs in `docs/decisions/`.
- Final README polish.

**Demonstrable:** the project is ready to show to a hiring manager.

**Total: ~28 working days** for a single developer working with focus. Multiply by 2–3× for reality (interruptions, learning, debugging).

---

## 12. Repository layout

```
ledger-platform/
├── README.md                       # quick start, architecture diagram, demo
├── Makefile                        # make up / down / test / load / clean
├── docker-compose.yml
├── docker-compose.observability.yml
├── services/
│   ├── wallet-service/             # Java, Spring Boot
│   │   ├── src/main/java/...
│   │   ├── src/main/resources/db/migration/  # Flyway migrations (ADR-004)
│   │   ├── src/test/java/...
│   │   └── Dockerfile
│   ├── ledger-service/             # Go
│   │   ├── cmd/
│   │   ├── internal/
│   │   ├── migrations/             # golang-migrate migrations (ADR-004)
│   │   ├── tests/
│   │   └── Dockerfile
│   ├── projection-service/         # Go
│   │   ├── cmd/
│   │   ├── internal/
│   │   └── migrations/             # golang-migrate migrations (ADR-004)
│   └── notification-service/       # Go, optional
├── infrastructure/
│   ├── kafka/                      # topic creation scripts
│   ├── observability/
│   │   ├── prometheus.yml
│   │   ├── grafana/
│   │   └── otel-collector.yml
│   └── load/                       # k6 scripts
├── docs/
│   ├── architecture/
│   │   ├── overview.md
│   │   ├── data-flow.md
│   │   └── diagrams/
│   ├── decisions/                  # ADRs, numbered
│   │   ├── 0001-http-not-grpc.md
│   │   ├── 0002-locking-strategy.md
│   │   └── ...
│   ├── api/                        # OpenAPI specs
│   └── results/                    # load test outputs, screenshots
├── scripts/
│   ├── seed-users.sh
│   └── generate-jwt.sh
└── .github/workflows/
    ├── ci.yml
    └── release.yml
```

---

## 13. Design principles (the short version)

In priority order, when two principles conflict:

1. **Financial correctness beats everything.** No exceptions.
2. **Make failure visible, not silent.** A loud, ugly error beats a quiet wrong answer.
3. **Boundaries between services are contracts.** Validate at the edge; trust within.
4. **Append, don't mutate.** Especially for the ledger.
5. **Make it possible to replay history.** Anything derived can be rebuilt.
6. **Idempotency is not optional.** Anywhere a retry could happen, design for it.
7. **Observability is a feature, not a layer.** Build it in from day one.
8. **Optimize last.** Correctness first; measure; then improve what hurts.