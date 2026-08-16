// services/wallet-service/src/test/java/com/ledger/wallet/api/filter/MetricsFilterIT.java
package com.ledger.wallet.api.filter;

import com.ledger.wallet.support.BaseIntegrationTest;
import com.ledger.wallet.support.JwtTestHelper;
import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.core.instrument.Timer;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.http.MediaType;

import java.util.UUID;

import static org.assertj.core.api.Assertions.assertThat;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.post;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.status;

class MetricsFilterIT extends BaseIntegrationTest {

    @Autowired
    private MeterRegistry meterRegistry;

    @Test
    void createWallet_recordsRequestCounterAndDurationWithRoutePatternLabel() throws Exception {
        UUID ownerId = UUID.randomUUID();

        mockMvc.perform(post("/wallets")
                        .header("Authorization", "Bearer " + JwtTestHelper.tokenFor(ownerId))
                        .contentType(MediaType.APPLICATION_JSON)
                        .content("{\"currency\":\"BRL\"}"))
                .andExpect(status().isCreated());

        double count = meterRegistry
                .counter("wallet_requests", "endpoint", "/wallets", "method", "POST", "status", "201")
                .count();
        assertThat(count).isGreaterThanOrEqualTo(1.0);

        Timer timer = meterRegistry.find("wallet_request_duration")
                .tag("endpoint", "/wallets")
                .tag("method", "POST")
                .timer();
        assertThat(timer).isNotNull();
        assertThat(timer.count()).isGreaterThanOrEqualTo(1);
    }
}
