-- Per-wallet entry sequence (Task 08, SEQ-4..SEQ-8).
--
-- last_seq is the seq (the ledger's per-account entry number) of the newest entry applied
-- to the balance. The consumer applies an entry only when it is last_seq + 1.
ALTER TABLE projection_db.wallet_balances
    ADD COLUMN last_seq BIGINT NOT NULL DEFAULT 0;

-- SEQ-6: entries that arrived ahead of a missing predecessor. Parked durably, drained when
-- the gap fills. Expected to be near-empty, so it needs no pruning.
CREATE TABLE projection_db.pending_entries (
    wallet_id   UUID NOT NULL,
    seq         BIGINT NOT NULL,
    entry_id    UUID NOT NULL,
    delta       NUMERIC(19, 2) NOT NULL,   -- already signed (SignedDelta), so draining needs no entry_type
    parked_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (wallet_id, seq)
);

-- Align existing rows. last_entry_id is KEPT even though it is no longer the dedupe key: it
-- remains a useful "which entry was applied last" fact for debugging and for reconciliation.
-- This is exact only if the projection was caught up (consumer lag 0) when this ran; drain the
-- consumer before migrating, or rebuild every wallet afterwards.
UPDATE projection_db.wallet_balances wb
   SET last_seq = le.seq
  FROM ledger_db.ledger_entries le
 WHERE le.id = wb.last_entry_id;

GRANT SELECT, INSERT, DELETE ON TABLE projection_db.pending_entries TO projection_app;
