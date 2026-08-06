package com.ledger.wallet.api.advice;

import com.ledger.wallet.api.dto.CreateTransferRequest;
import com.ledger.wallet.api.dto.ErrorResponse;
import com.ledger.wallet.api.dto.ValidationErrorResponse;
import com.ledger.wallet.domain.exception.DomainValidationException;
import com.ledger.wallet.domain.exception.TransferInProgressException;
import com.ledger.wallet.domain.exception.WalletAccessDeniedException;
import com.ledger.wallet.domain.exception.code.DomainErrorCode;
import com.ledger.wallet.infrastructure.ledger.LedgerUnavailableException;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpStatus;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.MethodArgumentNotValidException;
import org.springframework.web.bind.annotation.ExceptionHandler;
import org.springframework.web.bind.annotation.RestControllerAdvice;

import java.util.List;

@RestControllerAdvice
public class GlobalExceptionHandler {

    @ExceptionHandler(DomainValidationException.class)
    public ResponseEntity<ValidationErrorResponse> handleDomainValidation(DomainValidationException ex) {
        List<ErrorResponse> errors = ex.getErrors().stream()
                .map(e -> new ErrorResponse(e.code(), e.message()))
                .toList();
        return ResponseEntity.status(HttpStatus.UNPROCESSABLE_CONTENT).body(new ValidationErrorResponse(errors));
    }

    @ExceptionHandler(LedgerUnavailableException.class)
    public ResponseEntity<ErrorResponse> handleLedgerUnavailable(LedgerUnavailableException ex) {
        return ResponseEntity.status(HttpStatus.SERVICE_UNAVAILABLE)
                .header(HttpHeaders.RETRY_AFTER, "2")
                .body(new ErrorResponse(DomainErrorCode.LEDGER_UNAVAILABLE, null));
    }

    // AC-5.11: same response whether the source wallet doesn't exist or just isn't the caller's.
    @ExceptionHandler(WalletAccessDeniedException.class)
    public ResponseEntity<Void> handleWalletAccessDenied(WalletAccessDeniedException ex) {
        return ResponseEntity.status(HttpStatus.FORBIDDEN).build();
    }

    // AC-5.16: waited out the in-flight window with nothing to replay.
    @ExceptionHandler(TransferInProgressException.class)
    public ResponseEntity<ErrorResponse> handleTransferInProgress(TransferInProgressException ex) {
        return ResponseEntity.status(HttpStatus.CONFLICT)
                .body(new ErrorResponse(DomainErrorCode.IN_PROGRESS, "the original request has not completed yet"));
    }

    // AC-5.6: bean validation already rejects amount <= 0 / wrong scale; this only upgrades
    // CreateTransferRequest's amount failures from a bare 400 to 422 INVALID_AMOUNT. Any other
    // endpoint's validation failures (e.g. blank wallet currency) keep the plain 400.
    @ExceptionHandler(MethodArgumentNotValidException.class)
    public ResponseEntity<?> handleValidationError(MethodArgumentNotValidException ex) {
        boolean isTransferAmountFailure = ex.getBindingResult().getTarget() instanceof CreateTransferRequest
                && ex.getBindingResult().getFieldErrors().stream().allMatch(fe -> "amount".equals(fe.getField()));

        if (isTransferAmountFailure) {
            return ResponseEntity.status(HttpStatus.UNPROCESSABLE_CONTENT).body(new ValidationErrorResponse(List.of(
                    new ErrorResponse(DomainErrorCode.INVALID_AMOUNT, "amount must be greater than 0 with exactly 2 decimal places"))));
        }
        return ResponseEntity.badRequest().build();
    }
}
