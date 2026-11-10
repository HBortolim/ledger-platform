REVOKE ALL PRIVILEGES ON TABLE projection_db.pending_entries FROM projection_app;
DROP TABLE projection_db.pending_entries;
ALTER TABLE projection_db.wallet_balances DROP COLUMN last_seq;
