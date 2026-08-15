package com.ledger.wallet.infrastructure.persistence;

import com.ledger.wallet.application.usecase.BalanceRepository;
import com.ledger.wallet.domain.model.Balance;
import com.ledger.wallet.support.BaseIntegrationTest;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;

import java.math.BigDecimal;
import java.sql.Connection;
import java.sql.DriverManager;
import java.sql.PreparedStatement;
import java.sql.Statement;
import java.time.Instant;
import java.time.ZoneOffset;
import java.time.temporal.ChronoUnit;
import java.util.UUID;

import static org.assertj.core.api.Assertions.assertThat;

// Exercises the real cross-schema LEFT JOIN query against a live Postgres instance -- this is
// the query itself (VALUES + LEFT JOIN, §06 step 5) that a mocked unit test can't validate.
// wallet-service's own Flyway only owns wallet_db, so projection_db/ledger_db and the wallet_app
// read grants -- normally created by projection-service's 0003 and ledger-service's 0004
// migrations -- are set up here directly with the container's owner credentials, per the
// "cross-schema fixtures" contract documented on BaseIntegrationTest.postgres.
class BalanceJdbcRepositoryIT extends BaseIntegrationTest {

    @Autowired
    private BalanceRepository repository;

    @BeforeAll
    static void createCrossSchemaFixtures() throws Exception {
        try (Connection conn = ownerConnection();
             Statement stmt = conn.createStatement()) {
            // wallet-service's own Flyway (V2__wallet_grants.sql) also creates this role, but
            // it runs lazily on Spring context startup -- not guaranteed to happen before this
            // @BeforeAll. Same guarded pattern as the real grant migrations, so it's a no-op
            // once Flyway gets there either way.
            stmt.execute("""
                    DO $$
                    BEGIN
                      IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'wallet_app') THEN
                        CREATE ROLE wallet_app LOGIN PASSWORD 'wallet_app';
                      END IF;
                    END
                    $$
                    """);
            stmt.execute("CREATE SCHEMA IF NOT EXISTS projection_db");
            stmt.execute("""
                    CREATE TABLE IF NOT EXISTS projection_db.wallet_balances (
                        wallet_id       UUID PRIMARY KEY,
                        balance         NUMERIC(19, 2) NOT NULL DEFAULT 0,
                        last_entry_id   UUID,
                        last_applied_at TIMESTAMPTZ NOT NULL DEFAULT now(),
                        updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
                    )
                    """);
            stmt.execute("CREATE SCHEMA IF NOT EXISTS ledger_db");
            stmt.execute("""
                    CREATE TABLE IF NOT EXISTS ledger_db.ledger_entries (
                        id              UUID PRIMARY KEY,
                        transaction_id  UUID NOT NULL,
                        account_id      UUID NOT NULL,
                        entry_type      VARCHAR(8) NOT NULL,
                        amount          NUMERIC(19, 2) NOT NULL,
                        created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
                    )
                    """);
            stmt.execute("GRANT USAGE ON SCHEMA projection_db TO wallet_app");
            stmt.execute("GRANT SELECT ON projection_db.wallet_balances TO wallet_app");
            stmt.execute("GRANT USAGE ON SCHEMA ledger_db TO wallet_app");
            stmt.execute("GRANT SELECT ON ledger_db.ledger_entries TO wallet_app");
        }
    }

    // No per-test cleanup: each test uses a fresh random walletId/accountId, so rows never
    // collide across tests. (A TRUNCATE here would also deadlock -- it needs an exclusive lock,
    // which blocks on the still-open @Transactional connection from the test body until after
    // this callback returns, which never happens.)
    private static Connection ownerConnection() throws Exception {
        return DriverManager.getConnection(postgres.getJdbcUrl(), postgres.getUsername(), postgres.getPassword());
    }

    private void insertWalletBalance(UUID walletId, String balance, Instant lastAppliedAt) throws Exception {
        try (Connection conn = ownerConnection();
             PreparedStatement ps = conn.prepareStatement(
                     "INSERT INTO projection_db.wallet_balances (wallet_id, balance, last_applied_at) VALUES (?, ?, ?)")) {
            ps.setObject(1, walletId);
            ps.setBigDecimal(2, new BigDecimal(balance));
            ps.setObject(3, lastAppliedAt.atOffset(ZoneOffset.UTC));
            ps.executeUpdate();
        }
    }

    private void insertLedgerEntry(UUID accountId, Instant createdAt) throws Exception {
        try (Connection conn = ownerConnection();
             PreparedStatement ps = conn.prepareStatement("""
                     INSERT INTO ledger_db.ledger_entries (id, transaction_id, account_id, entry_type, amount, created_at)
                     VALUES (?, ?, ?, 'CREDIT', 10.00, ?)
                     """)) {
            ps.setObject(1, UUID.randomUUID());
            ps.setObject(2, UUID.randomUUID());
            ps.setObject(3, accountId);
            ps.setObject(4, createdAt.atOffset(ZoneOffset.UTC));
            ps.executeUpdate();
        }
    }

    @Test
    void projectionRowExists_returnsBalanceAndLastAppliedAtAndLatestEntryAt() throws Exception {
        UUID walletId = UUID.randomUUID();
        Instant lastAppliedAt = Instant.now().minus(1, ChronoUnit.HOURS).truncatedTo(ChronoUnit.MILLIS);
        insertWalletBalance(walletId, "250.00", lastAppliedAt);
        insertLedgerEntry(walletId, lastAppliedAt);

        Balance balance = repository.getByWalletId(walletId);

        assertThat(balance.balance()).isEqualByComparingTo("250.00");
        assertThat(balance.lastAppliedAt()).isEqualTo(lastAppliedAt);
        assertThat(balance.latestEntryAt()).isEqualTo(lastAppliedAt);
        assertThat(balance.dbNow()).isAfterOrEqualTo(lastAppliedAt);
    }

    @Test
    void noProjectionRow_neverHadEntries_returnsAllNullExceptDbNow() {
        UUID walletId = UUID.randomUUID();

        Balance balance = repository.getByWalletId(walletId);

        assertThat(balance.balance()).isNull();
        assertThat(balance.lastAppliedAt()).isNull();
        assertThat(balance.latestEntryAt()).isNull();
        assertThat(balance.dbNow()).isNotNull();
    }

    // The scenario the plain inner-join query used to miss entirely (see 06-balance-endpoint.md
    // step 5): entries exist in ledger_db but the projection consumer never wrote a row.
    @Test
    void noProjectionRow_entriesExistButConsumerNeverCaughtUp_stillSurfacesLatestEntryAt() throws Exception {
        UUID walletId = UUID.randomUUID();
        Instant entryAt = Instant.now().minusSeconds(20).truncatedTo(ChronoUnit.MILLIS);
        insertLedgerEntry(walletId, entryAt);

        Balance balance = repository.getByWalletId(walletId);

        assertThat(balance.balance()).isNull();
        assertThat(balance.lastAppliedAt()).isNull();
        assertThat(balance.latestEntryAt()).isEqualTo(entryAt);
    }

    @Test
    void multipleEntries_latestEntryAtPicksMostRecent() throws Exception {
        UUID walletId = UUID.randomUUID();
        Instant older = Instant.now().minusSeconds(60).truncatedTo(ChronoUnit.MILLIS);
        Instant newer = Instant.now().minusSeconds(5).truncatedTo(ChronoUnit.MILLIS);
        insertLedgerEntry(walletId, older);
        insertLedgerEntry(walletId, newer);

        Balance balance = repository.getByWalletId(walletId);

        assertThat(balance.latestEntryAt()).isEqualTo(newer);
    }

    @Test
    void entriesForOtherAccounts_areNotMixedIntoLatestEntryAt() throws Exception {
        UUID walletId = UUID.randomUUID();
        UUID otherAccount = UUID.randomUUID();
        insertLedgerEntry(otherAccount, Instant.now());

        Balance balance = repository.getByWalletId(walletId);

        assertThat(balance.latestEntryAt()).isNull();
    }
}
