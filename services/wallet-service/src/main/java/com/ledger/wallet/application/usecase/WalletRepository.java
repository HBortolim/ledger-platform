package com.ledger.wallet.application.usecase;

import com.ledger.wallet.domain.model.Wallet;

public interface WalletRepository {

    void save(Wallet wallet);
}
