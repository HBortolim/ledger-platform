UPDATE ledger_db.account_balances_locked
   SET balance = 1000, updated_at = now()
 WHERE account_id = '00000000-0000-0000-0000-000000000001'
   AND NOT EXISTS (
     SELECT 1 FROM ledger_db.ledger_entries
      WHERE account_id = '00000000-0000-0000-0000-000000000001'
   );