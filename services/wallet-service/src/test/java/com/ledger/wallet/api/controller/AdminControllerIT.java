package com.ledger.wallet.api.controller;

import com.github.tomakehurst.wiremock.WireMockServer;
import com.github.tomakehurst.wiremock.http.Fault;
import com.ledger.wallet.support.BaseIntegrationTest;
import com.ledger.wallet.support.JwtTestHelper;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.http.MediaType;
import org.springframework.jdbc.core.namedparam.MapSqlParameterSource;
import org.springframework.jdbc.core.namedparam.NamedParameterJdbcTemplate;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.test.context.DynamicPropertyRegistry;
import org.springframework.test.context.DynamicPropertySource;

import java.util.Map;
import java.util.UUID;

import static com.github.tomakehurst.wiremock.client.WireMock.aResponse;
import static com.github.tomakehurst.wiremock.client.WireMock.urlEqualTo;
import static org.assertj.core.api.Assertions.assertThat;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.post;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.*;

// WireMock stands in for the Projection Service, same pattern TransferControllerIT uses for the
// Ledger Service -- "the Projection Service really rebuilds" is Task 04/07's job against the
// real compose stack. This IT is the Wallet Service's half: auth gate, path parity, degradation.
class AdminControllerIT extends BaseIntegrationTest {

    private static final WireMockServer PROJECTION = new WireMockServer(0);

    static {
        PROJECTION.start();
    }

    @Autowired
    private NamedParameterJdbcTemplate jdbcTemplate;

    @DynamicPropertySource
    static void projectionServiceUrl(DynamicPropertyRegistry registry) {
        registry.add("projection.service.url", PROJECTION::baseUrl);
    }

    @BeforeEach
    void setUp() {
        PROJECTION.resetAll();
    }

    private UUID createWallet(String token) throws Exception {
        String body = mockMvc.perform(post("/wallets")
                        .header("Authorization", "Bearer " + token)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content("{\"currency\":\"BRL\"}"))
                .andExpect(status().isCreated())
                .andReturn().getResponse().getContentAsString();
        return UUID.fromString(objectMapper.readTree(body).get("walletId").asText());
    }

    private void stubUpstreamRebuild(UUID walletId, String balance, int entriesApplied) {
        PROJECTION.stubFor(com.github.tomakehurst.wiremock.client.WireMock.post(urlEqualTo("/admin/projections/" + walletId + "/rebuild"))
                .willReturn(aResponse().withStatus(200).withHeader("Content-Type", "application/json")
                        .withBody("""
                                {
                                  "walletId": "%s",
                                  "balance": "%s",
                                  "entriesApplied": %d,
                                  "rebuiltAt": "2026-05-19T14:23:00.123Z"
                                }
                                """.formatted(walletId, balance, entriesApplied)))
        );
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

    // §9.7 admin-only, happy path, and §9.7's "clear audit event" end to end.
    @Test
    void rebuild_adminToken_returns200AndWritesAuditRow() throws Exception {
        UUID adminId = UUID.randomUUID();
        String adminToken = JwtTestHelper.adminTokenFor(adminId);
        UUID walletId = createWallet(adminToken);
        stubUpstreamRebuild(walletId, "300.00", 2);

        String responseBody = mockMvc.perform(post("/admin/projections/{walletId}/rebuild", walletId)
                        .header("Authorization", "Bearer " + adminToken))
                .andExpect(status().isOk())
                .andExpect(jsonPath("$.walletId").value(walletId.toString()))
                .andExpect(jsonPath("$.entriesApplied").value(2))
                .andReturn().getResponse().getContentAsString();

        // balance is a BigDecimal serialized by Jackson's default (a bare JSON number, same
        // convention BalanceResponse already uses) -- compare numerically via BigDecimal rather
        // than matching a JSON string literal, which this field is not.
        var balance = objectMapper.readTree(responseBody).get("balance").decimalValue();
        assertThat(balance).isEqualByComparingTo("300.00");

        assertThat(auditRowCount(walletId, "PROJECTION_REBUILD")).isEqualTo(1);
    }

    // §9.7 audit event, with both state columns NULL (Task 01 BR-A11): this is not a status
    // transition.
    @Test
    void rebuild_adminToken_auditRowHasNullStates() throws Exception {
        UUID adminId = UUID.randomUUID();
        String adminToken = JwtTestHelper.adminTokenFor(adminId);
        UUID walletId = createWallet(adminToken);
        stubUpstreamRebuild(walletId, "0.00", 0);

        mockMvc.perform(post("/admin/projections/{walletId}/rebuild", walletId)
                        .header("Authorization", "Bearer " + adminToken))
                .andExpect(status().isOk());

        Map<String, Object> row = jdbcTemplate.queryForMap("""
                SELECT actor_id,
                       before_state IS NULL AS before_is_null,
                       after_state  IS NULL AS after_is_null
                  FROM wallet_db.wallet_audit_log
                 WHERE wallet_id = :walletId AND action = 'PROJECTION_REBUILD'
                """, new MapSqlParameterSource().addValue("walletId", walletId));
        assertThat(row.get("actor_id")).isEqualTo(adminId);
        assertThat(row.get("before_is_null")).isEqualTo(Boolean.TRUE);
        assertThat(row.get("after_is_null")).isEqualTo(Boolean.TRUE);
    }

    // §9.7 admin-only: a regular user is refused.
    @Test
    void rebuild_userToken_returns403AndNeverCallsUpstream() throws Exception {
        UUID userId = UUID.randomUUID();
        UUID walletId = createWallet(JwtTestHelper.adminTokenFor(UUID.randomUUID()));
        // No stub registered for this walletId: if the Wallet Service called upstream anyway,
        // WireMock would 404 and the assertion on auditRowCount below would catch it either way.

        mockMvc.perform(post("/admin/projections/{walletId}/rebuild", walletId)
                        .header("Authorization", "Bearer " + JwtTestHelper.tokenFor(userId)))
                .andExpect(status().isForbidden());

        assertThat(auditRowCount(walletId, "PROJECTION_REBUILD")).isZero();
    }

    @Test
    void rebuild_noToken_returns401() throws Exception {
        mockMvc.perform(post("/admin/projections/{walletId}/rebuild", UUID.randomUUID()))
                .andExpect(status().isUnauthorized());
    }

    // §5.2 / Task 04: no wallet on the Wallet Service's own side gates this -- it is the
    // Projection Service that is the source of truth for whether a wallet's projection exists.
    // An unknown wallet here still needs an owner-agnostic lookup in the Wallet Service's own
    // table; since none exists, the admin gate itself is what this test actually isolates.
    @Test
    void rebuild_unknownWallet_returns404AndNeverCallsUpstream() throws Exception {
        String adminToken = JwtTestHelper.adminTokenFor(UUID.randomUUID());
        UUID unknownWalletId = UUID.randomUUID();

        mockMvc.perform(post("/admin/projections/{walletId}/rebuild", unknownWalletId)
                        .header("Authorization", "Bearer " + adminToken))
                .andExpect(status().isNotFound());

        PROJECTION.verify(0, com.github.tomakehurst.wiremock.client.WireMock.postRequestedFor(
                urlEqualTo("/admin/projections/" + unknownWalletId + "/rebuild")));
    }

    // NFR-AVAIL-4: downstream unavailable degrades to 503 + Retry-After, never 500 or a silent
    // 404 -- the exact bug this task's use case had before it was fixed.
    @Test
    void rebuild_projectionServiceDown_returns503WithRetryAfter() throws Exception {
        String adminToken = JwtTestHelper.adminTokenFor(UUID.randomUUID());
        UUID walletId = createWallet(adminToken);
        PROJECTION.stubFor(com.github.tomakehurst.wiremock.client.WireMock.post(urlEqualTo("/admin/projections/" + walletId + "/rebuild"))
                .willReturn(aResponse().withStatus(500)));

        mockMvc.perform(post("/admin/projections/{walletId}/rebuild", walletId)
                        .header("Authorization", "Bearer " + adminToken))
                .andExpect(status().isServiceUnavailable())
                .andExpect(header().string("Retry-After", "2"))
                .andExpect(jsonPath("$.code").value("PROJECTION_UNAVAILABLE"));

        assertThat(auditRowCount(walletId, "PROJECTION_REBUILD")).isZero();
    }

    // Same degradation when the connection itself fails, not just an upstream error status.
    // Deliberately does NOT call PROJECTION.stop(): WireMockServer(0) would rebind to a new
    // random port on the next start(), orphaning the RestClient bean Spring already built with
    // the old base URL for every later test sharing this cached context (TransferControllerIT's
    // ledgerServiceDown_returns503WithRetryAfter hit this first; same fix here).
    @Test
    void rebuild_projectionServiceUnreachable_returns503() throws Exception {
        String adminToken = JwtTestHelper.adminTokenFor(UUID.randomUUID());
        UUID walletId = createWallet(adminToken);
        PROJECTION.stubFor(com.github.tomakehurst.wiremock.client.WireMock.post(urlEqualTo("/admin/projections/" + walletId + "/rebuild"))
                .willReturn(aResponse().withFault(Fault.CONNECTION_RESET_BY_PEER)));

        mockMvc.perform(post("/admin/projections/{walletId}/rebuild", walletId)
                        .header("Authorization", "Bearer " + adminToken))
                .andExpect(status().isServiceUnavailable())
                .andExpect(jsonPath("$.code").value("PROJECTION_UNAVAILABLE"));
    }
}
