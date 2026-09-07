package com.ledger.wallet.application.usecase;

import com.ledger.wallet.domain.model.Wallet;
import com.ledger.wallet.domain.model.WalletStatus;

import java.util.Optional;
import java.util.UUID;

public interface WalletRepository {

    void save(Wallet wallet);

    Optional<Wallet> getById(UUID walletId, UUID ownerId);

    Optional<Wallet> getById(UUID walletId);

    int updateStatus(UUID walletId, WalletStatus status);

}
