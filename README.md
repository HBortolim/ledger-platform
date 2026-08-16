# ledger-platform

## Testing

- `make test` — unit + integration test suites for all three services (Testcontainers/dockertest manage their own ephemeral Postgres/Kafka; no `docker compose` stack required).
- `make up && make test-e2e` — end-to-end suite (`tests/e2e/`) against the full running stack. Requires `make up` first; never run by `make test` or CI.
- If `make test-e2e` fails with 401/403 errors, `keys/private.pem` and `keys/public.pem` are probably an out-of-sync pair. Regenerate both together with `rm -f keys/private.pem keys/public.pem && ./scripts/generate-jwt.sh 00000000-0000-0000-0000-000000000001`, then `docker compose restart wallet-service` if the stack was already running (so it picks up the new public key).