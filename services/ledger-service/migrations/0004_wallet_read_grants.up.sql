DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'wallet_app') THEN
    CREATE ROLE wallet_app LOGIN PASSWORD 'wallet_app';
  END IF;
END
$$;

GRANT USAGE ON SCHEMA ledger_db TO wallet_app;

GRANT SELECT ON ledger_db.ledger_entries TO wallet_app;
