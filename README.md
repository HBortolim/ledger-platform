# ledger-platform

## Testing

- `make test` — unit + integration test suites for all three services (Testcontainers/dockertest manage their own ephemeral Postgres/Kafka; no `docker compose` stack required).
- `make up && make test-e2e` — end-to-end suite (`tests/e2e/`) against the full running stack. Requires `make up` first; never run by `make test` or CI.