package com.ledger.wallet.infrastructure.ledger;

import java.math.BigDecimal;
import java.util.UUID;

/**
 * One entry to post, expressed in the caller's own types (UUID/enum/BigDecimal)
 * so the use case layer never has to think about the Ledger Service's wire
 * format (SPEC.md §3.4: amounts go over the wire as 2-decimal-place strings —
 * that conversion is LedgerClient's job, not the caller's).
 */
public record LedgerEntryInstruction(UUID accountId, LedgerEntryType entryType, BigDecimal amount) {}
