package com.ledger.wallet.application.usecase;

import com.fasterxml.jackson.core.JsonProcessingException;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.ledger.wallet.api.dto.CreateTransferRequest;
import com.ledger.wallet.api.dto.ErrorResponse;
import com.ledger.wallet.api.dto.ValidationErrorResponse;
import com.ledger.wallet.application.idempotency.IdempotencyKeys;
import com.ledger.wallet.application.idempotency.IdempotencyResult;
import com.ledger.wallet.application.idempotency.IdempotencyService;
import com.ledger.wallet.application.idempotency.RequestFingerprint;
import com.ledger.wallet.domain.exception.DomainValidationException;
import com.ledger.wallet.domain.exception.TransferInProgressException;
import com.ledger.wallet.domain.exception.WalletAccessDeniedException;
import com.ledger.wallet.domain.exception.code.DomainErrorCode;
import com.ledger.wallet.domain.model.DomainError;
import com.ledger.wallet.domain.model.Wallet;
import com.ledger.wallet.infrastructure.ledger.LedgerClient;
import com.ledger.wallet.infrastructure.ledger.LedgerEntryInstruction;
import com.ledger.wallet.infrastructure.ledger.LedgerEntryType;
import com.ledger.wallet.infrastructure.ledger.LedgerUnavailableException;
import com.ledger.wallet.infrastructure.ledger.PostPostingResult;
import com.ledger.wallet.infrastructure.ledger.dto.LedgerPosting;
import com.ledger.wallet.infrastructure.security.AuthenticatedUser;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.stereotype.Service;

import java.math.BigDecimal;
import java.time.Instant;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.UUID;

/**
 * FR-5 end to end: validate (AC-5.5..5.11), run the idempotency lifecycle (Task 04), post to
 * the ledger (Task 03), map every outcome to the spec's status/code table, and recover from a
 * crash at the §9.2 boundary via the Ledger Service's own transaction lookup.
 */
@Service
public class CreateTransferUseCase {

    private static final Logger LOGGER = LoggerFactory.getLogger(CreateTransferUseCase.class);
    private static final ObjectMapper MAPPER = new ObjectMapper();

    private final WalletRepository walletRepository;
    private final IdempotencyService idempotencyService;
    private final LedgerClient ledgerClient;

    public CreateTransferUseCase(
            WalletRepository walletRepository,
            IdempotencyService idempotencyService,
            LedgerClient ledgerClient
    ) {
        this.walletRepository = walletRepository;
        this.idempotencyService = idempotencyService;
        this.ledgerClient = ledgerClient;
    }

    public TransferOutcome execute(CreateTransferRequest request, String idempotencyKey, AuthenticatedUser principal) {
        validate(request, principal);

        if (idempotencyKey == null) {
            LOGGER.warn("Transfer request from user {} has no Idempotency-Key; duplicate protection is the client's responsibility", principal.userId());
            return postToLedger(IdempotencyKeys.randomTransactionId(), request);
        }

        String fingerprint = RequestFingerprint.of(request);
        IdempotencyResult result = idempotencyService.begin(principal.userId(), idempotencyKey, fingerprint);

        return switch (result) {
            case IdempotencyResult.New(UUID transactionId) -> {
                TransferOutcome outcome;
                try {
                    outcome = postToLedger(transactionId, request);
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

    private TransferOutcome postToLedger(UUID transactionId, CreateTransferRequest request) {
        List<LedgerEntryInstruction> entries = List.of(
                new LedgerEntryInstruction(request.sourceWalletId(), LedgerEntryType.DEBIT, request.amount()),
                new LedgerEntryInstruction(request.destinationWalletId(), LedgerEntryType.CREDIT, request.amount())
        );

        PostPostingResult result = ledgerClient.postPosting(transactionId, "TRANSFER", request.description(), entries);

        return switch (result) {
            case PostPostingResult.Posted(Instant postedAt, var ignored) -> successOutcome(transactionId, postedAt);
            case PostPostingResult.AlreadyPosted(LedgerPosting original) -> successOutcome(transactionId, original.postedAt());
            case PostPostingResult.Rejected(String code, String message) -> rejectedOutcome(code, message);
        };
    }

    private void validate(CreateTransferRequest request, AuthenticatedUser principal) {
        if (request.sourceWalletId().equals(request.destinationWalletId())) {
            throw new DomainValidationException(List.of(DomainError.of(
                    DomainErrorCode.SELF_TRANSFER, "source and destination wallets must differ")));
        }

        Wallet source = walletRepository.getById(request.sourceWalletId(), principal.userId())
                .orElseThrow(WalletAccessDeniedException::new);

        Optional<Wallet> destinationOpt = walletRepository.getById(request.destinationWalletId());

        if (!source.isActive()) {
            throw notActive("source");
        }
        if (destinationOpt.isEmpty() || !destinationOpt.get().isActive()) {
            throw notActive("destination");
        }

        Wallet destination = destinationOpt.get();

        if (!source.currency().equals(destination.currency())) {
            throw new DomainValidationException(List.of(DomainError.of(
                    DomainErrorCode.CURRENCY_MISMATCH, "source and destination currencies differ")));
        }
    }

    private static DomainValidationException notActive(String which) {
        return new DomainValidationException(List.of(DomainError.of(
                DomainErrorCode.WALLET_NOT_ACTIVE, which + " wallet is not ACTIVE")));
    }

    private TransferOutcome successOutcome(UUID transactionId, Instant postedAt) {
        // Built as a canonical map (not the TransferResponse record) so this plain ObjectMapper
        // never has to serialize an Instant -- same reasoning as RequestFingerprint.
        Map<String, Object> body = new LinkedHashMap<>();
        body.put("transferId", transactionId.toString());
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
            throw new IllegalStateException("Failed to serialize transfer response", e);
        }
    }
}
