package com.ledger.wallet.application.usecase;

import com.ledger.wallet.domain.model.Wallet;

import java.util.Optional;
import java.util.UUID;

public interface WalletRepository {

    void save(Wallet wallet);

    Optional<Wallet> getById(UUID walletId, UUID ownerId);
}
