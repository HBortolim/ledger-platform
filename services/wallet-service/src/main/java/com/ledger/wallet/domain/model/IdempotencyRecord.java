package com.ledger.wallet.domain.model;

import java.time.Instant;
import java.util.UUID;

public record IdempotencyRecord(
        String key,
        UUID userId,
        String requestFingerprint,
        IdempotencyStatus status,
        Integer responseStatus,
        String responseBody,
        Instant createdAt,
        Instant expiresAt
) {}
