# API reference

| File | What it is |
|---|---|
| [`wallet-service.openapi.yaml`](wallet-service.openapi.yaml) | **The contract** for the Wallet Service's public API (OpenAPI 3.1). |
| [`testing-guide.md`](testing-guide.md) | How to mint a JWT and call the API from the command line. |
| [`../../postman/`](../../postman/) | A Postman collection and environments: a **convenience** for trying the API by hand. It is not the contract; where it and the OpenAPI file disagree, the OpenAPI file wins and the collection is the thing to fix. |

## What is and is not covered

Clients only ever talk to the Wallet Service (SPEC §5.2), so the OpenAPI file describes that one
service: wallets and their lifecycle, balances, transfers, admin deposits and withdrawals, the
admin projection rebuild, and the health probes.

The Ledger Service (`POST /ledger/postings`, `POST /admin/reconciliation/run`, …) and the Projection
Service (`POST /admin/projections/{walletId}/rebuild`) have **internal** routes. They are unauthenticated
by design and reachable only on the compose network, so they are not part of the public contract. They
are described in prose in [`docs/architecture/`](../architecture/).

## Things worth knowing before you read it

- **Four error shapes.** Domain errors (`{code, message}`), validation and ledger rejections
  (`{errors: [...]}`), empty bodies (ownership failures), and Spring Boot's default error object
  (authentication and malformed requests). The spec's top-level description lists which status codes use which.
- **Deposits and withdrawals answer `transactionId`; transfers answer `transferId`.**
- **A wallet that is not yours is indistinguishable from one that does not exist** on every owner-scoped
  endpoint, on purpose, so wallet ids cannot be probed.

## Checking the spec

```sh
# Lint (Redocly). Deliberate exceptions, each with a reason, are in .redocly.lint-ignore.yaml.
docker run --rm -v "$PWD/docs/api:/spec" redocly/cli:2.11.1 lint /spec/wallet-service.openapi.yaml
```

The embedded examples were also checked against their schemas with `kin-openapi`, which loads OpenAPI
3.1 documents.

**Drift check.** `make up && make test-e2e` validates every Wallet Service response the e2e suite receives
against this file (`tests/e2e/spec_drift_test.go`) and fails the run on any mismatch, even when the tests
pass. It covers the statuses the suite provokes (201s, and the 4xx bodies its tests trigger); statuses it never
triggers, such as the 503s, are checked by hand only. It needs the running stack, so it stays out of CI, which
only compiles the suite.
