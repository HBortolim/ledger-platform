package com.ledger.wallet.domain.exception.code;

public final class DomainErrorCode {
    public static final String UNSUPPORTED_CURRENCY = "UNSUPPORTED_CURRENCY";

    // FR-5: POST /transfers
    public static final String SELF_TRANSFER = "SELF_TRANSFER";
    public static final String WALLET_NOT_ACTIVE = "WALLET_NOT_ACTIVE";
    public static final String CURRENCY_MISMATCH = "CURRENCY_MISMATCH";
    public static final String INVALID_AMOUNT = "INVALID_AMOUNT";
    public static final String INSUFFICIENT_FUNDS = "INSUFFICIENT_FUNDS";
    public static final String DAILY_LIMIT_EXCEEDED = "DAILY_LIMIT_EXCEEDED"; // ADR-0011: enforced in Ledger Service, passed through verbatim
    public static final String IDEMPOTENCY_KEY_MISMATCH = "IDEMPOTENCY_KEY_MISMATCH";
    public static final String IN_PROGRESS = "IN_PROGRESS";
    public static final String LEDGER_UNAVAILABLE = "LEDGER_UNAVAILABLE";

    private DomainErrorCode() {}
}
