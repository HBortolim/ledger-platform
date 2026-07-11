-- ledger_db schema

CREATE SCHEMA IF NOT EXISTS ledger_db;

CREATE TABLE ledger_db.ledger_transactions (
    id                      UUID PRIMARY KEY,
    type                    VARCHAR(32) NOT NULL,
    reverses_transaction_id UUID REFERENCES ledger_db.ledger_transactions(id),
    description             TEXT,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE ledger_db.ledger_entries (
    id              UUID PRIMARY KEY,
    transaction_id  UUID NOT NULL REFERENCES ledger_db.ledger_transactions(id),
    account_id      UUID NOT NULL,
    entry_type      VARCHAR(8) NOT NULL CHECK (entry_type IN ('DEBIT','CREDIT')),
    amount          NUMERIC(19, 2) NOT NULL CHECK (amount > 0),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_entries_account_created ON ledger_db.ledger_entries(account_id, created_at);
CREATE INDEX idx_entries_transaction ON ledger_db.ledger_entries(transaction_id);

CREATE OR REPLACE FUNCTION ledger_db.check_transaction_balance() RETURNS TRIGGER AS $$
DECLARE
    net NUMERIC(19,2);
BEGIN
    SELECT COALESCE(SUM(CASE WHEN entry_type = 'CREDIT' THEN amount ELSE -amount END), 0)
      INTO net
      FROM ledger_db.ledger_entries
     WHERE transaction_id = NEW.id;
    IF net <> 0 THEN
        RAISE EXCEPTION 'ledger transaction % is unbalanced (net = %)', NEW.id, net;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE CONSTRAINT TRIGGER trg_check_balance
AFTER INSERT ON ledger_db.ledger_transactions
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION ledger_db.check_transaction_balance();

-- account_balances_locked: lock target + cached balance for SELECT FOR UPDATE
CREATE TABLE ledger_db.account_balances_locked (
    account_id  UUID PRIMARY KEY,
    balance     NUMERIC(19, 2) NOT NULL DEFAULT 0,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE ledger_db.outbox (
    id           BIGSERIAL PRIMARY KEY,
    topic        VARCHAR(64) NOT NULL,
    key          VARCHAR(128) NOT NULL,
    payload      JSONB NOT NULL,
    headers      JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ
);
CREATE INDEX idx_outbox_unpublished ON ledger_db.outbox(id) WHERE published_at IS NULL;
