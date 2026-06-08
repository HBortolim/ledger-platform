package com.ledger.wallet.domain.exception;

import com.ledger.wallet.domain.model.DomainError;

import java.util.List;

public class DomainValidationException extends RuntimeException {

    private final List<DomainError> errors;

    public DomainValidationException(List<DomainError> errors) {
        super(errors.stream().map(DomainError::code).reduce((a, b) -> a + ", " + b).orElse("validation failed"));
        this.errors = errors;
    }

    public List<DomainError> getErrors() {
        return errors;
    }
}
