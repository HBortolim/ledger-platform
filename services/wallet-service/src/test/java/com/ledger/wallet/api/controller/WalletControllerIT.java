package com.ledger.wallet.api.controller;

import com.ledger.wallet.support.BaseIntegrationTest;
import com.ledger.wallet.support.JwtTestHelper;
import org.junit.jupiter.api.Test;
import org.springframework.http.MediaType;

import java.util.UUID;

import static org.hamcrest.Matchers.matchesPattern;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.get;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.post;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.*;

class WalletControllerIT extends BaseIntegrationTest {

    private static final String BRL_BODY = "{\"currency\": \"BRL\"}";
    private static final String USD_BODY = "{\"currency\": \"USD\"}";
    private static final String BLANK_CURRENCY_BODY = "{\"currency\": \"\"}";

    // AC-1.1: valid request returns 201 with wallet ID and Location header
    @Test
    void createWallet_happyPath_returns201WithLocationHeader() throws Exception {
        UUID userId = UUID.randomUUID();

        mockMvc.perform(post("/wallets")
                        .header("Authorization", "Bearer " + JwtTestHelper.tokenFor(userId))
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(BRL_BODY))
                .andExpect(status().isCreated())
                .andExpect(header().string("Location", matchesPattern("/wallets/[a-f0-9\\-]+")))
                .andExpect(jsonPath("$.walletId").exists())
                .andExpect(jsonPath("$.currency").value("BRL"))
                .andExpect(jsonPath("$.status").value("ACTIVE"))
                .andExpect(jsonPath("$.createdAt").exists());
    }

    // AC-1.2: request without a valid JWT returns 401
    @Test
    void createWallet_withoutJwt_returns401() throws Exception {
        mockMvc.perform(post("/wallets")
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(BRL_BODY))
                .andExpect(status().isUnauthorized());
    }

    // AC-1.2: invalid JWT returns 401
    @Test
    void createWallet_withInvalidJwt_returns401() throws Exception {
        mockMvc.perform(post("/wallets")
                        .header("Authorization", "Bearer not.a.valid.token")
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(BRL_BODY))
                .andExpect(status().isUnauthorized());
    }

    // AC-1.4: currency != "BRL" returns 422 with code UNSUPPORTED_CURRENCY
    @Test
    void createWallet_withUnsupportedCurrency_returns422() throws Exception {
        UUID userId = UUID.randomUUID();

        mockMvc.perform(post("/wallets")
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

        mockMvc.perform(post("/wallets")
                        .header("Authorization", "Bearer " + JwtTestHelper.tokenFor(userId))
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(BLANK_CURRENCY_BODY))
                .andExpect(status().isBadRequest());
    }

    // AC-1.5: created wallet has status ACTIVE
    @Test
    void createWallet_createsWalletWithActiveStatus() throws Exception {
        UUID userId = UUID.randomUUID();

        mockMvc.perform(post("/wallets")
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

        mockMvc.perform(post("/wallets")
                        .header("Authorization", "Bearer " + token)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(BRL_BODY))
                .andExpect(status().isCreated());

        mockMvc.perform(post("/wallets")
                        .header("Authorization", "Bearer " + token)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(BRL_BODY))
                .andExpect(status().isCreated());
    }

    @Test
    void getWallet_existingWallet_returns200WithData() throws Exception {
        UUID userId = UUID.randomUUID();
        String token = JwtTestHelper.tokenFor(userId);

        String responseBody = mockMvc.perform(post("/wallets")
                        .header("Authorization", "Bearer " + token)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(BRL_BODY))
                .andExpect(status().isCreated())
                .andReturn().getResponse().getContentAsString();

        UUID walletId = UUID.fromString(objectMapper.readTree(responseBody).get("walletId").asText());

        mockMvc.perform(get("/wallets/{id}", walletId)
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

        mockMvc.perform(get("/wallets/{id}", UUID.randomUUID())
                        .header("Authorization", "Bearer " + JwtTestHelper.tokenFor(userId)))
                .andExpect(status().isNotFound());
    }

    // IDOR prevention: wallet belonging to another user returns 404, not 403
    @Test
    void getWallet_differentOwner_returns404() throws Exception {
        UUID owner = UUID.randomUUID();
        UUID attacker = UUID.randomUUID();

        String responseBody = mockMvc.perform(post("/wallets")
                        .header("Authorization", "Bearer " + JwtTestHelper.tokenFor(owner))
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(BRL_BODY))
                .andReturn().getResponse().getContentAsString();

        UUID walletId = UUID.fromString(objectMapper.readTree(responseBody).get("walletId").asText());

        mockMvc.perform(get("/wallets/{id}", walletId)
                        .header("Authorization", "Bearer " + JwtTestHelper.tokenFor(attacker)))
                .andExpect(status().isNotFound());
    }

    @Test
    void getWallet_withoutJwt_returns401() throws Exception {
        mockMvc.perform(get("/wallets/{id}", UUID.randomUUID()))
                .andExpect(status().isUnauthorized());
    }
}
