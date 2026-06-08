package com.ledger.wallet.infrastructure.persistence;

import com.ledger.wallet.application.usecase.WalletRepository;
import com.ledger.wallet.domain.model.Wallet;
import org.springframework.jdbc.core.namedparam.MapSqlParameterSource;
import org.springframework.jdbc.core.namedparam.NamedParameterJdbcTemplate;
import org.springframework.stereotype.Repository;

import java.sql.Timestamp;

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
                VALUES (:id, :ownerId, :currency, :status, :createdAt, :updatedAt)
                """;

        MapSqlParameterSource params = new MapSqlParameterSource()
                .addValue("id", wallet.id())
                .addValue("ownerId", wallet.ownerId())
                .addValue("currency", wallet.currency())
                .addValue("status", wallet.status().name())
                .addValue("createdAt", Timestamp.from(wallet.createdAt()))
                .addValue("updatedAt", Timestamp.from(wallet.updatedAt()));

        jdbc.update(sql, params);
    }
}
