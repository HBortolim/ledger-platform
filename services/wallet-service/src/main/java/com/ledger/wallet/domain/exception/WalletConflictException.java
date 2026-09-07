package com.ledger.wallet.domain.exception;

/**
 * A lifecycle request that conflicts with the wallet's current state (AC-3.3, AC-4.1, AC-4.2).
 * Maps to 409. DomainValidationException cannot be reused -- it is hard-wired to 422.
 *
 * <p>Parameter order is (code, message), matching ErrorResponse and every call site.
 */
public class WalletConflictException extends RuntimeException {

    private final String code;

    public WalletConflictException(String code, String message) {
        super(message);
        this.code = code;
    }

    public String getCode() {
        return code;
    }
}
