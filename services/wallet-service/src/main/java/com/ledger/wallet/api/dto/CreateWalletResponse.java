package com.ledger.wallet.api.dto;

import java.time.Instant;
import java.util.UUID;

public record CreateWalletResponse(
        UUID walletId,
        String currency,
        String status,
        Instant createdAt
) {}
