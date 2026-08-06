package com.ledger.wallet.api.dto;

import jakarta.validation.ConstraintViolation;
import jakarta.validation.Validation;
import jakarta.validation.Validator;
import org.junit.jupiter.api.Test;

import java.math.BigDecimal;
import java.util.Set;
import java.util.UUID;

import static org.junit.jupiter.api.Assertions.assertTrue;

class CreateTransferRequestValidationTest {

    private static final Validator VALIDATOR = Validation.buildDefaultValidatorFactory().getValidator();

    private CreateTransferRequest requestWithAmount(String amount) {
        return new CreateTransferRequest(UUID.randomUUID(), UUID.randomUUID(), new BigDecimal(amount), null);
    }

    @Test
    void exactlyTwoDecimalPlaces_isValid() {
        Set<ConstraintViolation<CreateTransferRequest>> violations = VALIDATOR.validate(requestWithAmount("100.00"));

        assertTrue(violations.isEmpty(), "expected no violations, got: " + violations);
    }

    @Test
    void oneDecimalPlace_isRejected() {
        Set<ConstraintViolation<CreateTransferRequest>> violations = VALIDATOR.validate(requestWithAmount("10.5"));

        assertViolatesAmount(violations);
    }

    @Test
    void noDecimalPlaces_isRejected() {
        Set<ConstraintViolation<CreateTransferRequest>> violations = VALIDATOR.validate(requestWithAmount("100"));

        assertViolatesAmount(violations);
    }

    @Test
    void threeDecimalPlaces_isRejected() {
        Set<ConstraintViolation<CreateTransferRequest>> violations = VALIDATOR.validate(requestWithAmount("10.555"));

        assertViolatesAmount(violations);
    }

    private void assertViolatesAmount(Set<ConstraintViolation<CreateTransferRequest>> violations) {
        assertTrue(violations.stream().anyMatch(v -> v.getPropertyPath().toString().equals("amount")),
                "expected a violation on 'amount', got: " + violations);
    }
}
