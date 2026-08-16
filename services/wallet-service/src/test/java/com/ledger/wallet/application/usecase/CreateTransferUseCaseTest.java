package com.ledger.wallet.application.usecase;

import com.ledger.wallet.api.dto.CreateTransferRequest;
import com.ledger.wallet.application.idempotency.IdempotencyResult;
import com.ledger.wallet.domain.exception.DomainValidationException;
import com.ledger.wallet.domain.exception.TransferInProgressException;
import com.ledger.wallet.domain.exception.WalletAccessDeniedException;
import com.ledger.wallet.domain.exception.code.DomainErrorCode;
import com.ledger.wallet.domain.model.Wallet;
import com.ledger.wallet.domain.model.WalletStatus;
import com.ledger.wallet.infrastructure.ledger.LedgerClient;
import com.ledger.wallet.infrastructure.ledger.LedgerUnavailableException;
import com.ledger.wallet.infrastructure.ledger.PostPostingResult;
import com.ledger.wallet.infrastructure.ledger.dto.LedgerPosting;
import com.ledger.wallet.infrastructure.security.AuthenticatedUser;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

import java.math.BigDecimal;
import java.time.Instant;
import java.util.List;
import java.util.Optional;
import java.util.UUID;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatExceptionOfType;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyInt;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

@ExtendWith(MockitoExtension.class)
class CreateTransferUseCaseTest {

    @Mock
    private WalletRepository walletRepository;
    @Mock
    private com.ledger.wallet.application.idempotency.IdempotencyService idempotencyService;
    @Mock
    private LedgerClient ledgerClient;

    private CreateTransferUseCase useCase;
    private UUID userId;
    private UUID sourceId;
    private UUID destinationId;
    private AuthenticatedUser principal;

    @BeforeEach
    void setUp() {
        useCase = new CreateTransferUseCase(walletRepository, idempotencyService, ledgerClient);
        userId = UUID.randomUUID();
        sourceId = UUID.randomUUID();
        destinationId = UUID.randomUUID();
        principal = new AuthenticatedUser(userId, null);
    }

    private Wallet activeWallet(UUID id, UUID ownerId, String currency) {
        Instant now = Instant.now();
        return new Wallet(id, ownerId, currency, WalletStatus.ACTIVE, now, now);
    }

    private Wallet walletWithStatus(UUID id, UUID ownerId, WalletStatus status) {
        Instant now = Instant.now();
        return new Wallet(id, ownerId, "BRL", status, now, now);
    }

    private CreateTransferRequest request(UUID source, UUID destination, String amount) {
        return new CreateTransferRequest(source, destination, new BigDecimal(amount), "test");
    }

    // ---- Validation chain ----

    @Test
    void selfTransfer_throwsDomainValidationException() {
        CreateTransferRequest req = request(sourceId, sourceId, "10.00");

        assertThatExceptionOfType(DomainValidationException.class)
                .isThrownBy(() -> useCase.execute(req, "key", principal))
                .satisfies(ex -> assertThat(ex.getErrors().get(0).code()).isEqualTo(DomainErrorCode.SELF_TRANSFER));

        verifyNoInteractions(walletRepository, idempotencyService, ledgerClient);
    }

    @Test
    void sourceWalletMissing_throwsWalletAccessDenied() {
        when(walletRepository.getById(sourceId, userId)).thenReturn(Optional.empty());

        assertThatExceptionOfType(WalletAccessDeniedException.class)
                .isThrownBy(() -> useCase.execute(request(sourceId, destinationId, "10.00"), "key", principal));
    }

    @Test
    void destinationWalletMissing_throwsWalletNotActive() {
        when(walletRepository.getById(sourceId, userId)).thenReturn(Optional.of(activeWallet(sourceId, userId, "BRL")));
        when(walletRepository.getById(destinationId)).thenReturn(Optional.empty());

        assertThatExceptionOfType(DomainValidationException.class)
                .isThrownBy(() -> useCase.execute(request(sourceId, destinationId, "10.00"), "key", principal))
                .satisfies(ex -> assertThat(ex.getErrors().get(0).code()).isEqualTo(DomainErrorCode.WALLET_NOT_ACTIVE));
    }

    @Test
    void sourceWalletFrozen_throwsWalletNotActive() {
        when(walletRepository.getById(sourceId, userId))
                .thenReturn(Optional.of(walletWithStatus(sourceId, userId, WalletStatus.FROZEN)));

        assertThatExceptionOfType(DomainValidationException.class)
                .isThrownBy(() -> useCase.execute(request(sourceId, destinationId, "10.00"), "key", principal))
                .satisfies(ex -> assertThat(ex.getErrors().get(0).code()).isEqualTo(DomainErrorCode.WALLET_NOT_ACTIVE));
    }

    @Test
    void destinationWalletClosed_throwsWalletNotActive() {
        when(walletRepository.getById(sourceId, userId)).thenReturn(Optional.of(activeWallet(sourceId, userId, "BRL")));
        when(walletRepository.getById(destinationId))
                .thenReturn(Optional.of(walletWithStatus(destinationId, UUID.randomUUID(), WalletStatus.CLOSED)));

        assertThatExceptionOfType(DomainValidationException.class)
                .isThrownBy(() -> useCase.execute(request(sourceId, destinationId, "10.00"), "key", principal))
                .satisfies(ex -> assertThat(ex.getErrors().get(0).code()).isEqualTo(DomainErrorCode.WALLET_NOT_ACTIVE));
    }

    @Test
    void currencyMismatch_throwsCurrencyMismatch() {
        when(walletRepository.getById(sourceId, userId)).thenReturn(Optional.of(activeWallet(sourceId, userId, "BRL")));
        when(walletRepository.getById(destinationId))
                .thenReturn(Optional.of(activeWallet(destinationId, UUID.randomUUID(), "USD")));

        assertThatExceptionOfType(DomainValidationException.class)
                .isThrownBy(() -> useCase.execute(request(sourceId, destinationId, "10.00"), "key", principal))
                .satisfies(ex -> assertThat(ex.getErrors().get(0).code()).isEqualTo(DomainErrorCode.CURRENCY_MISMATCH));
    }

    // ---- Orchestration ----

    private void stubValidWallets() {
        when(walletRepository.getById(sourceId, userId)).thenReturn(Optional.of(activeWallet(sourceId, userId, "BRL")));
        when(walletRepository.getById(destinationId)).thenReturn(Optional.of(activeWallet(destinationId, UUID.randomUUID(), "BRL")));
    }

    @Test
    void keylessRequest_postsDirectlyAndNeverTouchesIdempotency() {
        stubValidWallets();
        Instant postedAt = Instant.now();
        when(ledgerClient.postPosting(any(), anyString(), any(), any()))
                .thenReturn(new PostPostingResult.Posted(postedAt, List.of()));

        TransferOutcome outcome = useCase.execute(request(sourceId, destinationId, "10.00"), null, principal);

        assertThat(outcome.httpStatus()).isEqualTo(201);
        assertThat(outcome.body()).contains("COMPLETED");
        verifyNoInteractions(idempotencyService);
    }

    @Test
    void keylessRequest_ledgerUnavailable_propagatesWithoutTouchingIdempotency() {
        stubValidWallets();
        when(ledgerClient.postPosting(any(), anyString(), any(), any()))
                .thenThrow(new LedgerUnavailableException("down"));

        assertThatExceptionOfType(LedgerUnavailableException.class)
                .isThrownBy(() -> useCase.execute(request(sourceId, destinationId, "10.00"), null, principal));

        verifyNoInteractions(idempotencyService);
    }

    @Test
    void newIdempotencyKey_postedSuccessfully_completesAndReturns201() {
        stubValidWallets();
        UUID transactionId = UUID.randomUUID();
        Instant postedAt = Instant.now();
        when(idempotencyService.begin(eq(userId), eq("key"), anyString())).thenReturn(new IdempotencyResult.New(transactionId));
        when(ledgerClient.postPosting(any(), anyString(), any(), any()))
                .thenReturn(new PostPostingResult.Posted(postedAt, List.of()));

        TransferOutcome outcome = useCase.execute(request(sourceId, destinationId, "10.00"), "key", principal);

        assertThat(outcome.httpStatus()).isEqualTo(201);
        assertThat(outcome.body()).contains(transactionId.toString()).contains("COMPLETED");
        verify(idempotencyService).complete(userId, "key", 201, outcome.body());
    }

    @Test
    void replay_returnsCachedResponseWithoutCallingLedger() {
        when(idempotencyService.begin(eq(userId), eq("key"), anyString()))
                .thenReturn(new IdempotencyResult.Replay(201, "{\"cached\":true}"));

        // No wallet stubbing needed either -- wait, validation runs first. Stub wallets valid.
        stubValidWallets();

        TransferOutcome outcome = useCase.execute(request(sourceId, destinationId, "10.00"), "key", principal);

        assertThat(outcome.httpStatus()).isEqualTo(201);
        assertThat(outcome.body()).isEqualTo("{\"cached\":true}");
        verifyNoInteractions(ledgerClient);
    }

    @Test
    void mismatch_throwsIdempotencyKeyMismatch() {
        stubValidWallets();
        when(idempotencyService.begin(eq(userId), eq("key"), anyString())).thenReturn(new IdempotencyResult.Mismatch());

        assertThatExceptionOfType(DomainValidationException.class)
                .isThrownBy(() -> useCase.execute(request(sourceId, destinationId, "10.00"), "key", principal))
                .satisfies(ex -> assertThat(ex.getErrors().get(0).code()).isEqualTo(DomainErrorCode.IDEMPOTENCY_KEY_MISMATCH));

        verifyNoInteractions(ledgerClient);
    }

    @Test
    void inProgress_recoveryFindsCommittedTransaction_completesAndReturns201() {
        stubValidWallets();
        when(idempotencyService.begin(eq(userId), eq("key"), anyString())).thenReturn(new IdempotencyResult.InProgress());
        UUID transactionId = com.ledger.wallet.application.idempotency.IdempotencyKeys.transactionId(userId, "key");
        Instant postedAt = Instant.now();
        when(ledgerClient.getTransaction(transactionId))
                .thenReturn(Optional.of(new LedgerPosting(transactionId, "TRANSFER", "test", postedAt, List.of())));

        TransferOutcome outcome = useCase.execute(request(sourceId, destinationId, "10.00"), "key", principal);

        assertThat(outcome.httpStatus()).isEqualTo(201);
        verify(idempotencyService).complete(userId, "key", 201, outcome.body());
    }

    @Test
    void inProgress_recoveryFindsNothing_throwsTransferInProgress() {
        stubValidWallets();
        when(idempotencyService.begin(eq(userId), eq("key"), anyString())).thenReturn(new IdempotencyResult.InProgress());
        UUID transactionId = com.ledger.wallet.application.idempotency.IdempotencyKeys.transactionId(userId, "key");
        when(ledgerClient.getTransaction(transactionId)).thenReturn(Optional.empty());

        assertThatExceptionOfType(TransferInProgressException.class)
                .isThrownBy(() -> useCase.execute(request(sourceId, destinationId, "10.00"), "key", principal));

        verify(idempotencyService, never()).complete(any(), anyString(), anyInt(), anyString());
    }

    @Test
    void alreadyPosted_treatedAsSuccess_usesOriginalPostedAt() {
        stubValidWallets();
        UUID transactionId = UUID.randomUUID();
        Instant originalPostedAt = Instant.now().minusSeconds(30);
        when(idempotencyService.begin(eq(userId), eq("key"), anyString())).thenReturn(new IdempotencyResult.New(transactionId));
        when(ledgerClient.postPosting(any(), anyString(), any(), any()))
                .thenReturn(new PostPostingResult.AlreadyPosted(
                        new LedgerPosting(transactionId, "TRANSFER", "test", originalPostedAt, List.of())));

        TransferOutcome outcome = useCase.execute(request(sourceId, destinationId, "10.00"), "key", principal);

        assertThat(outcome.httpStatus()).isEqualTo(201);
        assertThat(outcome.body()).contains(originalPostedAt.toString());
        verify(idempotencyService).complete(userId, "key", 201, outcome.body());
    }

    @Test
    void rejectedByLedger_returns422AndCompletes() {
        stubValidWallets();
        UUID transactionId = UUID.randomUUID();
        when(idempotencyService.begin(eq(userId), eq("key"), anyString())).thenReturn(new IdempotencyResult.New(transactionId));
        when(ledgerClient.postPosting(any(), anyString(), any(), any()))
                .thenReturn(new PostPostingResult.Rejected(DomainErrorCode.INSUFFICIENT_FUNDS, "not enough balance"));

        TransferOutcome outcome = useCase.execute(request(sourceId, destinationId, "10.00"), "key", principal);

        assertThat(outcome.httpStatus()).isEqualTo(422);
        assertThat(outcome.body()).contains(DomainErrorCode.INSUFFICIENT_FUNDS);
        verify(idempotencyService).complete(userId, "key", 422, outcome.body());
    }

    @Test
    void rejectedByLedgerWithDailyLimitExceeded_returns422AndCompletes() {
        stubValidWallets();
        UUID transactionId = UUID.randomUUID();
        when(idempotencyService.begin(eq(userId), eq("key"), anyString())).thenReturn(new IdempotencyResult.New(transactionId));
        when(ledgerClient.postPosting(any(), anyString(), any(), any()))
                .thenReturn(new PostPostingResult.Rejected(DomainErrorCode.DAILY_LIMIT_EXCEEDED, "daily cap exceeded"));

        TransferOutcome outcome = useCase.execute(request(sourceId, destinationId, "10.00"), "key", principal);

        assertThat(outcome.httpStatus()).isEqualTo(422);
        assertThat(outcome.body()).contains(DomainErrorCode.DAILY_LIMIT_EXCEEDED);
        verify(idempotencyService).complete(userId, "key", 422, outcome.body());
    }

    @Test
    void ledgerUnavailable_marksIdempotencyFailedAndRethrows() {
        stubValidWallets();
        UUID transactionId = UUID.randomUUID();
        when(idempotencyService.begin(eq(userId), eq("key"), anyString())).thenReturn(new IdempotencyResult.New(transactionId));
        when(ledgerClient.postPosting(any(), anyString(), any(), any()))
                .thenThrow(new LedgerUnavailableException("down"));

        assertThatExceptionOfType(LedgerUnavailableException.class)
                .isThrownBy(() -> useCase.execute(request(sourceId, destinationId, "10.00"), "key", principal));

        verify(idempotencyService).markFailed(userId, "key");
        verify(idempotencyService, never()).complete(any(), anyString(), anyInt(), anyString());
    }
}
