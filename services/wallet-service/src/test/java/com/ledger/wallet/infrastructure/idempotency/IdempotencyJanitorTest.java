package com.ledger.wallet.infrastructure.idempotency;

import com.ledger.wallet.application.idempotency.IdempotencyRepository;
import com.ledger.wallet.domain.model.IdempotencyRecord;
import org.junit.jupiter.api.Test;

import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.List;
import java.util.Optional;
import java.util.UUID;

import static org.assertj.core.api.Assertions.assertThat;

class IdempotencyJanitorTest {

    private abstract static class NoOpRepository implements IdempotencyRepository {
        @Override public void insertPending(IdempotencyRecord record) {}
        @Override public Optional<IdempotencyRecord> find(UUID userId, String key) { return Optional.empty(); }
        @Override public void complete(UUID userId, String key, int responseStatus, String responseBody, Instant expiresAt) {}
        @Override public void markFailed(UUID userId, String key) {}
        @Override public void replaceWithPending(IdempotencyRecord record) {}
    }

    @Test
    void sweep_failsStalePendingWithCutoff60SecondsAgo() {
        Instant[] capturedCutoff = new Instant[1];
        IdempotencyRepository repository = new NoOpRepository() {
            @Override public int failStalePending(Instant cutoff) { capturedCutoff[0] = cutoff; return 0; }
            @Override public int deleteExpired(Instant now) { return 0; }
        };
        IdempotencyJanitor janitor = new IdempotencyJanitor(repository);

        Instant before = Instant.now();
        janitor.sweep();

        assertThat(capturedCutoff[0]).isNotNull();
        Duration age = Duration.between(capturedCutoff[0], before);
        assertThat(age).isCloseTo(Duration.ofSeconds(60), Duration.ofMillis(1500));
    }

    @Test
    void sweep_deletesExpiredAsOfNow() {
        Instant[] capturedNow = new Instant[1];
        IdempotencyRepository repository = new NoOpRepository() {
            @Override public int failStalePending(Instant cutoff) { return 0; }
            @Override public int deleteExpired(Instant now) { capturedNow[0] = now; return 0; }
        };
        IdempotencyJanitor janitor = new IdempotencyJanitor(repository);

        Instant before = Instant.now();
        janitor.sweep();
        Instant after = Instant.now();

        assertThat(capturedNow[0]).isBetween(before, after);
    }

    @Test
    void sweep_failsStaleBeforeDeletingExpired() {
        List<String> callOrder = new ArrayList<>();
        IdempotencyRepository repository = new NoOpRepository() {
            @Override public int failStalePending(Instant cutoff) { callOrder.add("failStalePending"); return 0; }
            @Override public int deleteExpired(Instant now) { callOrder.add("deleteExpired"); return 0; }
        };
        IdempotencyJanitor janitor = new IdempotencyJanitor(repository);

        janitor.sweep();

        assertThat(callOrder).containsExactly("failStalePending", "deleteExpired");
    }
}
