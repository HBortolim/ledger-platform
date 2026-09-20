package com.ledger.wallet.application.usecase;

import com.fasterxml.jackson.core.JsonProcessingException;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.ledger.wallet.api.dto.CreateDepositRequest;
import com.ledger.wallet.api.dto.CreateWithdrawalRequest;
import com.ledger.wallet.api.dto.ErrorResponse;
import com.ledger.wallet.api.dto.ValidationErrorResponse;
import com.ledger.wallet.application.idempotency.IdempotencyKeys;
import com.ledger.wallet.application.idempotency.IdempotencyResult;
import com.ledger.wallet.application.idempotency.IdempotencyService;
import com.ledger.wallet.application.idempotency.RequestFingerprint;
import com.ledger.wallet.domain.exception.DomainValidationException;
import com.ledger.wallet.domain.exception.TransferInProgressException;
import com.ledger.wallet.domain.exception.code.DomainErrorCode;
import com.ledger.wallet.domain.model.DomainError;
import com.ledger.wallet.domain.model.Wallet;
import com.ledger.wallet.infrastructure.ledger.LedgerClient;
import com.ledger.wallet.infrastructure.ledger.LedgerEntryInstruction;
import com.ledger.wallet.infrastructure.ledger.LedgerEntryType;
import com.ledger.wallet.infrastructure.ledger.LedgerServiceProperties;
import com.ledger.wallet.infrastructure.ledger.LedgerUnavailableException;
import com.ledger.wallet.infrastructure.ledger.PostPostingResult;
import com.ledger.wallet.infrastructure.ledger.dto.LedgerPosting;
import com.ledger.wallet.infrastructure.security.AuthenticatedUser;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.stereotype.Service;

import java.time.Instant;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.UUID;

/**
 * FR-6/FR-7: DEPOSIT and WITHDRAWAL, admin-only movements between a wallet and the system
 * funding account. Structurally a near-copy of {@link CreateTransferUseCase} — same validate /
 * idempotency-lifecycle / ledger-post / §9.2-recovery shape — because both directions differ
 * only in which side of the entry pair the system account sits on.
 */
@Service
public class ExternalMovementUseCase {

    private static final Logger LOGGER = LoggerFactory.getLogger(ExternalMovementUseCase.class);
    private static final ObjectMapper MAPPER = new ObjectMapper();

    private final WalletRepository walletRepository;
    private final IdempotencyService idempotencyService;
    private final LedgerClient ledgerClient;
    private final UUID systemAccountId;

    public ExternalMovementUseCase(
            WalletRepository walletRepository,
            IdempotencyService idempotencyService,
            LedgerClient ledgerClient,
            LedgerServiceProperties ledgerServiceProperties
    ) {
        this.walletRepository = walletRepository;
        this.idempotencyService = idempotencyService;
        this.ledgerClient = ledgerClient;
        this.systemAccountId = ledgerServiceProperties.systemAccountId();
    }

    public TransferOutcome deposit(CreateDepositRequest request, String idempotencyKey, AuthenticatedUser principal) {
        validateWallet(request.destinationWalletId());

        List<LedgerEntryInstruction> entries = List.of(
                new LedgerEntryInstruction(systemAccountId, LedgerEntryType.DEBIT, request.amount()),
                new LedgerEntryInstruction(request.destinationWalletId(), LedgerEntryType.CREDIT, request.amount())
        );

        return execute(entries, "DEPOSIT", request.description(), RequestFingerprint.of(request), idempotencyKey, principal);
    }

    public TransferOutcome withdraw(CreateWithdrawalRequest request, String idempotencyKey, AuthenticatedUser principal) {
        validateWallet(request.sourceWalletId());

        List<LedgerEntryInstruction> entries = List.of(
                new LedgerEntryInstruction(request.sourceWalletId(), LedgerEntryType.DEBIT, request.amount()),
                new LedgerEntryInstruction(systemAccountId, LedgerEntryType.CREDIT, request.amount())
        );

        return execute(entries, "WITHDRAWAL", request.description(), RequestFingerprint.of(request), idempotencyKey, principal);
    }

    // Owner-agnostic lookup (Step 6): an admin acts on wallets it does not own, so the
    // ownership-checked overload would 403 every legitimate call. A non-existent wallet returns
    // the same WALLET_NOT_ACTIVE as an inactive one -- the no-existence-leak rule protects
    // *users* from probing each other's wallets, but an admin is already trusted with the whole
    // wallet table, so 403 here would be actively misleading.
    private void validateWallet(UUID walletId) {
        Wallet wallet = walletRepository.getById(walletId)
                .filter(Wallet::isActive)
                .orElseThrow(() -> new DomainValidationException(List.of(DomainError.of(
                        DomainErrorCode.WALLET_NOT_ACTIVE, "wallet is not ACTIVE"))));

        if (!"BRL".equals(wallet.currency())) {
            throw new DomainValidationException(List.of(DomainError.of(
                    DomainErrorCode.UNSUPPORTED_CURRENCY, "Only BRL is supported")));
        }
    }

    private TransferOutcome execute(
            List<LedgerEntryInstruction> entries,
            String type,
            String description,
            String fingerprint,
            String idempotencyKey,
            AuthenticatedUser principal
    ) {
        if (idempotencyKey == null) {
            LOGGER.warn("{} request from user {} has no Idempotency-Key; duplicate protection is the client's responsibility",
                    type, principal.userId());
            return postToLedger(IdempotencyKeys.randomTransactionId(), type, description, entries);
        }

        IdempotencyResult result = idempotencyService.begin(principal.userId(), idempotencyKey, fingerprint);

        return switch (result) {
            case IdempotencyResult.New(UUID transactionId) -> {
                TransferOutcome outcome;
                try {
                    outcome = postToLedger(transactionId, type, description, entries);
                } catch (LedgerUnavailableException e) {
                    idempotencyService.markFailed(principal.userId(), idempotencyKey);
                    throw e;
                }
                idempotencyService.complete(principal.userId(), idempotencyKey, outcome.httpStatus(), outcome.body());
                yield outcome;
            }
            case IdempotencyResult.Replay(int status, String body) -> new TransferOutcome(status, body);
            case IdempotencyResult.Mismatch ignored -> throw new DomainValidationException(List.of(DomainError.of(
                    DomainErrorCode.IDEMPOTENCY_KEY_MISMATCH, "Idempotency-Key was already used with a different request")));
            case IdempotencyResult.InProgress ignored -> recoverFromInProgress(principal.userId(), idempotencyKey);
        };
    }

    /** §9.2: the process that owned this key may have crashed after the ledger already committed. */
    private TransferOutcome recoverFromInProgress(UUID userId, String idempotencyKey) {
        UUID transactionId = IdempotencyKeys.transactionId(userId, idempotencyKey);
        Optional<LedgerPosting> posting = ledgerClient.getTransaction(transactionId);

        if (posting.isPresent()) {
            TransferOutcome outcome = successOutcome(transactionId, posting.get().postedAt());
            idempotencyService.complete(userId, idempotencyKey, outcome.httpStatus(), outcome.body());
            return outcome;
        }
        throw new TransferInProgressException();
    }

    private TransferOutcome postToLedger(UUID transactionId, String type, String description, List<LedgerEntryInstruction> entries) {
        PostPostingResult result = ledgerClient.postPosting(transactionId, type, description, entries);

        return switch (result) {
            case PostPostingResult.Posted(Instant postedAt, var ignored) -> successOutcome(transactionId, postedAt);
            case PostPostingResult.AlreadyPosted(LedgerPosting original) -> successOutcome(transactionId, original.postedAt());
            case PostPostingResult.Rejected(String code, String message) -> rejectedOutcome(code, message);
        };
    }

    private TransferOutcome successOutcome(UUID transactionId, Instant postedAt) {
        Map<String, Object> body = new LinkedHashMap<>();
        body.put("transactionId", transactionId.toString());
        body.put("status", "COMPLETED");
        body.put("postedAt", postedAt.toString());
        return new TransferOutcome(201, writeJson(body));
    }

    private TransferOutcome rejectedOutcome(String code, String message) {
        ValidationErrorResponse response = new ValidationErrorResponse(List.of(new ErrorResponse(code, message)));
        return new TransferOutcome(422, writeJson(response));
    }

    private String writeJson(Object value) {
        try {
            return MAPPER.writeValueAsString(value);
        } catch (JsonProcessingException e) {
            throw new IllegalStateException("Failed to serialize external movement response", e);
        }
    }
}
