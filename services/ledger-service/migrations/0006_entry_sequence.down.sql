ALTER TABLE ledger_db.ledger_entries
    DROP CONSTRAINT uq_entries_account_seq,
    DROP COLUMN seq;

ALTER TABLE ledger_db.account_balances_locked
    DROP COLUMN entry_seq;
