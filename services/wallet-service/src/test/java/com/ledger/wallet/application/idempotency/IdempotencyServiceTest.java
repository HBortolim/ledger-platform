package com.ledger.wallet.application.idempotency;

import com.ledger.wallet.domain.model.IdempotencyRecord;
import com.ledger.wallet.domain.model.IdempotencyStatus;
import org.junit.jupiter.api.Test;
import org.springframework.dao.DuplicateKeyException;

import java.time.Duration;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.Optional;
import java.util.UUID;

import static org.assertj.core.api.Assertions.assertThat;

class IdempotencyServiceTest {

    private static final String FINGERPRINT = "f".repeat(64);
    private static final String OTHER_FINGERPRINT = "a".repeat(64);
    private static final Duration TTL = Duration.ofHours(24);
    private static final Duration MAX_WAIT = Duration.ofMillis(300);
    private static final Duration POLL_INTERVAL = Duration.ofMillis(50);

    private IdempotencyService serviceWith(IdempotencyRepository repository) {
        return new IdempotencyService(repository, TTL, MAX_WAIT, POLL_INTERVAL);
    }

    @Test
    void begin_noRecord_returnsNewWithDeterministicTransactionId() {
        InMemoryIdempotencyRepository repository = new InMemoryIdempotencyRepository();
        IdempotencyService service = serviceWith(repository);
        UUID userId = UUID.randomUUID();

        IdempotencyResult result = service.begin(userId, "key-1", FINGERPRINT);

        assertThat(result).isInstanceOf(IdempotencyResult.New.class);
        UUID transactionId = ((IdempotencyResult.New) result).transactionId();
        assertThat(transactionId).isEqualTo(IdempotencyKeys.transactionId(userId, "key-1"));
    }

    @Test
    void begin_afterCompletion_replaysCachedResponseByteForByte() {
        InMemoryIdempotencyRepository repository = new InMemoryIdempotencyRepository();
        IdempotencyService service = serviceWith(repository);
        UUID userId = UUID.randomUUID();

        service.begin(userId, "key-2", FINGERPRINT);
        service.complete(userId, "key-2", 201, "{\"transferId\": \"abc\",  \"status\":\"COMPLETED\"}");

        IdempotencyResult result = service.begin(userId, "key-2", FINGERPRINT);

        assertThat(result).isEqualTo(new IdempotencyResult.Replay(201, "{\"transferId\": \"abc\",  \"status\":\"COMPLETED\"}"));
    }

    @Test
    void begin_sameKeyDifferentFingerprint_returnsMismatchWithoutMutatingRecord() {
        InMemoryIdempotencyRepository repository = new InMemoryIdempotencyRepository();
        IdempotencyService service = serviceWith(repository);
        UUID userId = UUID.randomUUID();

        service.begin(userId, "key-3", FINGERPRINT);
        IdempotencyResult result = service.begin(userId, "key-3", OTHER_FINGERPRINT);

        assertThat(result).isInstanceOf(IdempotencyResult.Mismatch.class);
        IdempotencyRecord stored = repository.find(userId, "key-3").orElseThrow();
        assertThat(stored.status()).isEqualTo(IdempotencyStatus.PENDING);
        assertThat(stored.requestFingerprint()).isEqualTo(FINGERPRINT);
    }

    @Test
    void begin_sameKeyDifferentUsers_areIndependentRecords() {
        InMemoryIdempotencyRepository repository = new InMemoryIdempotencyRepository();
        IdempotencyService service = serviceWith(repository);
        UUID userA = UUID.randomUUID();
        UUID userB = UUID.randomUUID();

        IdempotencyResult resultA = service.begin(userA, "shared-key", FINGERPRINT);
        IdempotencyResult resultB = service.begin(userB, "shared-key", FINGERPRINT);

        assertThat(resultA).isInstanceOf(IdempotencyResult.New.class);
        assertThat(resultB).isInstanceOf(IdempotencyResult.New.class);
        assertThat(((IdempotencyResult.New) resultA).transactionId())
                .isNotEqualTo(((IdempotencyResult.New) resultB).transactionId());
    }

    @Test
    void begin_pendingWithNoCompleter_waitsThenReturnsInProgress() {
        InMemoryIdempotencyRepository repository = new InMemoryIdempotencyRepository();
        IdempotencyService service = serviceWith(repository);
        UUID userId = UUID.randomUUID();

        service.begin(userId, "key-4", FINGERPRINT);

        Instant start = Instant.now();
        IdempotencyResult result = service.begin(userId, "key-4", FINGERPRINT);
        Duration elapsed = Duration.between(start, Instant.now());

        assertThat(result).isInstanceOf(IdempotencyResult.InProgress.class);
        assertThat(elapsed).isGreaterThanOrEqualTo(MAX_WAIT.minusMillis(20));
    }

    @Test
    void begin_pendingCompletedByAnotherWriterMidPoll_returnsReplay() throws InterruptedException {
        InMemoryIdempotencyRepository repository = new InMemoryIdempotencyRepository();
        IdempotencyService service = serviceWith(repository);
        UUID userId = UUID.randomUUID();

        service.begin(userId, "key-5", FINGERPRINT);

        Thread completer = new Thread(() -> {
            try {
                Thread.sleep(80);
            } catch (InterruptedException ignored) {
                Thread.currentThread().interrupt();
            }
            service.complete(userId, "key-5", 201, "{\"ok\":true}");
        });
        completer.start();

        IdempotencyResult result = service.begin(userId, "key-5", FINGERPRINT);
        completer.join();

        assertThat(result).isEqualTo(new IdempotencyResult.Replay(201, "{\"ok\":true}"));
    }

    @Test
    void begin_failedRecord_isTreatedAsMiss_keyReusable() {
        InMemoryIdempotencyRepository repository = new InMemoryIdempotencyRepository();
        IdempotencyService service = serviceWith(repository);
        UUID userId = UUID.randomUUID();

        service.begin(userId, "key-6", FINGERPRINT);
        repository.markFailed(userId, "key-6");

        IdempotencyResult result = service.begin(userId, "key-6", OTHER_FINGERPRINT);

        assertThat(result).isInstanceOf(IdempotencyResult.New.class);
        IdempotencyRecord stored = repository.find(userId, "key-6").orElseThrow();
        assertThat(stored.status()).isEqualTo(IdempotencyStatus.PENDING);
        assertThat(stored.requestFingerprint()).isEqualTo(OTHER_FINGERPRINT);
    }

    @Test
    void begin_completedRecordPastExpiry_isTreatedAsMiss_keyReusable() {
        InMemoryIdempotencyRepository repository = new InMemoryIdempotencyRepository();
        IdempotencyService service = serviceWith(repository);
        UUID userId = UUID.randomUUID();
        Instant past = Instant.now().minus(1, ChronoUnit.HOURS);

        repository.replaceWithPending(new IdempotencyRecord(
                "key-7", userId, FINGERPRINT, IdempotencyStatus.COMPLETED,
                201, "{}", past.minus(24, ChronoUnit.HOURS), past));

        IdempotencyResult result = service.begin(userId, "key-7", FINGERPRINT);

        assertThat(result).isInstanceOf(IdempotencyResult.New.class);
    }

    @Test
    void begin_stalePendingSweptToFailed_isTreatedAsMiss_keyReusable() {
        InMemoryIdempotencyRepository repository = new InMemoryIdempotencyRepository();
        IdempotencyService service = serviceWith(repository);
        UUID userId = UUID.randomUUID();
        Instant longAgo = Instant.now().minus(2, ChronoUnit.HOURS);

        repository.insertPending(new IdempotencyRecord(
                "key-8", userId, FINGERPRINT, IdempotencyStatus.PENDING,
                null, null, longAgo, longAgo.plus(24, ChronoUnit.HOURS)));
        repository.failStalePending(Instant.now().minus(60, ChronoUnit.SECONDS));

        IdempotencyResult result = service.begin(userId, "key-8", FINGERPRINT);

        assertThat(result).isInstanceOf(IdempotencyResult.New.class);
    }

    @Test
    void begin_loserOfInsertRace_fallsIntoPendingBranchInsteadOfThrowing() {
        InMemoryIdempotencyRepository real = new InMemoryIdempotencyRepository();
        UUID userId = UUID.randomUUID();

        // Simulates two callers finding no record and racing to insertPending: the winner's
        // row actually lands, and our own insert attempt throws DuplicateKeyException.
        IdempotencyRepository racy = new IdempotencyRepository() {
            private boolean firstInsert = true;

            @Override
            public void insertPending(IdempotencyRecord record) {
                if (firstInsert) {
                    firstInsert = false;
                    real.insertPending(record);
                    throw new DuplicateKeyException("simulated concurrent winner");
                }
                real.insertPending(record);
            }

            @Override
            public Optional<IdempotencyRecord> find(UUID u, String k) {
                return real.find(u, k);
            }

            @Override
            public void complete(UUID u, String k, int s, String b, Instant e) {
                real.complete(u, k, s, b, e);
            }

            @Override
            public void markFailed(UUID u, String k) {
                real.markFailed(u, k);
            }

            @Override
            public void replaceWithPending(IdempotencyRecord r) {
                real.replaceWithPending(r);
            }

            @Override
            public int failStalePending(Instant cutoff) {
                return real.failStalePending(cutoff);
            }

            @Override
            public int deleteExpired(Instant now) {
                return real.deleteExpired(now);
            }
        };

        IdempotencyService service = new IdempotencyService(racy, TTL, MAX_WAIT, POLL_INTERVAL);

        IdempotencyResult result = service.begin(userId, "race-key", FINGERPRINT);

        // Nobody ever completes the winner's row, so we time out into InProgress --
        // the important thing is we didn't blow up with an uncaught DuplicateKeyException.
        assertThat(result).isInstanceOf(IdempotencyResult.InProgress.class);
    }

    @Test
    void complete_persistsResponseAndExpiry() {
        InMemoryIdempotencyRepository repository = new InMemoryIdempotencyRepository();
        IdempotencyService service = serviceWith(repository);
        UUID userId = UUID.randomUUID();

        service.begin(userId, "key-9", FINGERPRINT);
        service.complete(userId, "key-9", 422, "{\"code\":\"INSUFFICIENT_FUNDS\"}");

        IdempotencyRecord stored = repository.find(userId, "key-9").orElseThrow();
        assertThat(stored.status()).isEqualTo(IdempotencyStatus.COMPLETED);
        assertThat(stored.responseStatus()).isEqualTo(422);
        assertThat(stored.responseBody()).isEqualTo("{\"code\":\"INSUFFICIENT_FUNDS\"}");
        assertThat(stored.expiresAt()).isAfter(Instant.now().plus(23, ChronoUnit.HOURS));
    }

    @Test
    void markFailed_setsFailedStatus() {
        InMemoryIdempotencyRepository repository = new InMemoryIdempotencyRepository();
        IdempotencyService service = serviceWith(repository);
        UUID userId = UUID.randomUUID();

        service.begin(userId, "key-10", FINGERPRINT);
        service.markFailed(userId, "key-10");

        assertThat(repository.find(userId, "key-10").orElseThrow().status()).isEqualTo(IdempotencyStatus.FAILED);
    }
}
