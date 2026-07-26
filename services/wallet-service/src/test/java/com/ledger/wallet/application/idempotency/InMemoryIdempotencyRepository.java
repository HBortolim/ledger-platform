package com.ledger.wallet.application.idempotency;

import com.ledger.wallet.domain.model.IdempotencyRecord;
import com.ledger.wallet.domain.model.IdempotencyStatus;
import org.springframework.dao.DuplicateKeyException;

import java.time.Instant;
import java.util.Map;
import java.util.Optional;
import java.util.UUID;
import java.util.concurrent.ConcurrentHashMap;

/**
 * In-memory fake mirroring the PK-uniqueness and status-transition behavior of
 * {@code IdempotencyJdbcRepository}, so {@link IdempotencyService}'s state machine can be
 * unit-tested with real (fast, deterministic) collaborator behavior instead of mocks.
 */
class InMemoryIdempotencyRepository implements IdempotencyRepository {

    private final Map<String, IdempotencyRecord> store = new ConcurrentHashMap<>();

    private static String pk(UUID userId, String key) {
        return userId + "::" + key;
    }

    @Override
    public void insertPending(IdempotencyRecord record) {
        IdempotencyRecord previous = store.putIfAbsent(pk(record.userId(), record.key()), record);
        if (previous != null) {
            throw new DuplicateKeyException("duplicate idempotency key: " + pk(record.userId(), record.key()));
        }
    }

    @Override
    public Optional<IdempotencyRecord> find(UUID userId, String key) {
        return Optional.ofNullable(store.get(pk(userId, key)));
    }

    @Override
    public void complete(UUID userId, String key, int responseStatus, String responseBody, Instant expiresAt) {
        store.computeIfPresent(pk(userId, key), (k, r) -> new IdempotencyRecord(
                r.key(), r.userId(), r.requestFingerprint(), IdempotencyStatus.COMPLETED,
                responseStatus, responseBody, r.createdAt(), expiresAt));
    }

    @Override
    public void markFailed(UUID userId, String key) {
        store.computeIfPresent(pk(userId, key), (k, r) -> new IdempotencyRecord(
                r.key(), r.userId(), r.requestFingerprint(), IdempotencyStatus.FAILED,
                r.responseStatus(), r.responseBody(), r.createdAt(), r.expiresAt()));
    }

    @Override
    public void replaceWithPending(IdempotencyRecord record) {
        store.put(pk(record.userId(), record.key()), record);
    }

    @Override
    public int failStalePending(Instant cutoff) {
        int[] count = {0};
        store.replaceAll((k, r) -> {
            if (r.status() == IdempotencyStatus.PENDING && r.createdAt().isBefore(cutoff)) {
                count[0]++;
                return new IdempotencyRecord(r.key(), r.userId(), r.requestFingerprint(), IdempotencyStatus.FAILED,
                        r.responseStatus(), r.responseBody(), r.createdAt(), r.expiresAt());
            }
            return r;
        });
        return count[0];
    }

    @Override
    public int deleteExpired(Instant now) {
        int before = store.size();
        store.values().removeIf(r -> r.expiresAt().isBefore(now));
        return before - store.size();
    }
}
