-- projection_db schema

CREATE SCHEMA IF NOT EXISTS projection_db;

CREATE TABLE projection_db.wallet_balances (
    wallet_id       UUID PRIMARY KEY,
    balance         NUMERIC(19, 2) NOT NULL DEFAULT 0,
    last_entry_id   UUID,
    last_applied_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE projection_db.projection_offsets (
    consumer_group   VARCHAR(64) PRIMARY KEY,
    topic            VARCHAR(64) NOT NULL,
    partition        INT NOT NULL,
    offset_committed BIGINT NOT NULL,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
