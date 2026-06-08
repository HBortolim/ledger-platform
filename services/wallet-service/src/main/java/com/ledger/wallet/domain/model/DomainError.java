package com.ledger.wallet.domain.model;

public record DomainError(String code, String message) {

    public static DomainError of(String code, String message) {
        return new DomainError(code, message);
    }
}
