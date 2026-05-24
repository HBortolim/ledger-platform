package com.ledger.wallet.domain.model;

import java.time.Instant;
import java.util.UUID;

public record Wallet(
        UUID id,
        UUID ownerId,
        String currency,
        WalletStatus status,
        Instant createdAt,
        Instant updatedAt
) {
    public boolean isActive() {
        return status == WalletStatus.ACTIVE;
    }

    public boolean isClosed() {
        return status == WalletStatus.CLOSED;
    }
}
