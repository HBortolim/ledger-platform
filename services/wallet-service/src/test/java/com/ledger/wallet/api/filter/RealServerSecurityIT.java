package com.ledger.wallet.api.filter;

import com.ledger.wallet.support.BaseIntegrationTest;
import org.junit.jupiter.api.Test;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.resttestclient.TestRestTemplate;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.http.HttpEntity;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpStatus;
import org.springframework.http.MediaType;
import org.springframework.http.ResponseEntity;

import static org.assertj.core.api.Assertions.assertThat;

// Regression test for a bug MockMvc cannot reproduce: sendError() triggers a real servlet
// container forward to /error, which — before this fix — was itself denied by Spring Security
// and overwrote the original 401 with an empty-body 403. MockMvc-based tests elsewhere in this
// suite (e.g. WalletControllerIT.createWallet_withoutJwt_returns401) pass regardless of this bug
// because MockMvc's sendError() never performs a real forward. Only a real embedded server proves
// this.
@SpringBootTest(webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT)
@AutoConfigureTestRestTemplate
class RealServerSecurityIT extends BaseIntegrationTest {

    @Autowired
    private TestRestTemplate restTemplate;

    @Test
    void createWallet_withoutJwt_returns401OverRealHttp() {
        HttpHeaders headers = new HttpHeaders();
        headers.setContentType(MediaType.APPLICATION_JSON);
        HttpEntity<String> request = new HttpEntity<>("{\"currency\":\"BRL\"}", headers);

        ResponseEntity<String> response = restTemplate.postForEntity("/wallets", request, String.class);

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.UNAUTHORIZED);
    }

    @Test
    void actuatorPrometheus_isReachableAndExposesMetricNames() {
        ResponseEntity<String> response = restTemplate.getForEntity("/actuator/prometheus", String.class);

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(response.getBody()).isNotBlank();
    }
}
