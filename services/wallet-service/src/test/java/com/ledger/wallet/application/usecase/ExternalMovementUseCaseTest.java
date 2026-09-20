package com.ledger.wallet.application.usecase;

import com.ledger.wallet.api.dto.CreateDepositRequest;
import com.ledger.wallet.api.dto.CreateWithdrawalRequest;
import com.ledger.wallet.application.idempotency.IdempotencyResult;
import com.ledger.wallet.domain.exception.DomainValidationException;
import com.ledger.wallet.domain.exception.code.DomainErrorCode;
import com.ledger.wallet.domain.model.Wallet;
import com.ledger.wallet.domain.model.WalletStatus;
import com.ledger.wallet.infrastructure.ledger.LedgerClient;
import com.ledger.wallet.infrastructure.ledger.LedgerEntryInstruction;
import com.ledger.wallet.infrastructure.ledger.LedgerEntryType;
import com.ledger.wallet.infrastructure.ledger.LedgerServiceProperties;
import com.ledger.wallet.infrastructure.ledger.LedgerUnavailableException;
import com.ledger.wallet.infrastructure.ledger.PostPostingResult;
import com.ledger.wallet.infrastructure.security.AuthenticatedUser;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.ArgumentCaptor;
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
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

@ExtendWith(MockitoExtension.class)
class ExternalMovementUseCaseTest {

    @Mock
    private WalletRepository walletRepository;
    @Mock
    private com.ledger.wallet.application.idempotency.IdempotencyService idempotencyService;
    @Mock
    private LedgerClient ledgerClient;

    private ExternalMovementUseCase useCase;
    private UUID adminId;
    private UUID walletId;
    private UUID systemAccountId;
    private AuthenticatedUser admin;

    @BeforeEach
    void setUp() {
        systemAccountId = UUID.randomUUID();
        LedgerServiceProperties properties = new LedgerServiceProperties(
                new LedgerServiceProperties.Service("http://ledger"), systemAccountId);
        useCase = new ExternalMovementUseCase(walletRepository, idempotencyService, ledgerClient, properties);
        adminId = UUID.randomUUID();
        walletId = UUID.randomUUID();
        admin = new AuthenticatedUser(adminId, "admin");
    }

    private Wallet activeWallet(UUID id, String currency) {
        Instant now = Instant.now();
        return new Wallet(id, UUID.randomUUID(), currency, WalletStatus.ACTIVE, now, now);
    }

    private Wallet walletWithStatus(UUID id, WalletStatus status) {
        Instant now = Instant.now();
        return new Wallet(id, UUID.randomUUID(), "BRL", status, now, now);
    }

    private CreateDepositRequest depositRequest(String amount) {
        return new CreateDepositRequest(walletId, new BigDecimal(amount), "test");
    }

    private CreateWithdrawalRequest withdrawalRequest(String amount) {
        return new CreateWithdrawalRequest(walletId, new BigDecimal(amount), "test");
    }

    // ---- Entry pairs and direction (AC-6.1) ----

    @Test
    void deposit_debitsSystemAccountAndCreditsDestinationWallet() {
        when(walletRepository.getById(walletId)).thenReturn(Optional.of(activeWallet(walletId, "BRL")));
        when(ledgerClient.postPosting(any(), anyString(), any(), any()))
                .thenReturn(new PostPostingResult.Posted(Instant.now(), List.of()));

        useCase.deposit(depositRequest("50.00"), null, admin);

        ArgumentCaptor<List<LedgerEntryInstruction>> entriesCaptor = ArgumentCaptor.forClass(List.class);
        verify(ledgerClient).postPosting(any(), eq("DEPOSIT"), any(), entriesCaptor.capture());
        List<LedgerEntryInstruction> entries = entriesCaptor.getValue();
        assertThat(entries).containsExactly(
                new LedgerEntryInstruction(systemAccountId, LedgerEntryType.DEBIT, new BigDecimal("50.00")),
                new LedgerEntryInstruction(walletId, LedgerEntryType.CREDIT, new BigDecimal("50.00")));
    }

    @Test
    void withdrawal_debitsSourceWalletAndCreditsSystemAccount() {
        when(walletRepository.getById(walletId)).thenReturn(Optional.of(activeWallet(walletId, "BRL")));
        when(ledgerClient.postPosting(any(), anyString(), any(), any()))
                .thenReturn(new PostPostingResult.Posted(Instant.now(), List.of()));

        useCase.withdraw(withdrawalRequest("50.00"), null, admin);

        ArgumentCaptor<List<LedgerEntryInstruction>> entriesCaptor = ArgumentCaptor.forClass(List.class);
        verify(ledgerClient).postPosting(any(), eq("WITHDRAWAL"), any(), entriesCaptor.capture());
        List<LedgerEntryInstruction> entries = entriesCaptor.getValue();
        assertThat(entries).containsExactly(
                new LedgerEntryInstruction(walletId, LedgerEntryType.DEBIT, new BigDecimal("50.00")),
                new LedgerEntryInstruction(systemAccountId, LedgerEntryType.CREDIT, new BigDecimal("50.00")));
    }

    // ---- Validation ----

    @Test
    void deposit_frozenDestinationWallet_throwsWalletNotActive() {
        when(walletRepository.getById(walletId)).thenReturn(Optional.of(walletWithStatus(walletId, WalletStatus.FROZEN)));

        assertThatExceptionOfType(DomainValidationException.class)
                .isThrownBy(() -> useCase.deposit(depositRequest("10.00"), "key", admin))
                .satisfies(ex -> assertThat(ex.getErrors().get(0).code()).isEqualTo(DomainErrorCode.WALLET_NOT_ACTIVE));

        verifyNoInteractions(ledgerClient, idempotencyService);
    }

    @Test
    void withdraw_nonExistentSourceWallet_throwsWalletNotActive() {
        when(walletRepository.getById(walletId)).thenReturn(Optional.empty());

        assertThatExceptionOfType(DomainValidationException.class)
                .isThrownBy(() -> useCase.withdraw(withdrawalRequest("10.00"), "key", admin))
                .satisfies(ex -> assertThat(ex.getErrors().get(0).code()).isEqualTo(DomainErrorCode.WALLET_NOT_ACTIVE));
    }

    @Test
    void deposit_unsupportedCurrency_throwsUnsupportedCurrency() {
        when(walletRepository.getById(walletId)).thenReturn(Optional.of(activeWallet(walletId, "USD")));

        assertThatExceptionOfType(DomainValidationException.class)
                .isThrownBy(() -> useCase.deposit(depositRequest("10.00"), "key", admin))
                .satisfies(ex -> assertThat(ex.getErrors().get(0).code()).isEqualTo(DomainErrorCode.UNSUPPORTED_CURRENCY));
    }

    // ---- Orchestration ----

    @Test
    void newIdempotencyKey_postedSuccessfully_completesAndReturns201() {
        when(walletRepository.getById(walletId)).thenReturn(Optional.of(activeWallet(walletId, "BRL")));
        UUID transactionId = UUID.randomUUID();
        Instant postedAt = Instant.now();
        when(idempotencyService.begin(eq(adminId), eq("key"), anyString())).thenReturn(new IdempotencyResult.New(transactionId));
        when(ledgerClient.postPosting(any(), anyString(), any(), any()))
                .thenReturn(new PostPostingResult.Posted(postedAt, List.of()));

        TransferOutcome outcome = useCase.deposit(depositRequest("10.00"), "key", admin);

        assertThat(outcome.httpStatus()).isEqualTo(201);
        assertThat(outcome.body()).contains(transactionId.toString()).contains("COMPLETED");
        verify(idempotencyService).complete(adminId, "key", 201, outcome.body());
    }

    @Test
    void replay_returnsCachedResponseWithoutCallingLedger() {
        // Validation runs before the idempotency lookup, so the wallet still needs stubbing.
        when(walletRepository.getById(walletId)).thenReturn(Optional.of(activeWallet(walletId, "BRL")));
        when(idempotencyService.begin(eq(adminId), eq("key"), anyString()))
                .thenReturn(new IdempotencyResult.Replay(201, "{\"cached\":true}"));

        TransferOutcome outcome = useCase.deposit(depositRequest("10.00"), "key", admin);

        assertThat(outcome.httpStatus()).isEqualTo(201);
        assertThat(outcome.body()).isEqualTo("{\"cached\":true}");
        verifyNoInteractions(ledgerClient);
    }

    @Test
    void keylessRequest_postsDirectlyAndNeverTouchesIdempotency() {
        when(walletRepository.getById(walletId)).thenReturn(Optional.of(activeWallet(walletId, "BRL")));
        when(ledgerClient.postPosting(any(), anyString(), any(), any()))
                .thenReturn(new PostPostingResult.Posted(Instant.now(), List.of()));

        TransferOutcome outcome = useCase.withdraw(withdrawalRequest("10.00"), null, admin);

        assertThat(outcome.httpStatus()).isEqualTo(201);
        verifyNoInteractions(idempotencyService);
    }

    @Test
    void rejectedByLedgerWithInsufficientFunds_returns422AndCompletes() {
        when(walletRepository.getById(walletId)).thenReturn(Optional.of(activeWallet(walletId, "BRL")));
        UUID transactionId = UUID.randomUUID();
        when(idempotencyService.begin(eq(adminId), eq("key"), anyString())).thenReturn(new IdempotencyResult.New(transactionId));
        when(ledgerClient.postPosting(any(), anyString(), any(), any()))
                .thenReturn(new PostPostingResult.Rejected(DomainErrorCode.INSUFFICIENT_FUNDS, "not enough balance"));

        TransferOutcome outcome = useCase.withdraw(withdrawalRequest("10.00"), "key", admin);

        assertThat(outcome.httpStatus()).isEqualTo(422);
        assertThat(outcome.body()).contains(DomainErrorCode.INSUFFICIENT_FUNDS);
        verify(idempotencyService).complete(adminId, "key", 422, outcome.body());
    }

    @Test
    void ledgerUnavailable_marksIdempotencyFailedAndRethrows() {
        when(walletRepository.getById(walletId)).thenReturn(Optional.of(activeWallet(walletId, "BRL")));
        UUID transactionId = UUID.randomUUID();
        when(idempotencyService.begin(eq(adminId), eq("key"), anyString())).thenReturn(new IdempotencyResult.New(transactionId));
        when(ledgerClient.postPosting(any(), anyString(), any(), any()))
                .thenThrow(new LedgerUnavailableException("down"));

        assertThatExceptionOfType(LedgerUnavailableException.class)
                .isThrownBy(() -> useCase.deposit(depositRequest("10.00"), "key", admin));

        verify(idempotencyService).markFailed(adminId, "key");
    }
}
