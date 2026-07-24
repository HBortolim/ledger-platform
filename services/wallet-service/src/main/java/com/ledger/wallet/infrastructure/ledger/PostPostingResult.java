package com.ledger.wallet.infrastructure.ledger;

import com.ledger.wallet.infrastructure.ledger.dto.LedgerPosting;
import com.ledger.wallet.infrastructure.ledger.dto.LedgerPostingEntry;

import java.time.Instant;
import java.util.List;

/** The outcome of {@link LedgerClient#postPosting}, per SPEC.md §7.1. */
public sealed interface PostPostingResult {

    /** AC-5.1: posted for the first time. */
    record Posted(Instant postedAt, List<LedgerPostingEntry> entries) implements PostPostingResult {}

    /**
     * The transactionId already existed — the §9.2 recovery signal, not an
     * error. The caller should treat this as success using original's data.
     */
    record AlreadyPosted(LedgerPosting original) implements PostPostingResult {}

    /** A 422: invariant violation, insufficient funds, invalid account/amount. code is passed through untranslated. */
    record Rejected(String code, String message) implements PostPostingResult {}
}
