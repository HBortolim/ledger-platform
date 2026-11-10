-- The 1000 float predates ADR-0008: it existed so early deposits wouldn't overdraw
-- an account that could not yet go negative. The overdraft exemption made it
-- unnecessary, and it makes NFR-CONS-5's "system holds zero net" literally false.
-- Forward migration rather than an edit to 0002, so already-migrated environments converge.
UPDATE ledger_db.account_balances_locked
   SET balance = 0, updated_at = now()
 WHERE account_id = '00000000-0000-0000-0000-000000000001'
   AND NOT EXISTS (
     SELECT 1 FROM ledger_db.ledger_entries
      WHERE account_id = '00000000-0000-0000-0000-000000000001'
   );
