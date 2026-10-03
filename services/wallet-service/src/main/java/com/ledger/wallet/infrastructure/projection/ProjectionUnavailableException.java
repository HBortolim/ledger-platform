package com.ledger.wallet.infrastructure.projection;

public class ProjectionUnavailableException extends RuntimeException {

    public ProjectionUnavailableException(String message) {
        super(message);
    }

    public ProjectionUnavailableException(String message, Throwable cause) {
        super(message, cause);
    }
}
