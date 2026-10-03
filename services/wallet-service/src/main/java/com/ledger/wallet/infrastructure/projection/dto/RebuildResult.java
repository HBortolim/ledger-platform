package com.ledger.wallet.infrastructure.projection.dto;

import java.math.BigDecimal;
import java.time.Instant;
import java.util.UUID;

// Mirrors the Projection Service's response body verbatim (Task 04, Step 4):
// {"walletId": ..., "balance": "300.00", "entriesApplied": 2, "rebuiltAt": ...}
public record RebuildResult(UUID walletId, BigDecimal balance, int entriesApplied, Instant rebuiltAt) {
}
