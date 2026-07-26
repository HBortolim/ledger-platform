package com.ledger.wallet.application.idempotency;

import java.util.UUID;

public sealed interface IdempotencyResult {

    record New(UUID transactionId) implements IdempotencyResult {}

    record Replay(int responseStatus, String responseBody) implements IdempotencyResult {}

    record Mismatch() implements IdempotencyResult {}

    record InProgress() implements IdempotencyResult {}
}
