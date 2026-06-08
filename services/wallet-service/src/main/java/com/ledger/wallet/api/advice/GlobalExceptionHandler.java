package com.ledger.wallet.api.advice;

import com.ledger.wallet.domain.exception.DomainValidationException;
import com.ledger.wallet.domain.model.DomainError;
import org.springframework.http.HttpStatus;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.ExceptionHandler;
import org.springframework.web.bind.annotation.RestControllerAdvice;

import java.util.List;

@RestControllerAdvice
public class GlobalExceptionHandler {

    private record ErrorResponse(String code, String message) {}
    private record ValidationErrorResponse(List<ErrorResponse> errors) {}

    @ExceptionHandler(DomainValidationException.class)
    public ResponseEntity<ValidationErrorResponse> handleDomainValidation(DomainValidationException ex) {
        List<ErrorResponse> errors = ex.getErrors().stream()
                .map(e -> new ErrorResponse(e.code(), e.message()))
                .toList();
        return ResponseEntity.status(HttpStatus.UNPROCESSABLE_CONTENT).body(new ValidationErrorResponse(errors));
    }
}
