package com.ledger.wallet.application.usecase;

import com.ledger.wallet.domain.model.Balance;

import java.util.UUID;

public interface BalanceRepository {

    Balance getByWalletId(UUID walletId);
}
