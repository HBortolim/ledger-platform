package com.ledger.wallet.infrastructure.ledger;

import org.springframework.boot.context.properties.EnableConfigurationProperties;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.http.client.JdkClientHttpRequestFactory;
import org.springframework.web.client.RestClient;

import java.net.http.HttpClient;
import java.time.Duration;

@Configuration
@EnableConfigurationProperties(LedgerServiceProperties.class)
public class LedgerClientConfig {

    private static final Duration CONNECT_TIMEOUT = Duration.ofSeconds(2);
    private static final Duration READ_TIMEOUT = Duration.ofSeconds(5);

    @Bean
    public RestClient ledgerRestClient(LedgerServiceProperties props) {
        return buildRestClient(props.url(), CONNECT_TIMEOUT, READ_TIMEOUT);
    }

    /**
     * Package-visible so tests can build a client against a WireMock server
     * with short timeouts, without duplicating the request-factory wiring.
     */
     static RestClient buildRestClient(String baseUrl, Duration connectTimeout, Duration readTimeout) {
        HttpClient httpClient = HttpClient.newBuilder()
                .connectTimeout(connectTimeout)
                // The Ledger Service (Go/Gin's net/http) speaks plain HTTP/1.1 only.
                // Without pinning this, the JDK client's HTTP/2-upgrade attempt against
                // an HTTP/1.1-only server intermittently resets the connection.
                .version(HttpClient.Version.HTTP_1_1)
                .build();
        JdkClientHttpRequestFactory requestFactory = new JdkClientHttpRequestFactory(httpClient);
        requestFactory.setReadTimeout(readTimeout);

        return RestClient.builder()
                .baseUrl(baseUrl)
                .requestFactory(requestFactory)
                .build();
    }
}
