# ADR-0008: System funding account may overdraw

**Status:** Accepted
**Date:** 2026-08-16

## Context

`docs/SPEC.md`'s NFR-CONS-5 defines the money-supply invariant: the sum of all wallet balances
plus the external funding account's balance equals zero at all times. This is only possible if
the external account's balance is allowed to go negative — every BRL credited into a wallet via
`POST /ledger/postings` (AC-6.1) is debited from this one account, and nothing ever credits it
back in v1 (withdrawals are M6 scope).

`checkAvailableBalance` in `PostingRepository.Post` rejected any debit exceeding the account's
cached balance, with no exception, for every account — including the system funding account,
seeded at a workaround balance of `1000` by `migrations/0002_seed_system_accounts.up.sql`. This
made the account's "exemption" an accident of the seed value being larger than whatever amount a
given demo or test happened to debit, not a real guarantee. Any deposit (or accumulation of
deposits) exceeding `1000` failed with `422 INSUFFICIENT_FUNDS`, exactly like it would for a
normal wallet — meaning nothing in Milestone 3 was demonstrable end-to-end without either a SQL
workaround or this fix, since every wallet starts at `0.00` and funding one requires debiting this
account.

## Decision

Exempt exactly one account — the ledger-service's configured `SystemAccountID` (env
`SYSTEM_ACCOUNT_ID`, default `00000000-0000-0000-0000-000000000001`) — from
`checkAvailableBalance`'s overdraft check. Everything else about the posting flow is unchanged:
the account is still locked `FOR UPDATE` in the same transaction, its cached balance in
`account_balances_locked` still tracks the (now negative) truth, and the double-entry invariant
trigger still enforces that every transaction balances to zero.

The match is on the configured UUID only — never inferred from "account has no
`account_balances_locked` row" or similar heuristics, since every account auto-provisions a
zero-balance lock row on first touch (`upsertAccountLocks`'s `ON CONFLICT DO NOTHING` upsert), and
a normal wallet's very first transfer would otherwise slip through the same heuristic.

## Alternatives considered

1. **Seed the account at a very large positive balance instead of a real exemption** — rejected:
   this is exactly the workaround already in place (seeded at `1000`) and it doesn't scale; any
   sustained demo, load test, or E2E suite rerunning against a warm stack eventually exhausts an
   arbitrarily large seed and starts failing for reasons unrelated to the scenario under test.
2. **Give the account a special `entry_type` or a dedicated posting endpoint that skips the check
   entirely** — rejected: `docs/SPEC.md` §2.2 explicitly scopes `/deposits`/`/withdrawals` as
   endpoints to Milestone 6. This ADR is deliberately the smallest possible M3 slice of FR-6 — the
   balance-check exemption only, reusing the existing `POST /ledger/postings` API exactly as
   wallet-to-wallet transfers do.
3. **Infer the exemption from account identity heuristics** (e.g. "any account never credited") —
   rejected: fragile, and every account (including brand-new wallets) starts in exactly that state
   before its first transfer; this would silently exempt the wrong accounts.

## Consequences

- `POST /ledger/postings` can now debit the system account arbitrarily far into negative
  territory, matching NFR-CONS-5's definition rather than approximating it with a large seed.
- The overview demo script's step 2 (seeding a wallet via a direct `DEBIT`/`CREDIT` posting)
  succeeds without any SQL workaround, unblocking the M3 demo and the new `tests/e2e` suite
  (Task 08).
- `migrations/0002_seed_system_accounts.up.sql`'s seed value of `1000` is now vestigial — harmless
  (the account can go negative from any starting point once exempt) but no longer load-bearing.
  It is left as-is rather than edited: this repo does not rewrite already-applied historical
  migrations (see ADR-0011's consequences section for the same convention applied to a different
  file).
- `services/ledger-service/tests/testhelpers_test.go`'s `seedAccountBalance` helper's doc comment,
  written before this ADR, calls the gap "a Milestone 6 concern" — that phrasing is now stale
  (the exemption is real and lands in M3), though the helper's actual behavior (seeding balances
  directly via SQL rather than through `PostingRepository.Post`) remains a valid, independent
  design choice for decoupling fixture setup from the code path under test.
