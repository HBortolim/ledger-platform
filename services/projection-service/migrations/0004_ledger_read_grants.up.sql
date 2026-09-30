DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'projection_app') THEN
    CREATE ROLE projection_app LOGIN PASSWORD 'projection_app';
  END IF;
END
$$;

GRANT USAGE ON SCHEMA ledger_db TO projection_app;

GRANT SELECT ON ledger_db.ledger_entries TO projection_app;
