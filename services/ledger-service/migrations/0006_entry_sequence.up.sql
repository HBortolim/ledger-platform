-- Per-account entry numbering (Task 08, SEQ-1/SEQ-2). Assigned under the account's
-- account_balances_locked row lock, so seq order is per-account commit order.
ALTER TABLE ledger_db.account_balances_locked
    ADD COLUMN entry_seq BIGINT NOT NULL DEFAULT 0;

ALTER TABLE ledger_db.ledger_entries
    ADD COLUMN seq BIGINT;

-- Backfill history. Runs as the owner role, which (unlike ledger_app) may UPDATE entries.
-- (created_at, id) is the best order available for rows written before this migration;
-- it is not guaranteed commit order for historic concurrent postings, which is acceptable:
-- the only requirement is a gap-free, unique numbering the projection can be aligned to.
UPDATE ledger_db.ledger_entries e
   SET seq = n.rn
  FROM (SELECT id, ROW_NUMBER() OVER (PARTITION BY account_id ORDER BY created_at, id) AS rn
          FROM ledger_db.ledger_entries) n
 WHERE e.id = n.id;

UPDATE ledger_db.account_balances_locked l
   SET entry_seq = m.max_seq
  FROM (SELECT account_id, MAX(seq) AS max_seq FROM ledger_db.ledger_entries GROUP BY account_id) m
 WHERE l.account_id = m.account_id;

-- The database-level guarantee of SEQ-1: a bug that double-assigns a number fails the
-- posting instead of corrupting history.
ALTER TABLE ledger_db.ledger_entries
    ALTER COLUMN seq SET NOT NULL,
    ADD CONSTRAINT uq_entries_account_seq UNIQUE (account_id, seq);
