CREATE SCHEMA IF NOT EXISTS wallet_db;

CREATE TABLE wallet_db.wallets (
    id         UUID                     NOT NULL PRIMARY KEY,
    owner_id   UUID                     NOT NULL,
    currency   VARCHAR(3)               NOT NULL,
    status     VARCHAR(20)              NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL
);

CREATE INDEX idx_wallets_owner_id ON wallet_db.wallets (owner_id);
