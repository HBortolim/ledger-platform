package com.ledger.wallet.infrastructure.ledger;

/**
 * Callers must surface this as 503 + Retry-After, never 500
 * (SPEC.md NFR-AVAIL-4).
 */
public class LedgerUnavailableException extends RuntimeException {

    public LedgerUnavailableException(String message) {
        super(message);
    }

    public LedgerUnavailableException(String message, Throwable cause) {
        super(message, cause);
    }
}
