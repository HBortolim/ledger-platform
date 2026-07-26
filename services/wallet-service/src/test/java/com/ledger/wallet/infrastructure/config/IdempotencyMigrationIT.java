package com.ledger.wallet.infrastructure.config;

import com.ledger.wallet.support.BaseIntegrationTest;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.dao.DataAccessException;
import org.springframework.jdbc.core.namedparam.NamedParameterJdbcTemplate;

import java.util.Map;
import java.util.UUID;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatExceptionOfType;

// Regression test for V3__idempotency_status.sql (Task 04).
class IdempotencyMigrationIT extends BaseIntegrationTest {

    @Autowired
    private NamedParameterJdbcTemplate jdbc;

    @Test
    void pendingRecord_withNullResponseFields_canBeInserted() {
        UUID userId = UUID.randomUUID();
        jdbc.update(
                "INSERT INTO wallet_db.idempotency_records "
                        + "(key, user_id, request_fingerprint, status, created_at, expires_at) "
                        + "VALUES (:key, :userId, :fingerprint, 'PENDING', now(), now() + interval '24 hours')",
                Map.of("key", "k-" + userId, "userId", userId, "fingerprint", "f".repeat(64)));

        Integer status = jdbc.queryForObject(
                "SELECT count(*) FROM wallet_db.idempotency_records WHERE user_id = :userId AND status = 'PENDING' "
                        + "AND response_status IS NULL AND response_body IS NULL",
                Map.of("userId", userId), Integer.class);

        assertThat(status).isEqualTo(1);
    }

    @Test
    void statusColumn_rejectsUnknownValue() {
        UUID userId = UUID.randomUUID();

        assertThatExceptionOfType(DataAccessException.class)
                .isThrownBy(() -> jdbc.update(
                        "INSERT INTO wallet_db.idempotency_records "
                                + "(key, user_id, request_fingerprint, status, created_at, expires_at) "
                                + "VALUES (:key, :userId, :fingerprint, 'BOGUS', now(), now() + interval '24 hours')",
                        Map.of("key", "k-" + userId, "userId", userId, "fingerprint", "f".repeat(64))));
    }

    @Test
    void existingRow_defaultsStatusToCompleted() {
        UUID userId = UUID.randomUUID();
        jdbc.update(
                "INSERT INTO wallet_db.idempotency_records "
                        + "(key, user_id, request_fingerprint, response_status, response_body, created_at, expires_at) "
                        + "VALUES (:key, :userId, :fingerprint, 201, '{}', now(), now() + interval '24 hours')",
                Map.of("key", "k-" + userId, "userId", userId, "fingerprint", "f".repeat(64)));

        String status = jdbc.queryForObject(
                "SELECT status FROM wallet_db.idempotency_records WHERE user_id = :userId",
                Map.of("userId", userId), String.class);

        assertThat(status).isEqualTo("COMPLETED");
    }

    @Test
    void walletApp_canDeleteIdempotencyRecords() {
        UUID userId = UUID.randomUUID();
        jdbc.update(
                "INSERT INTO wallet_db.idempotency_records "
                        + "(key, user_id, request_fingerprint, status, created_at, expires_at) "
                        + "VALUES (:key, :userId, :fingerprint, 'FAILED', now(), now() + interval '24 hours')",
                Map.of("key", "k-" + userId, "userId", userId, "fingerprint", "f".repeat(64)));

        jdbc.update(
                "DELETE FROM wallet_db.idempotency_records WHERE user_id = :userId",
                Map.of("userId", userId));

        Integer found = jdbc.queryForObject(
                "SELECT count(*) FROM wallet_db.idempotency_records WHERE user_id = :userId",
                Map.of("userId", userId), Integer.class);

        assertThat(found).isEqualTo(0);
    }
}
