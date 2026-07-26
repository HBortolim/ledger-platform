ALTER TABLE wallet_db.idempotency_records
    ADD COLUMN status VARCHAR(16) NOT NULL DEFAULT 'COMPLETED'
        CHECK (status IN ('PENDING','COMPLETED','FAILED'));

ALTER TABLE wallet_db.idempotency_records
    ALTER COLUMN response_status DROP NOT NULL;

ALTER TABLE wallet_db.idempotency_records
    ALTER COLUMN response_body DROP NOT NULL;

-- jsonb does not preserve input formatting on read-back (Postgres re-serializes,
-- e.g. inserting whitespace after ':'), which silently breaks AC-5.12's "byte-for-byte"
-- replay requirement. response_body only needs to round-trip exactly, not be queryable
-- as JSON, so it's stored as opaque text instead.
ALTER TABLE wallet_db.idempotency_records
    ALTER COLUMN response_body TYPE TEXT USING response_body::text;

-- The janitor (Task 04) sweeps stale PENDING rows to FAILED and deletes rows
-- past expires_at; V2 only granted SELECT/INSERT/UPDATE.
GRANT DELETE ON TABLE wallet_db.idempotency_records TO wallet_app;
