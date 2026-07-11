package com.ledger.wallet.infrastructure.config;

import com.ledger.wallet.support.BaseIntegrationTest;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.jdbc.core.namedparam.NamedParameterJdbcTemplate;

import java.util.Map;

import static org.assertj.core.api.Assertions.assertThat;

// Regression test for spring-boot-starter-flyway actually migrating wallet_db at app startup.
class FlywayMigrationIT extends BaseIntegrationTest {

    @Autowired
    private NamedParameterJdbcTemplate jdbc;

    @Test
    void startup_migratesWalletDbSchema() {
        Integer v1Applied = jdbc.getJdbcTemplate().queryForObject(
                "SELECT count(*) FROM wallet_db.flyway_schema_history "
                        + "WHERE version = '1' AND description = 'wallet schema' AND success = true",
                Integer.class);
        assertThat(v1Applied)
                .withFailMessage("expected migration V1__wallet_schema.sql to be applied and recorded")
                .isEqualTo(1);

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
}
