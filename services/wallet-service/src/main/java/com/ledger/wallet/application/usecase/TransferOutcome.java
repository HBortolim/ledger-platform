package com.ledger.wallet.application.usecase;

/**
 * The exact bytes {@link CreateTransferUseCase} wants sent back to the client -- whether
 * freshly computed or replayed verbatim from the idempotency cache (AC-5.12).
 */
public record TransferOutcome(int httpStatus, String body) {}
