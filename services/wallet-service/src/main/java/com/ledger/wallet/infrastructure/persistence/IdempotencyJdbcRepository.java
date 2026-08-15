package com.ledger.wallet.infrastructure.persistence;

import com.ledger.wallet.application.idempotency.IdempotencyRepository;
import com.ledger.wallet.domain.model.IdempotencyRecord;
import com.ledger.wallet.domain.model.IdempotencyStatus;
import org.springframework.jdbc.core.namedparam.MapSqlParameterSource;
import org.springframework.jdbc.core.namedparam.NamedParameterJdbcTemplate;
import org.springframework.stereotype.Repository;

import java.sql.Timestamp;
import java.time.Instant;
import java.util.List;
import java.util.Optional;
import java.util.UUID;

@Repository
public class IdempotencyJdbcRepository implements IdempotencyRepository {

    private final NamedParameterJdbcTemplate jdbc;

    public IdempotencyJdbcRepository(NamedParameterJdbcTemplate jdbc) {
        this.jdbc = jdbc;
    }

    @Override
    public void insertPending(IdempotencyRecord record) {
        String sql = """
                INSERT INTO wallet_db.idempotency_records
                    (key, user_id, request_fingerprint, status, response_status, response_body, created_at, expires_at)
                VALUES
                    (:key, :userId, :fingerprint, :status, :responseStatus, :responseBody, :createdAt, :expiresAt)
                """;
        jdbc.update(sql, paramsFor(record));
    }

    @Override
    public Optional<IdempotencyRecord> find(UUID userId, String key) {
        String sql = """
                SELECT key, user_id, request_fingerprint, status, response_status, response_body, created_at, expires_at
                FROM wallet_db.idempotency_records
                WHERE user_id = :userId AND key = :key
                """;
        MapSqlParameterSource params = new MapSqlParameterSource()
                .addValue("userId", userId)
                .addValue("key", key);

        List<IdempotencyRecord> results = jdbc.query(sql, params, (rs, rowNum) -> new IdempotencyRecord(
                rs.getString("key"),
                rs.getObject("user_id", UUID.class),
                rs.getString("request_fingerprint"),
                IdempotencyStatus.valueOf(rs.getString("status")),
                (Integer) rs.getObject("response_status"),
                rs.getString("response_body"),
                rs.getTimestamp("created_at").toInstant(),
                rs.getTimestamp("expires_at").toInstant()
        ));

        return results.stream().findFirst();
    }

    @Override
    public void complete(UUID userId, String key, int responseStatus, String responseBody, Instant expiresAt) {
        String sql = """
                UPDATE wallet_db.idempotency_records
                SET status = 'COMPLETED',
                    response_status = :responseStatus,
                    response_body = :responseBody,
                    expires_at = :expiresAt
                WHERE user_id = :userId AND key = :key
                """;
        MapSqlParameterSource params = new MapSqlParameterSource()
                .addValue("userId", userId)
                .addValue("key", key)
                .addValue("responseStatus", responseStatus)
                .addValue("responseBody", responseBody)
                .addValue("expiresAt", Timestamp.from(expiresAt));

        jdbc.update(sql, params);
    }

    @Override
    public void markFailed(UUID userId, String key) {
        String sql = """
                UPDATE wallet_db.idempotency_records
                SET status = 'FAILED'
                WHERE user_id = :userId AND key = :key
                """;
        jdbc.update(sql, new MapSqlParameterSource().addValue("userId", userId).addValue("key", key));
    }

    @Override
    public void replaceWithPending(IdempotencyRecord record) {
        String sql = """
                UPDATE wallet_db.idempotency_records
                SET status = :status,
                    request_fingerprint = :fingerprint,
                    response_status = :responseStatus,
                    response_body = :responseBody,
                    created_at = :createdAt,
                    expires_at = :expiresAt
                WHERE user_id = :userId AND key = :key
                """;
        jdbc.update(sql, paramsFor(record));
    }

    @Override
    public int failStalePending(Instant cutoff) {
        String sql = """
                UPDATE wallet_db.idempotency_records
                SET status = 'FAILED'
                WHERE status = 'PENDING' AND created_at < :cutoff
                """;
        return jdbc.update(sql, new MapSqlParameterSource().addValue("cutoff", Timestamp.from(cutoff)));
    }

    @Override
    public int deleteExpired(Instant now) {
        String sql = """
                DELETE FROM wallet_db.idempotency_records
                WHERE expires_at < :now
                """;
        return jdbc.update(sql, new MapSqlParameterSource().addValue("now", Timestamp.from(now)));
    }

    private MapSqlParameterSource paramsFor(IdempotencyRecord record) {
        return new MapSqlParameterSource()
                .addValue("key", record.key())
                .addValue("userId", record.userId())
                .addValue("fingerprint", record.requestFingerprint())
                .addValue("status", record.status().name())
                .addValue("responseStatus", record.responseStatus())
                .addValue("responseBody", record.responseBody())
                .addValue("createdAt", Timestamp.from(record.createdAt()))
                .addValue("expiresAt", Timestamp.from(record.expiresAt()));
    }
}
