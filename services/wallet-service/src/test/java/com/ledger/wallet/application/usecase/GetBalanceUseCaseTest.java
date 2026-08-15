package com.ledger.wallet.application.usecase;

import com.ledger.wallet.api.dto.BalanceResponse;
import com.ledger.wallet.domain.exception.WalletAccessDeniedException;
import com.ledger.wallet.domain.model.Balance;
import com.ledger.wallet.domain.model.Wallet;
import com.ledger.wallet.domain.model.WalletStatus;
import com.ledger.wallet.infrastructure.security.AuthenticatedUser;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

import java.math.BigDecimal;
import java.time.Instant;
import java.util.Optional;
import java.util.UUID;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatExceptionOfType;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

@ExtendWith(MockitoExtension.class)
class GetBalanceUseCaseTest {

    @Mock
    private WalletRepository walletRepository;
    @Mock
    private BalanceRepository balanceRepository;

    private GetBalanceUseCase useCase;
    private UUID userId;
    private UUID walletId;
    private AuthenticatedUser principal;
    private Instant walletCreatedAt;

    @BeforeEach
    void setUp() {
        useCase = new GetBalanceUseCase(walletRepository, balanceRepository);
        userId = UUID.randomUUID();
        walletId = UUID.randomUUID();
        principal = new AuthenticatedUser(userId, null);
        walletCreatedAt = Instant.parse("2026-01-01T00:00:00Z");
    }

    private void stubOwnedWallet() {
        Wallet wallet = new Wallet(walletId, userId, "BRL", WalletStatus.ACTIVE, walletCreatedAt, walletCreatedAt);
        when(walletRepository.getById(walletId, userId)).thenReturn(Optional.of(wallet));
    }

    // ---- Ownership (AC-2.2) ----

    @Test
    void walletMissing_throwsWalletAccessDeniedAndNeverQueriesBalance() {
        when(walletRepository.getById(walletId, userId)).thenReturn(Optional.empty());

        assertThatExceptionOfType(WalletAccessDeniedException.class)
                .isThrownBy(() -> useCase.execute(walletId, principal));

        verifyNoInteractions(balanceRepository);
    }

    // ---- Projection row exists (AC-2.1/2.3) ----

    @Test
    void projectionCaughtUp_returnsBalanceAndLastAppliedAtAsOf_notStale() {
        stubOwnedWallet();
        Instant lastAppliedAt = Instant.parse("2026-05-01T10:00:00Z");
        when(balanceRepository.getByWalletId(walletId)).thenReturn(new Balance(
                new BigDecimal("150.00"), lastAppliedAt, lastAppliedAt, lastAppliedAt));

        BalanceResponse response = useCase.execute(walletId, principal);

        assertThat(response.walletId()).isEqualTo(walletId);
        assertThat(response.balance()).isEqualByComparingTo("150.00");
        assertThat(response.currency()).isEqualTo("BRL");
        assertThat(response.asOf()).isEqualTo(lastAppliedAt);
        assertThat(response.stale()).isFalse();
    }

    @Test
    void projectionLaggingBeyondFiveSeconds_returnsStaleTrue() {
        stubOwnedWallet();
        Instant lastAppliedAt = Instant.parse("2026-05-01T10:00:00Z");
        Instant latestEntryAt = lastAppliedAt.plusSeconds(1);
        Instant dbNow = latestEntryAt.plusSeconds(8);
        when(balanceRepository.getByWalletId(walletId)).thenReturn(new Balance(
                new BigDecimal("150.00"), lastAppliedAt, latestEntryAt, dbNow));

        BalanceResponse response = useCase.execute(walletId, principal);

        assertThat(response.asOf()).isEqualTo(lastAppliedAt);
        assertThat(response.stale()).isTrue();
    }

    @Test
    void projectionLaggingWithinFiveSecondGracePeriod_returnsStaleFalse() {
        stubOwnedWallet();
        Instant lastAppliedAt = Instant.parse("2026-05-01T10:00:00Z");
        Instant latestEntryAt = lastAppliedAt.plusSeconds(1);
        Instant dbNow = latestEntryAt.plusSeconds(2);
        when(balanceRepository.getByWalletId(walletId)).thenReturn(new Balance(
                new BigDecimal("150.00"), lastAppliedAt, latestEntryAt, dbNow));

        BalanceResponse response = useCase.execute(walletId, principal);

        assertThat(response.stale()).isFalse();
    }

    // ---- No projection row yet ----

    @Test
    void freshWallet_neverHadEntries_returnsZeroBalanceAndWalletCreatedAtAsOf_notStale() {
        stubOwnedWallet();
        Instant dbNow = walletCreatedAt.plusSeconds(30);
        when(balanceRepository.getByWalletId(walletId)).thenReturn(new Balance(null, null, null, dbNow));

        BalanceResponse response = useCase.execute(walletId, principal);

        assertThat(response.balance()).isEqualByComparingTo("0.00");
        assertThat(response.asOf()).isEqualTo(walletCreatedAt);
        assertThat(response.stale()).isFalse();
    }

    @Test
    void consumerNeverCaughtUp_entriesExistButNoProjectionRow_returnsStaleTrue() {
        stubOwnedWallet();
        Instant latestEntryAt = walletCreatedAt.plusSeconds(10);
        Instant dbNow = latestEntryAt.plusSeconds(6);
        when(balanceRepository.getByWalletId(walletId)).thenReturn(new Balance(null, null, latestEntryAt, dbNow));

        BalanceResponse response = useCase.execute(walletId, principal);

        assertThat(response.balance()).isEqualByComparingTo("0.00");
        assertThat(response.asOf()).isEqualTo(walletCreatedAt);
        assertThat(response.stale()).isTrue();
    }

    @Test
    void consumerLaggingWithinGracePeriod_noProjectionRowYet_returnsStaleFalse() {
        stubOwnedWallet();
        Instant latestEntryAt = walletCreatedAt.plusSeconds(10);
        Instant dbNow = latestEntryAt.plusSeconds(2);
        when(balanceRepository.getByWalletId(walletId)).thenReturn(new Balance(null, null, latestEntryAt, dbNow));

        BalanceResponse response = useCase.execute(walletId, principal);

        assertThat(response.stale()).isFalse();
    }
}
