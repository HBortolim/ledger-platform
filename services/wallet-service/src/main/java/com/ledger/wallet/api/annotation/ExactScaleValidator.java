package com.ledger.wallet.api.annotation;

import jakarta.validation.ConstraintValidator;
import jakarta.validation.ConstraintValidatorContext;

import java.math.BigDecimal;

public class ExactScaleValidator implements ConstraintValidator<ExactScale, BigDecimal> {

    private int scale;

    @Override
    public void initialize(ExactScale annotation) {
        this.scale = annotation.value();
    }

    @Override
    public boolean isValid(BigDecimal value, ConstraintValidatorContext context) {
        // @NotNull is a separate constraint; null is valid here per Bean Validation convention.
        return value == null || value.scale() == scale;
    }
}
