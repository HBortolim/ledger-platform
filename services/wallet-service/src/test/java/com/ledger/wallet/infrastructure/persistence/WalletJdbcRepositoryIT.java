package com.ledger.wallet.infrastructure.persistence;

import com.ledger.wallet.application.usecase.WalletRepository;
import com.ledger.wallet.domain.model.Wallet;
import com.ledger.wallet.support.BaseIntegrationTest;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;

import java.time.Instant;
import java.util.Optional;
import java.util.UUID;

import static org.assertj.core.api.Assertions.assertThat;

class WalletJdbcRepositoryIT extends BaseIntegrationTest {

    @Autowired
    private WalletRepository repository;

    @Test
    void getById_ownerAgnostic_findsWalletRegardlessOfOwner() {
        UUID ownerId = UUID.randomUUID();
        UUID someoneElse = UUID.randomUUID();
        Instant now = Instant.now();
        Wallet wallet = new Wallet(UUID.randomUUID(), ownerId, "BRL",
                com.ledger.wallet.domain.model.WalletStatus.ACTIVE, now, now);
        repository.save(wallet);

        // A transfer's destination lookup must find the wallet even though the caller
        // (someoneElse) doesn't own it -- only the source wallet is ownership-scoped (AC-5.11).
        Optional<Wallet> found = repository.getById(wallet.id());

        assertThat(found).isPresent();
        assertThat(found.get().id()).isEqualTo(wallet.id());
        assertThat(someoneElse).isNotEqualTo(ownerId);
    }

    @Test
    void getById_ownerAgnostic_missingWallet_returnsEmpty() {
        assertThat(repository.getById(UUID.randomUUID())).isEmpty();
    }
}
