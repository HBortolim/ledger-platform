package com.ledger.wallet.application.idempotency;

import com.ledger.wallet.domain.model.IdempotencyRecord;

import java.time.Instant;
import java.util.Optional;
import java.util.UUID;

public interface IdempotencyRepository {

    /** Throws {@link org.springframework.dao.DuplicateKeyException} if (userId, key) already exists. */
    void insertPending(IdempotencyRecord record);

    Optional<IdempotencyRecord> find(UUID userId, String key);

    void complete(UUID userId, String key, int responseStatus, String responseBody, Instant expiresAt);

    void markFailed(UUID userId, String key);

    /** Overwrites an existing FAILED/expired row with a fresh PENDING attempt (AC-5.14). */
    void replaceWithPending(IdempotencyRecord record);

    /** Sweeps PENDING rows created before {@code cutoff} to FAILED. Returns the count affected. */
    int failStalePending(Instant cutoff);

    /** Deletes rows whose {@code expires_at} is before {@code now}. Returns the count deleted. */
    int deleteExpired(Instant now);
}
