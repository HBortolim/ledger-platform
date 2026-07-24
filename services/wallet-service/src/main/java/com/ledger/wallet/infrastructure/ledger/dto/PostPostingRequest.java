package com.ledger.wallet.infrastructure.ledger.dto;

import java.util.List;
import java.util.UUID;

/** Wire shape for a POST /ledger/postings request body (SPEC.md §7.1). */
public record PostPostingRequest(
        UUID transactionId,
        String type,
        String description,
        List<PostPostingEntryRequest> entries
) {}
