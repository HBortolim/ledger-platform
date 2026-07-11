-- System account for external funding (deposits/withdrawals)
-- UUID is fixed and known to all services via config.
INSERT INTO ledger_db.account_balances_locked (account_id, balance)
VALUES ('00000000-0000-0000-0000-000000000001', 0)
ON CONFLICT DO NOTHING;
