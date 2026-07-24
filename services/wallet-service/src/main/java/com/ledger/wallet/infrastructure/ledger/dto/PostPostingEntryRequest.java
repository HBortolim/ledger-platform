package com.ledger.wallet.infrastructure.ledger.dto;

import java.util.UUID;

/** Wire shape for one entry in a POST /ledger/postings request (SPEC.md §7.1). */
public record PostPostingEntryRequest(UUID accountId, String entryType, String amount) {}
