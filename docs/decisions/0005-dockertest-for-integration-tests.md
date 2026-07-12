# ADR-0005: Keep dockertest for integration tests

**Status:** Accepted
**Date:** 2026-07-11

## Context

`docs/milestones/milestone-2/07-integration-tests.md` states integration tests should use `testcontainers-go`, for consistency with wallet-service's Testcontainers-based Java tests, and asks for the divergence from `SPEC.md` §10.2's literal wording ("Use Testcontainers (Java) and `dockertest` (Go)") to be recorded as an ADR.

By the time that task was picked up, `services/ledger-service/tests/migrations_test.go` already existed and already used `dockertest` + `golang-migrate`, matching SPEC §10.2 as written. `go.mod` already depends on `dockertest`, not `testcontainers-go`. The milestone doc's stated intent and the code that was actually built diverged before this ADR was written, not after.

## Decision

Keep `dockertest`. All new ledger-service integration tests (`tests/posting_test.go`, `tests/posting_concurrency_test.go`, `tests/migrations_rollback_test.go`) use the same `dockertest`-based harness as `migrations_test.go`, extracted into a shared `setupLedgerDB` helper in `tests/testhelpers_test.go`.

## Alternatives considered

1. **Migrate to `testcontainers-go`** — would match the milestone doc's stated decision and wallet-service's tooling. Rejected: it means rewriting `migrations_test.go` (not just adding new files) and adding a new dependency, for a consistency benefit that's cosmetic — `dockertest` and `testcontainers-go` solve the identical problem (ephemeral Postgres containers for Go tests) with comparable APIs. The churn isn't justified by the benefit, and `dockertest` already matches `SPEC.md` §10.2's literal wording.

## Consequences

- ledger-service and projection-service (whose `tests/migrations_test.go` mirrors ledger-service's exactly) both standardize on `dockertest`; only wallet-service (Java) uses Testcontainers. This is a per-runtime split, not an inconsistency — each Go service uses the same Go tool, matching how `docs/decisions/0004-per-service-migration-ownership.md` already reasons about tooling (native to the runtime, not forced to match across languages).
- `docs/milestones/milestone-2/07-integration-tests.md`'s "Test framework (decision #3)" section is stale as of this ADR and should be updated to point here instead of asking for a new decision.
