package com.ledger.wallet.application.usecase;

import com.ledger.wallet.domain.model.AuditAction;
import com.ledger.wallet.domain.model.Wallet;
import com.ledger.wallet.domain.model.WalletStatus;
import com.ledger.wallet.infrastructure.projection.ProjectionClient;
import com.ledger.wallet.infrastructure.projection.ProjectionUnavailableException;
import com.ledger.wallet.infrastructure.projection.dto.RebuildResult;
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
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

@ExtendWith(MockitoExtension.class)
class RebuildProjectionUseCaseTest {

    @Mock
    private ProjectionClient projectionClient;
    @Mock
    private WalletRepository walletRepository;
    @Mock
    private WalletAuditRepository walletAuditRepository;

    private RebuildProjectionUseCase useCase;
    private UUID walletId;
    private UUID ownerId;
    private UUID adminId;
    private AuthenticatedUser adminPrincipal;

    @BeforeEach
    void setUp() {
        useCase = new RebuildProjectionUseCase(projectionClient, walletRepository, walletAuditRepository);
        walletId = UUID.randomUUID();
        // Deliberately different from the admin: an admin rebuilds wallets they don't own
        // (owner-agnostic lookup), and the audit actor must be the admin, never the owner.
        ownerId = UUID.randomUUID();
        adminId = UUID.randomUUID();
        adminPrincipal = new AuthenticatedUser(adminId, null);
    }

    private Wallet wallet() {
        Instant now = Instant.now();
        return new Wallet(walletId, ownerId, "BRL", WalletStatus.ACTIVE, now, now);
    }

    @Test
    void execute_happyPath_returnsUpstreamResultAndWritesOneAuditRow() {
        when(walletRepository.getById(walletId)).thenReturn(Optional.of(wallet()));
        RebuildResult upstream = new RebuildResult(walletId, new BigDecimal("300.00"), 2, Instant.now());
        when(projectionClient.rebuild(walletId)).thenReturn(upstream);

        Optional<RebuildResult> result = useCase.execute(walletId, adminPrincipal);

        assertThat(result).contains(upstream);
        verify(walletAuditRepository).recordAction(walletId, adminId, AuditAction.PROJECTION_REBUILD);
    }

    // BR-A4: the actor is the admin who called this, never the wallet's owner.
    @Test
    void execute_happyPath_auditsTheAdminNotTheOwner() {
        when(walletRepository.getById(walletId)).thenReturn(Optional.of(wallet()));
        when(projectionClient.rebuild(walletId))
                .thenReturn(new RebuildResult(walletId, BigDecimal.ZERO, 0, Instant.now()));

        useCase.execute(walletId, adminPrincipal);

        verify(walletAuditRepository).recordAction(walletId, adminId, AuditAction.PROJECTION_REBUILD);
        verify(walletAuditRepository, never())
                .recordAction(walletId, ownerId, AuditAction.PROJECTION_REBUILD);
    }

    // The lookup is owner-agnostic: an admin can rebuild a wallet belonging to someone else.
    @Test
    void execute_walletOwnedBySomeoneElse_stillRebuilds() {
        when(walletRepository.getById(walletId)).thenReturn(Optional.of(wallet()));
        when(projectionClient.rebuild(walletId))
                .thenReturn(new RebuildResult(walletId, BigDecimal.ZERO, 0, Instant.now()));

        assertThat(useCase.execute(walletId, adminPrincipal)).isPresent();
        verify(walletRepository).getById(walletId); // the single-arg, owner-agnostic overload
    }

    @Test
    void execute_unknownWallet_returnsEmptyAndNeverCallsTheClient() {
        when(walletRepository.getById(walletId)).thenReturn(Optional.empty());

        Optional<RebuildResult> result = useCase.execute(walletId, adminPrincipal);

        assertThat(result).isEmpty();
        verifyNoInteractions(projectionClient, walletAuditRepository);
    }

    // The core bug this test guards: a downstream outage must surface as a 503
    // (ProjectionUnavailableException propagating to GlobalExceptionHandler), never collapse
    // into the same signal as "wallet not found".
    @Test
    void execute_projectionUnavailable_propagatesAndWritesNoAuditRow() {
        when(walletRepository.getById(walletId)).thenReturn(Optional.of(wallet()));
        when(projectionClient.rebuild(walletId))
                .thenThrow(new ProjectionUnavailableException("Error rebuilding wallet"));

        assertThatExceptionOfType(ProjectionUnavailableException.class)
                .isThrownBy(() -> useCase.execute(walletId, adminPrincipal));

        verifyNoInteractions(walletAuditRepository);
    }

    // A rebuild that succeeds upstream but whose audit insert fails is a 500 and a missing
    // audit row -- documented as acceptable (Task 05, Step 3) -- not a silently swallowed
    // success. The exception must propagate, not be caught and hidden.
    @Test
    void execute_auditInsertFails_propagates() {
        when(walletRepository.getById(walletId)).thenReturn(Optional.of(wallet()));
        when(projectionClient.rebuild(walletId))
                .thenReturn(new RebuildResult(walletId, BigDecimal.ZERO, 0, Instant.now()));
        RuntimeException auditFailure = new RuntimeException("audit insert failed");
        org.mockito.Mockito.doThrow(auditFailure)
                .when(walletAuditRepository).recordAction(any(), any(), any());

        assertThatExceptionOfType(RuntimeException.class)
                .isThrownBy(() -> useCase.execute(walletId, adminPrincipal))
                .isSameAs(auditFailure);
    }
}
