#!/bin/sh
# Seeds test users directly into the wallet_db.
# Usage: ./scripts/seed-users.sh

PGHOST="${PGHOST:-localhost}"
PGPORT="${PGPORT:-5432}"
PGUSER="${PGUSER:-ledger}"
PGPASSWORD="${PGPASSWORD:-ledger}"
PGDATABASE="${PGDATABASE:-ledger}"

export PGPASSWORD

psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE" <<'SQL'
-- test users (no PII, just UUIDs)
INSERT INTO wallet_db.wallets (id, owner_id, currency, status)
VALUES
  ('aaaaaaaa-0000-0000-0000-000000000001', '10000000-0000-0000-0000-000000000001', 'BRL', 'ACTIVE'),
  ('aaaaaaaa-0000-0000-0000-000000000002', '10000000-0000-0000-0000-000000000002', 'BRL', 'ACTIVE')
ON CONFLICT DO NOTHING;
SQL

echo "Seed complete."
