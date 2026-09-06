package com.ledger.wallet.infrastructure.persistence;

import com.ledger.wallet.application.usecase.WalletAuditRepository;
import com.ledger.wallet.domain.model.AuditAction;
import com.ledger.wallet.domain.model.WalletStatus;
import org.springframework.jdbc.core.namedparam.MapSqlParameterSource;
import org.springframework.jdbc.core.namedparam.NamedParameterJdbcTemplate;
import org.springframework.stereotype.Repository;

import java.util.UUID;

@Repository
public class WalletAuditJdbcRepository implements WalletAuditRepository {

    private final NamedParameterJdbcTemplate jdbc;

    public WalletAuditJdbcRepository(NamedParameterJdbcTemplate jdbc) {
        this.jdbc = jdbc;
    }

    @Override
    public void recordStatusChange(UUID walletId, UUID actorId, AuditAction action, WalletStatus beforeState, WalletStatus afterState) {
        insert(walletId, actorId, action, statusJson(beforeState), statusJson(afterState));
    }

    @Override
    public void recordAction(UUID walletId, UUID actorId, AuditAction action) {
        insert(walletId, actorId, action, null, null);
    }

    private void insert(UUID walletId, UUID actorId, AuditAction action, String beforeState, String afterState) {
        String sql = """
                INSERT INTO  wallet_db.wallet_audit_log
                    (wallet_id, actor_id, action, before_state, after_state)
                VALUES
                    (:walletId, :actorId, :action, CAST(:beforeState AS jsonb), CAST(:afterState AS jsonb))
                """;

        MapSqlParameterSource params = new MapSqlParameterSource();
        params.addValue("walletId", walletId);
        params.addValue("actorId", actorId);
        params.addValue("action", action.name());
        params.addValue("beforeState", beforeState);
        params.addValue("afterState", afterState);

        jdbc.update(sql, params);
    }

    /** BR-A12: {"status":"ACTIVE"} -- or null, which the CAST above writes as SQL NULL. */
    private static String statusJson(WalletStatus status) {
        return status == null ? null : "{\"status\": \"%s\"}".formatted(status.name());
    }

}
