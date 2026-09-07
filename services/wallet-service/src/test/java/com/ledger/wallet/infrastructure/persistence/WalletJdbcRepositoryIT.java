package com.ledger.wallet.infrastructure.persistence;

import com.ledger.wallet.application.usecase.WalletRepository;
import com.ledger.wallet.domain.model.Wallet;
import com.ledger.wallet.domain.model.WalletStatus;
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

    @Test
    void updateStatus_changeStatus_returnsUpdatedStatus() {
        UUID walletId = UUID.randomUUID();
        UUID ownerId = UUID.randomUUID();
        String currency = "BRL";
        Instant now = Instant.now();
        Wallet wallet = new Wallet(walletId, ownerId, currency, WalletStatus.ACTIVE, now, now);

        repository.save(wallet);

        WalletStatus newStatus = WalletStatus.FROZEN;
        int updated = repository.updateStatus(walletId, newStatus);

        Wallet updatedWallet = repository.getById(walletId).orElse(null);

        assertThat(updated).isEqualTo(1);
        assertThat(updatedWallet).isNotNull();
        assertThat(updatedWallet.status()).isNotEqualTo(wallet.status());
        assertThat(updatedWallet.status()).isEqualTo(newStatus);
    }

    // The int return exists so a caller can tell "wallet vanished mid-request" from "updated".
    @Test
    void updateStatus_unknownWallet_returnsZeroRowsAffected() {
        assertThat(repository.updateStatus(UUID.randomUUID(), WalletStatus.FROZEN)).isZero();
    }

    // SPEC 9.10: updated_at comes from Postgres now(), not the JVM. Seeded deliberately in the
    // past so a bump is unambiguous -- and so a repository that forgot the column would fail.
    @Test
    void updateStatus_bumpsUpdatedAtFromDatabaseClock() {
        UUID walletId = UUID.randomUUID();
        Instant longAgo = Instant.parse("2020-01-01T00:00:00Z");
        repository.save(new Wallet(walletId, UUID.randomUUID(), "BRL", WalletStatus.ACTIVE, longAgo, longAgo));

        repository.updateStatus(walletId, WalletStatus.FROZEN);

        Wallet reloaded = repository.getById(walletId).orElseThrow();
        assertThat(reloaded.updatedAt()).isAfter(longAgo);
        assertThat(reloaded.createdAt()).isEqualTo(longAgo);
    }

    // Status is the only thing that moves.
    @Test
    void updateStatus_leavesOwnerAndCurrencyUntouched() {
        UUID walletId = UUID.randomUUID();
        UUID ownerId = UUID.randomUUID();
        Instant now = Instant.now();
        repository.save(new Wallet(walletId, ownerId, "BRL", WalletStatus.ACTIVE, now, now));

        repository.updateStatus(walletId, WalletStatus.CLOSED);

        Wallet reloaded = repository.getById(walletId).orElseThrow();
        assertThat(reloaded.ownerId()).isEqualTo(ownerId);
        assertThat(reloaded.currency()).isEqualTo("BRL");
        assertThat(reloaded.status()).isEqualTo(WalletStatus.CLOSED);
    }
}
