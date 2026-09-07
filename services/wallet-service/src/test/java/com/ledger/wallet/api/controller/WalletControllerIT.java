package com.ledger.wallet.api.controller;

import com.ledger.wallet.support.BaseIntegrationTest;
import com.ledger.wallet.support.JwtTestHelper;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.http.MediaType;
import org.springframework.jdbc.core.namedparam.MapSqlParameterSource;
import org.springframework.jdbc.core.namedparam.NamedParameterJdbcTemplate;

import java.math.BigDecimal;
import java.sql.Connection;
import java.sql.DriverManager;
import java.sql.PreparedStatement;
import java.sql.Statement;
import java.time.Instant;
import java.time.ZoneOffset;
import java.util.Map;
import java.util.UUID;

import static org.assertj.core.api.Assertions.assertThat;
import static org.hamcrest.Matchers.matchesPattern;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.get;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.post;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.*;

class WalletControllerIT extends BaseIntegrationTest {

    private static final String BRL_BODY = "{\"currency\": \"BRL\"}";
    private static final String USD_BODY = "{\"currency\": \"USD\"}";
    private static final String BLANK_CURRENCY_BODY = "{\"currency\": \"\"}";

    private static final String WALLETS = "/wallets";
    private static final String WALLET_BY_ID = "/wallets/{id}";
    private static final String FREEZE = "/wallets/{id}/freeze";
    private static final String UNFREEZE = "/wallets/{id}/unfreeze";
    private static final String CLOSE = "/wallets/{id}/close";

    @Autowired
    private NamedParameterJdbcTemplate jdbcTemplate;

    // AC-1.1: valid request returns 201 with wallet ID and Location header
    @Test
    void createWallet_happyPath_returns201WithLocationHeader() throws Exception {
        UUID userId = UUID.randomUUID();

        mockMvc.perform(post(WALLETS)
                        .header("Authorization", "Bearer " + JwtTestHelper.tokenFor(userId))
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(BRL_BODY))
                .andExpect(status().isCreated())
                .andExpect(header().string("Location", matchesPattern(WALLETS + "/[a-f0-9\\-]+")))
                .andExpect(jsonPath("$.walletId").exists())
                .andExpect(jsonPath("$.currency").value("BRL"))
                .andExpect(jsonPath("$.status").value("ACTIVE"))
                .andExpect(jsonPath("$.createdAt").exists());
    }

    // AC-1.2: request without a valid JWT returns 401
    @Test
    void createWallet_withoutJwt_returns401() throws Exception {
        mockMvc.perform(post(WALLETS)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(BRL_BODY))
                .andExpect(status().isUnauthorized());
    }

    // AC-1.2: invalid JWT returns 401
    @Test
    void createWallet_withInvalidJwt_returns401() throws Exception {
        mockMvc.perform(post(WALLETS)
                        .header("Authorization", "Bearer not.a.valid.token")
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(BRL_BODY))
                .andExpect(status().isUnauthorized());
    }

    // AC-1.4: currency != "BRL" returns 422 with code UNSUPPORTED_CURRENCY
    @Test
    void createWallet_withUnsupportedCurrency_returns422() throws Exception {
        UUID userId = UUID.randomUUID();

        mockMvc.perform(post(WALLETS)
                        .header("Authorization", "Bearer " + JwtTestHelper.tokenFor(userId))
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(USD_BODY))
                .andExpect(status().isUnprocessableContent())
                .andExpect(jsonPath("$.errors[0].code").value("UNSUPPORTED_CURRENCY"));
    }

    // AC-1.4: blank currency fails bean validation before reaching domain
    @Test
    void createWallet_withBlankCurrency_returns400() throws Exception {
        UUID userId = UUID.randomUUID();

        mockMvc.perform(post(WALLETS)
                        .header("Authorization", "Bearer " + JwtTestHelper.tokenFor(userId))
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(BLANK_CURRENCY_BODY))
                .andExpect(status().isBadRequest());
    }

    // AC-1.5: created wallet has status ACTIVE
    @Test
    void createWallet_createsWalletWithActiveStatus() throws Exception {
        UUID userId = UUID.randomUUID();

        mockMvc.perform(post(WALLETS)
                        .header("Authorization", "Bearer " + JwtTestHelper.tokenFor(userId))
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(BRL_BODY))
                .andExpect(status().isCreated())
                .andExpect(jsonPath("$.status").value("ACTIVE"));
    }

    // AC-1.6: same user may hold multiple wallets
    @Test
    void createWallet_sameUser_canCreateMultipleWallets() throws Exception {
        UUID userId = UUID.randomUUID();
        String token = JwtTestHelper.tokenFor(userId);

        mockMvc.perform(post(WALLETS)
                        .header("Authorization", "Bearer " + token)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(BRL_BODY))
                .andExpect(status().isCreated());

        mockMvc.perform(post(WALLETS)
                        .header("Authorization", "Bearer " + token)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(BRL_BODY))
                .andExpect(status().isCreated());
    }

    @Test
    void getWallet_existingWallet_returns200WithData() throws Exception {
        UUID userId = UUID.randomUUID();
        String token = JwtTestHelper.tokenFor(userId);

        String responseBody = mockMvc.perform(post(WALLETS)
                        .header("Authorization", "Bearer " + token)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(BRL_BODY))
                .andExpect(status().isCreated())
                .andReturn().getResponse().getContentAsString();

        UUID walletId = UUID.fromString(objectMapper.readTree(responseBody).get("walletId").asText());

        mockMvc.perform(get(WALLET_BY_ID, walletId)
                        .header("Authorization", "Bearer " + token))
                .andExpect(status().isOk())
                .andExpect(jsonPath("$.walletId").value(walletId.toString()))
                .andExpect(jsonPath("$.currency").value("BRL"))
                .andExpect(jsonPath("$.status").value("ACTIVE"))
                .andExpect(jsonPath("$.createdAt").exists());
    }

    @Test
    void getWallet_notFound_returns404() throws Exception {
        UUID userId = UUID.randomUUID();

        mockMvc.perform(get(WALLET_BY_ID, UUID.randomUUID())
                        .header("Authorization", "Bearer " + JwtTestHelper.tokenFor(userId)))
                .andExpect(status().isNotFound());
    }

    // IDOR prevention: wallet belonging to another user returns 404, not 403
    @Test
    void getWallet_differentOwner_returns404() throws Exception {
        UUID owner = UUID.randomUUID();
        UUID attacker = UUID.randomUUID();

        String responseBody = mockMvc.perform(post(WALLETS)
                        .header("Authorization", "Bearer " + JwtTestHelper.tokenFor(owner))
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(BRL_BODY))
                .andReturn().getResponse().getContentAsString();

        UUID walletId = UUID.fromString(objectMapper.readTree(responseBody).get("walletId").asText());

        mockMvc.perform(get(WALLET_BY_ID, walletId)
                        .header("Authorization", "Bearer " + JwtTestHelper.tokenFor(attacker)))
                .andExpect(status().isNotFound());
    }

    @Test
    void getWallet_withoutJwt_returns401() throws Exception {
        mockMvc.perform(get(WALLET_BY_ID, UUID.randomUUID()))
                .andExpect(status().isUnauthorized());
    }

    // FR-4's close check reads ledger_db.ledger_entries, which wallet-service's own Flyway does
    // not own. Same "cross-schema fixtures" contract BalanceJdbcRepositoryIT uses: create the
    // minimal ledger schema and the wallet_app read grant with the container's owner
    // credentials. Authoritative DDL lives in ledger-service/migrations/0001_ledger_schema.up.sql
    // and the grant in its 0004_wallet_read_grants.up.sql.
    @BeforeAll
    static void createCrossSchemaFixtures() throws Exception {
        try (Connection conn = ownerConnection();
             Statement stmt = conn.createStatement()) {
            stmt.execute("""
                    DO $$
                    BEGIN
                      IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'wallet_app') THEN
                        CREATE ROLE wallet_app LOGIN PASSWORD 'wallet_app';
                      END IF;
                    END
                    $$
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
            stmt.execute("GRANT USAGE ON SCHEMA ledger_db TO wallet_app");
            stmt.execute("GRANT SELECT ON ledger_db.ledger_entries TO wallet_app");
        }
    }

    private static Connection ownerConnection() throws Exception {
        return DriverManager.getConnection(postgres.getJdbcUrl(), postgres.getUsername(), postgres.getPassword());
    }

    // Committed outside the test's transaction, on purpose: the use case reads the ledger over
    // its own connection. Wallet ids are random, so rows never collide across tests.
    private void insertLedgerEntry(UUID accountId, String entryType, String amount) throws Exception {
        try (Connection conn = ownerConnection();
             PreparedStatement ps = conn.prepareStatement("""
                     INSERT INTO ledger_db.ledger_entries (id, transaction_id, account_id, entry_type, amount, created_at)
                     VALUES (?, ?, ?, ?, ?, ?)
                     """)) {
            ps.setObject(1, UUID.randomUUID());
            ps.setObject(2, UUID.randomUUID());
            ps.setObject(3, accountId);
            ps.setString(4, entryType);
            ps.setBigDecimal(5, new BigDecimal(amount));
            ps.setObject(6, Instant.now().atOffset(ZoneOffset.UTC));
            ps.executeUpdate();
        }
    }

    private UUID createWallet(String token) throws Exception {
        String body = mockMvc.perform(post(WALLETS)
                        .header("Authorization", "Bearer " + token)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(BRL_BODY))
                .andExpect(status().isCreated())
                .andReturn().getResponse().getContentAsString();
        return UUID.fromString(objectMapper.readTree(body).get("walletId").asText());
    }

    private long auditRowCount(UUID walletId, String action) {
        return jdbcTemplate.queryForObject("""
                SELECT COUNT(*)
                  FROM wallet_db.wallet_audit_log
                 WHERE wallet_id = :walletId AND action = :action
                """, new MapSqlParameterSource()
                .addValue("walletId", walletId)
                .addValue("action", action), Long.class);
    }

    private String walletStatus(UUID walletId, String token) throws Exception {
        String body = mockMvc.perform(get(WALLET_BY_ID, walletId)
                        .header("Authorization", "Bearer " + token))
                .andExpect(status().isOk())
                .andReturn().getResponse().getContentAsString();
        return objectMapper.readTree(body).get("status").asText();
    }

    // ---- FR-3: freeze / unfreeze ----

    // AC-3.1
    @Test
    void freeze_activeWallet_returns200AndFrozenStatus() throws Exception {
        UUID userId = UUID.randomUUID();
        String token = JwtTestHelper.tokenFor(userId);
        UUID walletId = createWallet(token);

        mockMvc.perform(post(FREEZE, walletId)
                        .header("Authorization", "Bearer " + token))
                .andExpect(status().isOk())
                .andExpect(jsonPath("$.walletId").value(walletId.toString()))
                .andExpect(jsonPath("$.status").value("FROZEN"));

        assertThat(walletStatus(walletId, token)).isEqualTo("FROZEN");
    }

    // AC-3.2 + Task 01 BR-A3: idempotent, and the second call adds no audit row.
    @Test
    void freeze_twice_returns200BothTimesAndWritesOneAuditRow() throws Exception {
        UUID userId = UUID.randomUUID();
        String token = JwtTestHelper.tokenFor(userId);
        UUID walletId = createWallet(token);

        mockMvc.perform(post(FREEZE, walletId)
                        .header("Authorization", "Bearer " + token))
                .andExpect(status().isOk());
        mockMvc.perform(post(FREEZE, walletId)
                        .header("Authorization", "Bearer " + token))
                .andExpect(status().isOk())
                .andExpect(jsonPath("$.status").value("FROZEN"));

        assertThat(auditRowCount(walletId, "FREEZE")).isEqualTo(1);
    }

    // AC-3.5 / NFR-AUDIT-3: the audit row records the actor and both states, end to end.
    @Test
    void freeze_writesAuditRowWithActorAndBothStates() throws Exception {
        UUID userId = UUID.randomUUID();
        String token = JwtTestHelper.tokenFor(userId);
        UUID walletId = createWallet(token);

        mockMvc.perform(post(FREEZE, walletId)
                        .header("Authorization", "Bearer " + token))
                .andExpect(status().isOk());

        Map<String, Object> row = jdbcTemplate.queryForMap("""
                SELECT actor_id,
                       before_state->>'status' AS before_status,
                       after_state->>'status'  AS after_status,
                       at
                  FROM wallet_db.wallet_audit_log
                 WHERE wallet_id = :walletId AND action = 'FREEZE'
                """, new MapSqlParameterSource().addValue("walletId", walletId));

        assertThat(row.get("actor_id")).isEqualTo(userId);
        assertThat(row.get("before_status")).isEqualTo("ACTIVE");
        assertThat(row.get("after_status")).isEqualTo("FROZEN");
        assertThat(row.get("at")).isNotNull();
    }

    @Test
    void unfreeze_frozenWallet_returnsActive() throws Exception {
        UUID userId = UUID.randomUUID();
        String token = JwtTestHelper.tokenFor(userId);
        UUID walletId = createWallet(token);

        mockMvc.perform(post(FREEZE, walletId)
                        .header("Authorization", "Bearer " + token))
                .andExpect(status().isOk());
        mockMvc.perform(post(UNFREEZE, walletId)
                        .header("Authorization", "Bearer " + token))
                .andExpect(status().isOk())
                .andExpect(jsonPath("$.status").value("ACTIVE"));

        assertThat(auditRowCount(walletId, "UNFREEZE")).isEqualTo(1);
    }

    // AC-3.3
    @Test
    void freeze_closedWallet_returns409WalletClosed() throws Exception {
        UUID userId = UUID.randomUUID();
        String token = JwtTestHelper.tokenFor(userId);
        UUID walletId = createWallet(token);

        mockMvc.perform(post(CLOSE, walletId)
                        .header("Authorization", "Bearer " + token))
                .andExpect(status().isOk());

        mockMvc.perform(post(FREEZE, walletId)
                        .header("Authorization", "Bearer " + token))
                .andExpect(status().isConflict())
                .andExpect(jsonPath("$.code").value("WALLET_CLOSED"));
    }

    // ---- FR-4: close ----

    // AC-4.1: the balance is read from the ledger, so a funded wallet cannot be closed.
    @Test
    void close_walletWithNonZeroLedgerBalance_returns409NonzeroBalance() throws Exception {
        UUID userId = UUID.randomUUID();
        String token = JwtTestHelper.tokenFor(userId);
        UUID walletId = createWallet(token);
        insertLedgerEntry(walletId, "CREDIT", "100.00");

        mockMvc.perform(post(CLOSE, walletId)
                        .header("Authorization", "Bearer " + token))
                .andExpect(status().isConflict())
                .andExpect(jsonPath("$.code").value("NONZERO_BALANCE"));

        assertThat(walletStatus(walletId, token)).isEqualTo("ACTIVE");
        assertThat(auditRowCount(walletId, "CLOSE")).isZero();
    }

    @Test
    void close_fundedThenDrainedWallet_returns200AndClosed() throws Exception {
        UUID userId = UUID.randomUUID();
        String token = JwtTestHelper.tokenFor(userId);
        UUID walletId = createWallet(token);
        insertLedgerEntry(walletId, "CREDIT", "40.00");
        insertLedgerEntry(walletId, "DEBIT", "40.00");

        mockMvc.perform(post(CLOSE, walletId)
                        .header("Authorization", "Bearer " + token))
                .andExpect(status().isOk())
                .andExpect(jsonPath("$.status").value("CLOSED"));

        assertThat(auditRowCount(walletId, "CLOSE")).isEqualTo(1);
    }

    // AC-4.2: closed is terminal, and re-closing is an idempotent 200 rather than a conflict.
    @Test
    void close_alreadyClosed_returns200AndWritesNoSecondAuditRow() throws Exception {
        UUID userId = UUID.randomUUID();
        String token = JwtTestHelper.tokenFor(userId);
        UUID walletId = createWallet(token);

        mockMvc.perform(post(CLOSE, walletId)
                        .header("Authorization", "Bearer " + token))
                .andExpect(status().isOk());
        mockMvc.perform(post(CLOSE, walletId)
                        .header("Authorization", "Bearer " + token))
                .andExpect(status().isOk())
                .andExpect(jsonPath("$.status").value("CLOSED"));

        assertThat(auditRowCount(walletId, "CLOSE")).isEqualTo(1);
    }

    @Test
    void unfreeze_closedWallet_returns409WalletClosed() throws Exception {
        UUID userId = UUID.randomUUID();
        String token = JwtTestHelper.tokenFor(userId);
        UUID walletId = createWallet(token);

        mockMvc.perform(post(CLOSE, walletId)
                        .header("Authorization", "Bearer " + token))
                .andExpect(status().isOk());

        mockMvc.perform(post(UNFREEZE, walletId)
                        .header("Authorization", "Bearer " + token))
                .andExpect(status().isConflict())
                .andExpect(jsonPath("$.code").value("WALLET_CLOSED"));
    }

    // AC-4.3: history outlives the wallet -- closing deletes nothing.
    @Test
    void close_leavesLedgerEntriesQueryable() throws Exception {
        UUID userId = UUID.randomUUID();
        String token = JwtTestHelper.tokenFor(userId);
        UUID walletId = createWallet(token);
        insertLedgerEntry(walletId, "CREDIT", "12.00");
        insertLedgerEntry(walletId, "DEBIT", "12.00");

        mockMvc.perform(post(CLOSE, walletId)
                        .header("Authorization", "Bearer " + token))
                .andExpect(status().isOk());

        try (Connection conn = ownerConnection();
             PreparedStatement ps = conn.prepareStatement(
                     "SELECT COUNT(*) FROM ledger_db.ledger_entries WHERE account_id = ?")) {
            ps.setObject(1, walletId);
            var rs = ps.executeQuery();
            rs.next();
            assertThat(rs.getLong(1)).isEqualTo(2);
        }
    }

    // ---- authorization on the lifecycle endpoints ----

    // AC-2.2 / AC-5.11: another user's wallet is indistinguishable from a missing one.
    @Test
    void freeze_differentOwner_returns403() throws Exception {
        UUID owner = UUID.randomUUID();
        UUID attacker = UUID.randomUUID();
        UUID walletId = createWallet(JwtTestHelper.tokenFor(owner));

        mockMvc.perform(post(FREEZE, walletId)
                        .header("Authorization", "Bearer " + JwtTestHelper.tokenFor(attacker)))
                .andExpect(status().isForbidden());

        assertThat(walletStatus(walletId, JwtTestHelper.tokenFor(owner))).isEqualTo("ACTIVE");
    }

    @Test
    void freeze_unknownWallet_returns403LikeAForeignWallet() throws Exception {
        mockMvc.perform(post(FREEZE, UUID.randomUUID())
                        .header("Authorization", "Bearer " + JwtTestHelper.tokenFor(UUID.randomUUID())))
                .andExpect(status().isForbidden());
    }

    @Test
    void lifecycleEndpoints_withoutJwt_return401() throws Exception {
        UUID walletId = UUID.randomUUID();

        mockMvc.perform(post(FREEZE, walletId)).andExpect(status().isUnauthorized());
        mockMvc.perform(post(UNFREEZE, walletId)).andExpect(status().isUnauthorized());
        mockMvc.perform(post(CLOSE, walletId)).andExpect(status().isUnauthorized());
    }
}
