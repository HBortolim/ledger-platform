DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'ledger_app') THEN
    CREATE ROLE ledger_app LOGIN PASSWORD 'ledger_app';
  END IF;
END
$$;

GRANT USAGE ON SCHEMA projection_db TO ledger_app;

GRANT SELECT ON projection_db.wallet_balances TO ledger_app;
