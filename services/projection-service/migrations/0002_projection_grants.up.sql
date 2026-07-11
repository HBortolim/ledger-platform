DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'projection_app') THEN
    CREATE ROLE projection_app LOGIN PASSWORD 'projection_app';
  END IF;
END
$$;

GRANT USAGE ON SCHEMA projection_db TO projection_app;

GRANT SELECT, INSERT, UPDATE, DELETE   ON TABLE projection_db.wallet_balances     TO projection_app;
GRANT SELECT, INSERT, UPDATE           ON TABLE projection_db.projection_offsets  TO projection_app;

GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA projection_db TO projection_app;
