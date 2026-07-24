package com.ledger.wallet.infrastructure.ledger.dto;

import java.time.Instant;
import java.util.List;
import java.util.UUID;

/**
 * A posted ledger transaction as returned by the Ledger Service — the same
 * shape for a fresh 201, a duplicate-transactionId 409, and a
 * GET /admin/ledger/transactions/{id} 200 (SPEC.md §7.1, §9.2).
 */
public record LedgerPosting(
        UUID transactionId,
        String type,
        String description,
        Instant postedAt,
        List<LedgerPostingEntry> entries
) {}
