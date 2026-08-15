package com.ledger.wallet.infrastructure.persistence;

import com.ledger.wallet.application.usecase.BalanceRepository;
import com.ledger.wallet.domain.model.Balance;
import org.springframework.jdbc.core.namedparam.MapSqlParameterSource;
import org.springframework.jdbc.core.namedparam.NamedParameterJdbcTemplate;
import org.springframework.stereotype.Repository;

import java.sql.ResultSet;
import java.sql.SQLException;
import java.sql.Timestamp;
import java.time.Instant;
import java.util.UUID;

@Repository
public class BalanceJdbcRepository implements BalanceRepository {

    private final NamedParameterJdbcTemplate jdbc;

    public BalanceJdbcRepository(NamedParameterJdbcTemplate jdbc) {
        this.jdbc = jdbc;
    }

    @Override
    public Balance getByWalletId(UUID walletId) {
        String sql = """
                SELECT b.balance, b.last_applied_at,
                       (SELECT max(e.created_at) FROM ledger_db.ledger_entries e WHERE e.account_id = :walletId) AS latest_entry_at,
                       now() AS db_now
                  FROM (VALUES (CAST(:walletId AS uuid))) AS w(wallet_id)
                  LEFT JOIN projection_db.wallet_balances b ON b.wallet_id = w.wallet_id
                """;

        MapSqlParameterSource params = new MapSqlParameterSource();
        params.addValue("walletId", walletId);

        return jdbc.queryForObject(sql, params, BalanceJdbcRepository::mapRow);
    }

    private static Balance mapRow(ResultSet rs, int rowNum) throws SQLException {
        return new Balance(
                rs.getBigDecimal("balance"),
                toInstant(rs.getTimestamp("last_applied_at")),
                toInstant(rs.getTimestamp("latest_entry_at")),
                toInstant(rs.getTimestamp("db_now"))
        );
    }

    private static Instant toInstant(Timestamp timestamp) {
        return timestamp != null ? timestamp.toInstant() : null;
    }
}
