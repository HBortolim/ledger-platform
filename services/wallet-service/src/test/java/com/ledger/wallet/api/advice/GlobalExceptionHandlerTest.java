package com.ledger.wallet.api.advice;

import com.ledger.wallet.infrastructure.ledger.LedgerUnavailableException;
import org.junit.jupiter.api.Test;
import org.springframework.http.HttpStatus;
import org.springframework.http.ResponseEntity;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

class GlobalExceptionHandlerTest {

    private final GlobalExceptionHandler handler = new GlobalExceptionHandler();

    @Test
    void handleLedgerUnavailable_returns503WithRetryAfterAndCode() {
        ResponseEntity<?> response = handler.handleLedgerUnavailable(
                new LedgerUnavailableException("ledger service unreachable"));

        assertEquals(HttpStatus.SERVICE_UNAVAILABLE, response.getStatusCode());
        assertEquals("2", response.getHeaders().getFirst("Retry-After"));
        assertTrue(response.getBody().toString().contains("LEDGER_UNAVAILABLE"),
                "expected body to contain LEDGER_UNAVAILABLE, got: " + response.getBody());
    }
}
