package com.ledger.wallet.infrastructure.persistence;

import com.ledger.wallet.application.usecase.WalletRepository;
import com.ledger.wallet.domain.model.Wallet;
import com.ledger.wallet.domain.model.WalletStatus;
import org.springframework.jdbc.core.namedparam.MapSqlParameterSource;
import org.springframework.jdbc.core.namedparam.NamedParameterJdbcTemplate;
import org.springframework.stereotype.Repository;

import java.sql.ResultSet;
import java.sql.SQLException;
import java.sql.Timestamp;
import java.util.Optional;
import java.util.UUID;

@Repository
public class WalletJdbcRepository implements WalletRepository {

    private final NamedParameterJdbcTemplate jdbc;

    public WalletJdbcRepository(NamedParameterJdbcTemplate jdbc) {
        this.jdbc = jdbc;
    }

    @Override
    public void save(Wallet wallet) {
        String sql = """
                INSERT INTO wallet_db.wallets (id, owner_id, currency, status, created_at, updated_at)
                VALUES (:id, :ownerId, :currency, :status, :lastAppliedAt, :updatedAt)
                """;

        MapSqlParameterSource params = new MapSqlParameterSource()
                .addValue("id", wallet.id())
                .addValue("ownerId", wallet.ownerId())
                .addValue("currency", wallet.currency())
                .addValue("status", wallet.status().name())
                .addValue("lastAppliedAt", Timestamp.from(wallet.createdAt()))
                .addValue("updatedAt", Timestamp.from(wallet.updatedAt()));

        jdbc.update(sql, params);
    }

    @Override
    public Optional<Wallet> getById(UUID walletId, UUID ownerId) {
        String sql = """
                SELECT id, owner_id, currency, status, created_at, updated_at
                FROM wallet_db.wallets
                WHERE id = :id
                AND owner_id = :ownerId
                """;
        MapSqlParameterSource params = new MapSqlParameterSource()
                .addValue("id", walletId)
                .addValue("ownerId", ownerId);

        return jdbc.query(sql, params, WalletJdbcRepository::mapRow).stream().findFirst();
    }

    @Override
    public Optional<Wallet> getById(UUID walletId) {
        String sql = """
                SELECT id, owner_id, currency, status, created_at, updated_at
                FROM wallet_db.wallets
                WHERE id = :id
                """;
        MapSqlParameterSource params = new MapSqlParameterSource().addValue("id", walletId);

        return jdbc.query(sql, params, WalletJdbcRepository::mapRow).stream().findFirst();
    }

    private static Wallet mapRow(ResultSet rs, int rowNum) throws SQLException {
        return new Wallet(
                rs.getObject("id", UUID.class),
                rs.getObject("owner_id", UUID.class),
                rs.getString("currency"),
                WalletStatus.valueOf(rs.getString("status")),
                rs.getTimestamp("created_at").toInstant(),
                rs.getTimestamp("updated_at").toInstant()
        );
    }
}
