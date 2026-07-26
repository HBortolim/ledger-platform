package com.ledger.wallet.infrastructure.persistence;

import com.ledger.wallet.application.idempotency.IdempotencyRepository;
import com.ledger.wallet.domain.model.IdempotencyRecord;
import com.ledger.wallet.domain.model.IdempotencyStatus;
import com.ledger.wallet.support.BaseIntegrationTest;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.dao.DuplicateKeyException;

import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.Optional;
import java.util.UUID;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatExceptionOfType;

class IdempotencyJdbcRepositoryIT extends BaseIntegrationTest {

    @Autowired
    private IdempotencyRepository repository;

    private IdempotencyRecord pending(UUID userId, String key, String fingerprint) {
        Instant now = Instant.now();
        return new IdempotencyRecord(
                key, userId, fingerprint, IdempotencyStatus.PENDING,
                null, null, now, now.plus(24, ChronoUnit.HOURS));
    }

    @Test
    void insertPending_thenFind_returnsSameRecord() {
        UUID userId = UUID.randomUUID();
        IdempotencyRecord record = pending(userId, "key-1", "f".repeat(64));

        repository.insertPending(record);
        Optional<IdempotencyRecord> found = repository.find(userId, "key-1");

        assertThat(found).isPresent();
        assertThat(found.get().status()).isEqualTo(IdempotencyStatus.PENDING);
        assertThat(found.get().requestFingerprint()).isEqualTo("f".repeat(64));
        assertThat(found.get().responseStatus()).isNull();
        assertThat(found.get().responseBody()).isNull();
    }

    @Test
    void find_noRecord_returnsEmpty() {
        assertThat(repository.find(UUID.randomUUID(), "missing")).isEmpty();
    }

    @Test
    void insertPending_duplicateKey_throwsDuplicateKeyException() {
        UUID userId = UUID.randomUUID();
        repository.insertPending(pending(userId, "dup", "f".repeat(64)));

        assertThatExceptionOfType(DuplicateKeyException.class)
                .isThrownBy(() -> repository.insertPending(pending(userId, "dup", "a".repeat(64))));
    }

    @Test
    void sameKey_differentUsers_areIndependentRecords() {
        UUID userA = UUID.randomUUID();
        UUID userB = UUID.randomUUID();

        repository.insertPending(pending(userA, "shared-key", "f".repeat(64)));
        repository.insertPending(pending(userB, "shared-key", "a".repeat(64)));

        assertThat(repository.find(userA, "shared-key")).isPresent();
        assertThat(repository.find(userB, "shared-key")).isPresent();
    }

    @Test
    void complete_setsCompletedStatusAndResponse() {
        UUID userId = UUID.randomUUID();
        repository.insertPending(pending(userId, "key-2", "f".repeat(64)));

        Instant expiresAt = Instant.now().plus(24, ChronoUnit.HOURS);
        repository.complete(userId, "key-2", 201, "{\"transferId\":\"abc\"}", expiresAt);

        IdempotencyRecord found = repository.find(userId, "key-2").orElseThrow();
        assertThat(found.status()).isEqualTo(IdempotencyStatus.COMPLETED);
        assertThat(found.responseStatus()).isEqualTo(201);
        assertThat(found.responseBody()).isEqualTo("{\"transferId\":\"abc\"}");
    }

    @Test
    void markFailed_setsFailedStatus() {
        UUID userId = UUID.randomUUID();
        repository.insertPending(pending(userId, "key-3", "f".repeat(64)));

        repository.markFailed(userId, "key-3");

        IdempotencyRecord found = repository.find(userId, "key-3").orElseThrow();
        assertThat(found.status()).isEqualTo(IdempotencyStatus.FAILED);
    }

    @Test
    void replaceWithPending_overwritesExistingRow() {
        UUID userId = UUID.randomUUID();
        repository.insertPending(pending(userId, "key-4", "f".repeat(64)));
        repository.markFailed(userId, "key-4");

        IdempotencyRecord fresh = pending(userId, "key-4", "b".repeat(64));
        repository.replaceWithPending(fresh);

        IdempotencyRecord found = repository.find(userId, "key-4").orElseThrow();
        assertThat(found.status()).isEqualTo(IdempotencyStatus.PENDING);
        assertThat(found.requestFingerprint()).isEqualTo("b".repeat(64));
        assertThat(found.responseStatus()).isNull();
        assertThat(found.responseBody()).isNull();
    }

    @Test
    void failStalePending_onlyAffectsRecordsCreatedBeforeCutoff() {
        UUID staleUser = UUID.randomUUID();
        UUID freshUser = UUID.randomUUID();
        Instant longAgo = Instant.now().minus(2, ChronoUnit.HOURS);

        repository.insertPending(new IdempotencyRecord(
                "stale", staleUser, "f".repeat(64), IdempotencyStatus.PENDING,
                null, null, longAgo, longAgo.plus(24, ChronoUnit.HOURS)));
        repository.insertPending(pending(freshUser, "fresh", "f".repeat(64)));

        int affected = repository.failStalePending(Instant.now().minus(60, ChronoUnit.SECONDS));

        assertThat(affected).isEqualTo(1);
        assertThat(repository.find(staleUser, "stale").orElseThrow().status()).isEqualTo(IdempotencyStatus.FAILED);
        assertThat(repository.find(freshUser, "fresh").orElseThrow().status()).isEqualTo(IdempotencyStatus.PENDING);
    }

    @Test
    void deleteExpired_removesOnlyRowsPastExpiry() {
        UUID expiredUser = UUID.randomUUID();
        UUID activeUser = UUID.randomUUID();
        Instant past = Instant.now().minus(1, ChronoUnit.HOURS);

        repository.insertPending(new IdempotencyRecord(
                "expired", expiredUser, "f".repeat(64), IdempotencyStatus.COMPLETED,
                201, "{}", past.minus(24, ChronoUnit.HOURS), past));
        repository.insertPending(pending(activeUser, "active", "f".repeat(64)));

        int deleted = repository.deleteExpired(Instant.now());

        assertThat(deleted).isEqualTo(1);
        assertThat(repository.find(expiredUser, "expired")).isEmpty();
        assertThat(repository.find(activeUser, "active")).isPresent();
    }
}
