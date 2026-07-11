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

// Regression test for spring-boot-starter-flyway actually migrating wallet_db at app startup.
class FlywayMigrationIT extends BaseIntegrationTest {

    @Autowired
    private NamedParameterJdbcTemplate jdbc;

    @Test
    void startup_migratesWalletDbSchema() {
        // flyway_schema_history is deliberately not queried here: wallet_app (what this
        // connection authenticates as) has no grant on it, only the owner-run Flyway migration does.
        for (String table : new String[] {"wallets", "idempotency_records", "wallet_audit_log"}) {
            Integer found = jdbc.queryForObject(
                    "SELECT count(*) FROM information_schema.tables "
                            + "WHERE table_schema = 'wallet_db' AND table_name = :table",
                    Map.of("table", table), Integer.class);
            assertThat(found)
                    .withFailMessage("expected wallet_db.%s to exist after startup migration", table)
                    .isEqualTo(1);
        }
    }

    @Test
    void walletApp_cannotMutateAuditLog() {
        UUID walletId = UUID.randomUUID();
        jdbc.update(
                "INSERT INTO wallet_db.wallet_audit_log (wallet_id, actor_id, action) "
                        + "VALUES (:walletId, :walletId, 'CREATE')",
                Map.of("walletId", walletId));

        assertThatExceptionOfType(DataAccessException.class)
                .isThrownBy(() -> jdbc.update(
                        "UPDATE wallet_db.wallet_audit_log SET action = 'TAMPERED' WHERE wallet_id = :walletId",
                        Map.of("walletId", walletId)));

        assertThatExceptionOfType(DataAccessException.class)
                .isThrownBy(() -> jdbc.update(
                        "DELETE FROM wallet_db.wallet_audit_log WHERE wallet_id = :walletId",
                        Map.of("walletId", walletId)));
    }
}
