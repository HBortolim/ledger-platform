DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'ledger_app') THEN
    CREATE ROLE ledger_app LOGIN PASSWORD 'ledger_app';
  END IF;
END
$$;

GRANT USAGE ON SCHEMA ledger_db TO ledger_app;

GRANT SELECT, INSERT                   ON TABLE ledger_db.ledger_transactions   TO ledger_app;
GRANT SELECT, INSERT                   ON TABLE ledger_db.ledger_entries         TO ledger_app;
GRANT SELECT, INSERT, UPDATE           ON TABLE ledger_db.account_balances_locked TO ledger_app;
GRANT SELECT, INSERT, UPDATE, DELETE   ON TABLE ledger_db.outbox                 TO ledger_app;

GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA ledger_db TO ledger_app;
