package com.ledger.wallet.domain.model;

import com.ledger.wallet.domain.exception.code.DomainErrorCode;

import java.time.Instant;
import java.util.ArrayList;
import java.util.List;
import java.util.UUID;

public record Wallet(
        UUID id,
        UUID ownerId,
        String currency,
        WalletStatus status,
        Instant createdAt,
        Instant updatedAt
) {

    public static Result<Wallet, List<DomainError>> create(UUID ownerId, String currency) {
        List<DomainError> errors = new ArrayList<>();

        if (!"BRL".equals(currency)) {
            errors.add(DomainError.of(DomainErrorCode.UNSUPPORTED_CURRENCY, "Only BRL is supported"));
        }

        if (!errors.isEmpty()) {
            return Result.failure(errors);
        }

        Instant now = Instant.now();
        return Result.success(new Wallet(UUID.randomUUID(), ownerId, currency, WalletStatus.ACTIVE, now, now));
    }

    public boolean isActive() {
        return status == WalletStatus.ACTIVE;
    }

    public boolean isClosed() {
        return status == WalletStatus.CLOSED;
    }
}
