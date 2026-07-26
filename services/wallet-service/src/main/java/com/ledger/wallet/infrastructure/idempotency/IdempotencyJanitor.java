package com.ledger.wallet.infrastructure.idempotency;

import com.ledger.wallet.application.idempotency.IdempotencyRepository;
import org.springframework.scheduling.annotation.Scheduled;
import org.springframework.stereotype.Component;

import java.time.Duration;
import java.time.Instant;

/**
 * §9.1 recovery + AC-5.14: sweeps PENDING records stuck for more than 60s to FAILED (freeing
 * the key), then deletes rows past their TTL.
 */
@Component
public class IdempotencyJanitor {

    private static final Duration PENDING_TIMEOUT = Duration.ofSeconds(60);

    private final IdempotencyRepository repository;

    public IdempotencyJanitor(IdempotencyRepository repository) {
        this.repository = repository;
    }

    // "000" appended turns the seconds property into milliseconds without a SpEL bean lookup
    // (@ConfigurationProperties beans register under a generated name, not the simple one).
    @Scheduled(fixedDelayString = "${ledger.idempotency.pending-sweep-seconds}000")
    public void sweep() {
        Instant now = Instant.now();
        // Fail stale PENDING rows first so they're immediately eligible for the expiry delete below.
        repository.failStalePending(now.minus(PENDING_TIMEOUT));
        repository.deleteExpired(now);
    }
}
