DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'wallet_app') THEN
    CREATE ROLE wallet_app LOGIN PASSWORD 'wallet_app';
  END IF;
END
$$;

GRANT USAGE ON SCHEMA wallet_db TO wallet_app;

GRANT SELECT, INSERT, UPDATE   ON TABLE wallet_db.wallets              TO wallet_app;
GRANT SELECT, INSERT, UPDATE   ON TABLE wallet_db.idempotency_records  TO wallet_app;
GRANT SELECT, INSERT           ON TABLE wallet_db.wallet_audit_log     TO wallet_app;

GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA wallet_db TO wallet_app;
