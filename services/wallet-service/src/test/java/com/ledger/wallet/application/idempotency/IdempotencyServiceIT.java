package com.ledger.wallet.application.idempotency;

import com.ledger.wallet.support.BaseIntegrationTest;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;

import java.util.UUID;

import static org.assertj.core.api.Assertions.assertThat;

// Confirms IdempotencyService wires correctly against the real JDBC repository + Postgres
// (the state machine itself is exhaustively unit-tested in IdempotencyServiceTest with a fake).
class IdempotencyServiceIT extends BaseIntegrationTest {

    @Autowired
    private IdempotencyService idempotencyService;

    @Test
    void begin_thenComplete_thenReplay_returnsExactBytesFromPostgres() {
        UUID userId = UUID.randomUUID();
        String fingerprint = "f".repeat(64);
        String responseBody = "{\"transferId\": \"abc\",  \"status\":\"COMPLETED\"}";

        IdempotencyResult first = idempotencyService.begin(userId, "it-key-1", fingerprint);
        assertThat(first).isInstanceOf(IdempotencyResult.New.class);

        idempotencyService.complete(userId, "it-key-1", 201, responseBody);

        IdempotencyResult replay = idempotencyService.begin(userId, "it-key-1", fingerprint);
        assertThat(replay).isEqualTo(new IdempotencyResult.Replay(201, responseBody));
    }

    @Test
    void begin_sameKeyDifferentFingerprint_returnsMismatch() {
        UUID userId = UUID.randomUUID();

        idempotencyService.begin(userId, "it-key-2", "f".repeat(64));
        IdempotencyResult result = idempotencyService.begin(userId, "it-key-2", "a".repeat(64));

        assertThat(result).isInstanceOf(IdempotencyResult.Mismatch.class);
    }
}
