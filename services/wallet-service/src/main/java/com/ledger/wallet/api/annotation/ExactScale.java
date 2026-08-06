package com.ledger.wallet.api.annotation;

import jakarta.validation.Constraint;
import jakarta.validation.Payload;

import java.lang.annotation.Documented;
import java.lang.annotation.ElementType;
import java.lang.annotation.Retention;
import java.lang.annotation.RetentionPolicy;
import java.lang.annotation.Target;

/**
 * Requires a {@code BigDecimal} to have exactly {@code value} digits after the decimal
 * point. {@code @Digits(fraction = n)} only enforces a maximum, so "10.5" or "100" (scale
 * 1 or 0) pass it silently
 */
@Documented
@Target({ElementType.METHOD, ElementType.FIELD, ElementType.ANNOTATION_TYPE, ElementType.CONSTRUCTOR,
        ElementType.PARAMETER, ElementType.TYPE_USE})
@Retention(RetentionPolicy.RUNTIME)
@Constraint(validatedBy = ExactScaleValidator.class)
public @interface ExactScale {

    int value();

    String message() default "must have exactly {value} decimal places";

    Class<?>[] groups() default {};

    Class<? extends Payload>[] payload() default {};
}
