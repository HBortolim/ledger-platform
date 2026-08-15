DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'wallet_app') THEN
    CREATE ROLE wallet_app LOGIN PASSWORD 'wallet_app';
  END IF;
END
$$;

GRANT USAGE ON SCHEMA projection_db TO wallet_app;

GRANT SELECT ON projection_db.wallet_balances TO wallet_app;
