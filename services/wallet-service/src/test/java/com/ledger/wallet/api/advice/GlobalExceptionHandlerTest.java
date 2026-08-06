package com.ledger.wallet.api.advice;

import com.ledger.wallet.api.dto.CreateTransferRequest;
import com.ledger.wallet.api.dto.CreateWalletRequest;
import com.ledger.wallet.domain.exception.TransferInProgressException;
import com.ledger.wallet.domain.exception.WalletAccessDeniedException;
import com.ledger.wallet.infrastructure.ledger.LedgerUnavailableException;
import org.junit.jupiter.api.Test;
import org.springframework.core.MethodParameter;
import org.springframework.http.HttpStatus;
import org.springframework.http.ResponseEntity;
import org.springframework.validation.BeanPropertyBindingResult;
import org.springframework.web.bind.MethodArgumentNotValidException;

import java.math.BigDecimal;
import java.util.UUID;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

class GlobalExceptionHandlerTest {

    private final GlobalExceptionHandler handler = new GlobalExceptionHandler();

    private static class DummyController {
        void transfer(CreateTransferRequest request) {}
        void createWallet(CreateWalletRequest request) {}
    }

    @Test
    void handleLedgerUnavailable_returns503WithRetryAfterAndCode() {
        ResponseEntity<?> response = handler.handleLedgerUnavailable(
                new LedgerUnavailableException("ledger service unreachable"));

        assertEquals(HttpStatus.SERVICE_UNAVAILABLE, response.getStatusCode());
        assertEquals("2", response.getHeaders().getFirst("Retry-After"));
        assertTrue(response.getBody().toString().contains("LEDGER_UNAVAILABLE"),
                "expected body to contain LEDGER_UNAVAILABLE, got: " + response.getBody());
    }

    @Test
    void handleWalletAccessDenied_returns403WithEmptyBody() {
        ResponseEntity<?> response = handler.handleWalletAccessDenied(new WalletAccessDeniedException());

        assertEquals(HttpStatus.FORBIDDEN, response.getStatusCode());
        assertNull(response.getBody());
    }

    @Test
    void handleTransferInProgress_returns409WithInProgressCode() {
        ResponseEntity<?> response = handler.handleTransferInProgress(new TransferInProgressException());

        assertEquals(HttpStatus.CONFLICT, response.getStatusCode());
        assertTrue(response.getBody().toString().contains("IN_PROGRESS"),
                "expected body to contain IN_PROGRESS, got: " + response.getBody());
    }

    @Test
    void handleValidationError_forTransferAmountFailure_returns422WithInvalidAmountCode() throws NoSuchMethodException {
        CreateTransferRequest request = new CreateTransferRequest(
                UUID.randomUUID(), UUID.randomUUID(), new BigDecimal("-1.00"), null);
        BeanPropertyBindingResult bindingResult = new BeanPropertyBindingResult(request, "createTransferRequest");
        bindingResult.rejectValue("amount", "DecimalMin", "must be greater than 0");
        MethodParameter methodParameter = new MethodParameter(
                DummyController.class.getDeclaredMethod("transfer", CreateTransferRequest.class), 0);
        MethodArgumentNotValidException ex = new MethodArgumentNotValidException(methodParameter, bindingResult);

        ResponseEntity<?> response = handler.handleValidationError(ex);

        assertEquals(HttpStatus.UNPROCESSABLE_CONTENT, response.getStatusCode());
        assertTrue(response.getBody().toString().contains("INVALID_AMOUNT"),
                "expected body to contain INVALID_AMOUNT, got: " + response.getBody());
    }

    @Test
    void handleValidationError_forNonTransferRequest_returns400() throws NoSuchMethodException {
        CreateWalletRequest request = new CreateWalletRequest("");
        BeanPropertyBindingResult bindingResult = new BeanPropertyBindingResult(request, "createWalletRequest");
        bindingResult.rejectValue("currency", "NotBlank", "must not be blank");
        MethodParameter methodParameter = new MethodParameter(
                DummyController.class.getDeclaredMethod("createWallet", CreateWalletRequest.class), 0);
        MethodArgumentNotValidException ex = new MethodArgumentNotValidException(methodParameter, bindingResult);

        ResponseEntity<?> response = handler.handleValidationError(ex);

        assertEquals(HttpStatus.BAD_REQUEST, response.getStatusCode());
    }
}
