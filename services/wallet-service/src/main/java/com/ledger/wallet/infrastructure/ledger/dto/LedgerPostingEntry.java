package com.ledger.wallet.infrastructure.ledger.dto;

import java.util.UUID;

/** One persisted entry as returned by the Ledger Service (SPEC.md §7.1). */
public record LedgerPostingEntry(UUID entryId, UUID accountId, String entryType, String amount) {}
