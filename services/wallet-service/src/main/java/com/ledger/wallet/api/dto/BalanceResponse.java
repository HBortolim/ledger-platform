package com.ledger.wallet.api.dto;

import java.math.BigDecimal;
import java.time.Instant;
import java.util.UUID;

public record BalanceResponse(
        UUID walletId,
        BigDecimal balance,
        String currency,
        Instant asOf,
        boolean stale
) {}
