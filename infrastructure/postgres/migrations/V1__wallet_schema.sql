-- wallet_db schema

CREATE SCHEMA IF NOT EXISTS wallet_db;

CREATE TABLE wallet_db.wallets (
    id          UUID PRIMARY KEY,
    owner_id    UUID NOT NULL,
    currency    VARCHAR(3) NOT NULL CHECK (currency = 'BRL'),
    status      VARCHAR(16) NOT NULL CHECK (status IN ('ACTIVE','FROZEN','CLOSED')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_wallets_owner ON wallet_db.wallets(owner_id);

CREATE TABLE wallet_db.idempotency_records (
    key                  VARCHAR(128) NOT NULL,
    user_id              UUID NOT NULL,
    request_fingerprint  CHAR(64) NOT NULL,
    response_status      INT NOT NULL,
    response_body        JSONB NOT NULL,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at           TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (user_id, key)
);
CREATE INDEX idx_idempotency_expires ON wallet_db.idempotency_records(expires_at);

CREATE TABLE wallet_db.wallet_audit_log (
    id           BIGSERIAL PRIMARY KEY,
    wallet_id    UUID NOT NULL,
    actor_id     UUID NOT NULL,
    action       VARCHAR(32) NOT NULL,
    before_state JSONB,
    after_state  JSONB,
    at           TIMESTAMPTZ NOT NULL DEFAULT now()
);
