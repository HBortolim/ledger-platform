package com.ledger.wallet.application.idempotency;

import com.ledger.wallet.domain.model.IdempotencyRecord;
import com.ledger.wallet.domain.model.IdempotencyStatus;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.dao.DuplicateKeyException;
import org.springframework.stereotype.Service;

import java.time.Duration;
import java.time.Instant;
import java.util.Optional;
import java.util.UUID;

/**
 * The `begin`/`complete` state machine behind AC-5.12..5.16: record-or-replay semantics keyed
 * on {@code (userId, key)}, PENDING in-flight coordination, and FAILED/expired records treated
 * as a fresh miss (AC-5.14).
 */
@Service
public class IdempotencyService {

    private static final Duration DEFAULT_MAX_WAIT = Duration.ofSeconds(5);
    private static final Duration DEFAULT_POLL_INTERVAL = Duration.ofMillis(250);

    private final IdempotencyRepository repository;
    private final Duration ttl;
    private final Duration maxWait;
    private final Duration pollInterval;

    @Autowired
    public IdempotencyService(IdempotencyRepository repository, IdempotencyProperties properties) {
        this(repository, Duration.ofHours(properties.ttlHours()), DEFAULT_MAX_WAIT, DEFAULT_POLL_INTERVAL);
    }

    /** Package-visible so tests can exercise the in-flight wait without a real 5s sleep. */
    IdempotencyService(IdempotencyRepository repository, Duration ttl, Duration maxWait, Duration pollInterval) {
        this.repository = repository;
        this.ttl = ttl;
        this.maxWait = maxWait;
        this.pollInterval = pollInterval;
    }

    public IdempotencyResult begin(UUID userId, String key, String fingerprint) {
        Optional<IdempotencyRecord> existing = repository.find(userId, key);
        if (existing.isEmpty()) {
            return attemptInsert(userId, key, fingerprint, InsertMode.FRESH);
        }

        IdempotencyRecord record = existing.get();
        if (isStale(record)) {
            return attemptInsert(userId, key, fingerprint, InsertMode.REPLACE_STALE);
        }

        if (!record.requestFingerprint().equals(fingerprint)) {
            return new IdempotencyResult.Mismatch();
        }

        return switch (record.status()) {
            case COMPLETED -> new IdempotencyResult.Replay(record.responseStatus(), record.responseBody());
            case PENDING -> awaitCompletion(userId, key);
            case FAILED -> throw new IllegalStateException("unreachable: FAILED records are always stale");
        };
    }

    public void complete(UUID userId, String key, int responseStatus, String responseBody) {
        repository.complete(userId, key, responseStatus, responseBody, Instant.now().plus(ttl));
    }

    public void markFailed(UUID userId, String key) {
        repository.markFailed(userId, key);
    }

    private boolean isStale(IdempotencyRecord record) {
        return switch (record.status()) {
            case FAILED -> true;
            case COMPLETED -> !record.expiresAt().isAfter(Instant.now());
            case PENDING -> false;
        };
    }

    /** Whether {@code attemptInsert} is creating a brand-new record or overwriting a stale one (AC-5.14). */
    private enum InsertMode { FRESH, REPLACE_STALE }

    private IdempotencyResult attemptInsert(UUID userId, String key, String fingerprint, InsertMode mode) {
        UUID transactionId = IdempotencyKeys.transactionId(userId, key);
        Instant now = Instant.now();
        IdempotencyRecord pending = new IdempotencyRecord(
                key, userId, fingerprint, IdempotencyStatus.PENDING, null, null, now, now.plus(ttl));

        try {
            switch (mode) {
                case FRESH -> repository.insertPending(pending);
                case REPLACE_STALE -> repository.replaceWithPending(pending);
            }
            return new IdempotencyResult.New(transactionId);
        } catch (DuplicateKeyException raced) {
            return awaitCompletion(userId, key);
        }
    }

    private IdempotencyResult awaitCompletion(UUID userId, String key) {
        Instant deadline = Instant.now().plus(maxWait);
        while (Instant.now().isBefore(deadline)) {
            sleep(pollInterval);

            Optional<IdempotencyRecord> current = repository.find(userId, key);
            if (current.isEmpty()) {
                return new IdempotencyResult.InProgress();
            }

            IdempotencyRecord record = current.get();
            if (record.status() == IdempotencyStatus.COMPLETED) {
                return new IdempotencyResult.Replay(record.responseStatus(), record.responseBody());
            }
            if (record.status() == IdempotencyStatus.FAILED) {
                return new IdempotencyResult.InProgress();
            }
        }
        return new IdempotencyResult.InProgress();
    }

    private void sleep(Duration duration) {
        try {
            Thread.sleep(duration.toMillis());
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new IllegalStateException("Interrupted while waiting for in-flight idempotency record", e);
        }
    }
}
