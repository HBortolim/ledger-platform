package com.ledger.wallet.application.usecase;

import com.ledger.wallet.api.dto.GetWalletResponse;
import com.ledger.wallet.domain.exception.WalletAccessDeniedException;
import com.ledger.wallet.domain.exception.WalletConflictException;
import com.ledger.wallet.domain.exception.code.DomainErrorCode;
import com.ledger.wallet.domain.model.AuditAction;
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
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.doThrow;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

@ExtendWith(MockitoExtension.class)
class ChangeWalletStatusUseCaseTest {

    @Mock
    private WalletRepository walletRepository;
    @Mock
    private BalanceRepository balanceRepository;
    @Mock
    private WalletAuditRepository auditRepository;

    private ChangeWalletStatusUseCase useCase;
    private UUID actorId;
    private UUID ownerId;
    private UUID walletId;
    private AuthenticatedUser principal;

    @BeforeEach
    void setUp() {
        useCase = new ChangeWalletStatusUseCase(walletRepository, balanceRepository, auditRepository);
        actorId = UUID.randomUUID();
        // Deliberately different from the actor: for M6 these coincide on owner-scoped
        // endpoints, which is exactly why confusing them would otherwise go unnoticed (BR-A4).
        ownerId = UUID.randomUUID();
        walletId = UUID.randomUUID();
        principal = new AuthenticatedUser(actorId, null);
    }

    private Wallet wallet(WalletStatus status) {
        Instant now = Instant.parse("2026-01-01T00:00:00Z");
        return new Wallet(walletId, ownerId, "BRL", status, now, now);
    }

    private void givenWallet(WalletStatus status) {
        when(walletRepository.getById(walletId, actorId)).thenReturn(Optional.of(wallet(status)));
    }

    // --- freeze ---

    @Test
    void freeze_activeWallet_updatesStatusAndAuditsOnce() {
        givenWallet(WalletStatus.ACTIVE);

        GetWalletResponse response = useCase.freeze(walletId, principal);

        assertThat(response.status()).isEqualTo("FROZEN");
        verify(walletRepository).updateStatus(walletId, WalletStatus.FROZEN);
        verify(auditRepository).recordStatusChange(walletId, actorId, AuditAction.FREEZE,
                WalletStatus.ACTIVE, WalletStatus.FROZEN);
    }

    @Test
    void freeze_recordsTheActorNotTheOwner() {
        givenWallet(WalletStatus.ACTIVE);

        useCase.freeze(walletId, principal);

        verify(auditRepository).recordStatusChange(walletId, actorId, AuditAction.FREEZE,
                WalletStatus.ACTIVE, WalletStatus.FROZEN);
        verify(auditRepository, never()).recordStatusChange(any(), org.mockito.ArgumentMatchers.eq(ownerId),
                any(), any(), any());
    }

    @Test
    void freeze_alreadyFrozen_writesNothingAndReturns() {
        givenWallet(WalletStatus.FROZEN);

        GetWalletResponse response = useCase.freeze(walletId, principal);

        assertThat(response.status()).isEqualTo("FROZEN");
        verify(walletRepository, never()).updateStatus(any(), any());
        verifyNoInteractions(auditRepository);
    }

    @Test
    void freeze_closedWallet_throwsConflictAndWritesNothing() {
        givenWallet(WalletStatus.CLOSED);

        assertThatExceptionOfType(WalletConflictException.class)
                .isThrownBy(() -> useCase.freeze(walletId, principal))
                .satisfies(ex -> assertThat(ex.getCode()).isEqualTo(DomainErrorCode.WALLET_CLOSED));

        verify(walletRepository, never()).updateStatus(any(), any());
        verifyNoInteractions(auditRepository);
    }

    // ---- unfreeze ----

    @Test
    void unfreeze_frozenWallet_updatesStatusAndAudits() {
        givenWallet(WalletStatus.FROZEN);

        GetWalletResponse response = useCase.unfreeze(walletId, principal);

        assertThat(response.status()).isEqualTo("ACTIVE");
        verify(walletRepository).updateStatus(walletId, WalletStatus.ACTIVE);
        verify(auditRepository).recordStatusChange(walletId, actorId, AuditAction.UNFREEZE,
                WalletStatus.FROZEN, WalletStatus.ACTIVE);
    }

    @Test
    void unfreeze_alreadyActive_writesNothing() {
        givenWallet(WalletStatus.ACTIVE);

        assertThat(useCase.unfreeze(walletId, principal).status()).isEqualTo("ACTIVE");
        verify(walletRepository, never()).updateStatus(any(), any());
        verifyNoInteractions(auditRepository);
    }

    @Test
    void unfreeze_closedWallet_throwsConflict() {
        givenWallet(WalletStatus.CLOSED);

        assertThatExceptionOfType(WalletConflictException.class)
                .isThrownBy(() -> useCase.unfreeze(walletId, principal))
                .satisfies(ex -> assertThat(ex.getCode()).isEqualTo(DomainErrorCode.WALLET_CLOSED));
    }

    // ---- close ----

    @Test
    void close_zeroLedgerBalance_closesAndAudits() {
        givenWallet(WalletStatus.ACTIVE);
        when(balanceRepository.getLedgerBalance(walletId)).thenReturn(new BigDecimal("0.00"));

        GetWalletResponse response = useCase.close(walletId, principal);

        assertThat(response.status()).isEqualTo("CLOSED");
        verify(walletRepository).updateStatus(walletId, WalletStatus.CLOSED);
        verify(auditRepository).recordStatusChange(walletId, actorId, AuditAction.CLOSE,
                WalletStatus.ACTIVE, WalletStatus.CLOSED);
    }

    @Test
    void close_frozenWalletWithZeroBalance_closes() {
        givenWallet(WalletStatus.FROZEN);
        when(balanceRepository.getLedgerBalance(walletId)).thenReturn(BigDecimal.ZERO);

        assertThat(useCase.close(walletId, principal).status()).isEqualTo("CLOSED");
        verify(auditRepository).recordStatusChange(walletId, actorId, AuditAction.CLOSE,
                WalletStatus.FROZEN, WalletStatus.CLOSED);
    }

    @Test
    void close_nonZeroLedgerBalance_throwsNonzeroBalanceAndWritesNothing() {
        givenWallet(WalletStatus.ACTIVE);
        when(balanceRepository.getLedgerBalance(walletId)).thenReturn(new BigDecimal("0.01"));

        assertThatExceptionOfType(WalletConflictException.class)
                .isThrownBy(() -> useCase.close(walletId, principal))
                .satisfies(ex -> assertThat(ex.getCode()).isEqualTo(DomainErrorCode.NONZERO_BALANCE));

        verify(walletRepository, never()).updateStatus(any(), any());
        verifyNoInteractions(auditRepository);
    }

    @Test
    void close_negativeLedgerBalance_throwsNonzeroBalance() {
        givenWallet(WalletStatus.ACTIVE);
        when(balanceRepository.getLedgerBalance(walletId)).thenReturn(new BigDecimal("-5.00"));

        assertThatExceptionOfType(WalletConflictException.class)
                .isThrownBy(() -> useCase.close(walletId, principal))
                .satisfies(ex -> assertThat(ex.getCode()).isEqualTo(DomainErrorCode.NONZERO_BALANCE));
    }

    @Test
    void close_readsTheLedgerNeverTheProjection() {
        givenWallet(WalletStatus.ACTIVE);
        when(balanceRepository.getLedgerBalance(walletId)).thenReturn(BigDecimal.ZERO);

        useCase.close(walletId, principal);

        verify(balanceRepository).getLedgerBalance(walletId);
        verify(balanceRepository, never()).getByWalletId(any());
    }

    @Test
    void close_alreadyClosed_writesNothingAndSkipsBalanceRead() {
        givenWallet(WalletStatus.CLOSED);

        assertThat(useCase.close(walletId, principal).status()).isEqualTo("CLOSED");
        verifyNoInteractions(balanceRepository);
        verifyNoInteractions(auditRepository);
        verify(walletRepository, never()).updateStatus(any(), any());
    }

    // ---- ownership and failure propagation ----

    @Test
    void transition_notTheOwner_throwsAccessDeniedAndWritesNothing() {
        when(walletRepository.getById(walletId, actorId)).thenReturn(Optional.empty());

        assertThatExceptionOfType(WalletAccessDeniedException.class)
                .isThrownBy(() -> useCase.freeze(walletId, principal));

        verify(walletRepository, never()).updateStatus(any(), any());
        verifyNoInteractions(auditRepository, balanceRepository);
    }

    @Test
    void transition_unknownWallet_throwsAccessDenied() {
        when(walletRepository.getById(walletId, actorId)).thenReturn(Optional.empty());

        assertThatExceptionOfType(WalletAccessDeniedException.class)
                .isThrownBy(() -> useCase.close(walletId, principal));
    }

    @Test
    void freeze_auditFailure_propagates() {
        givenWallet(WalletStatus.ACTIVE);
        doThrow(new RuntimeException("audit insert failed"))
                .when(auditRepository).recordStatusChange(any(), any(), any(), any(), any());

        assertThatExceptionOfType(RuntimeException.class)
                .isThrownBy(() -> useCase.freeze(walletId, principal))
                .withMessage("audit insert failed");
    }
}
