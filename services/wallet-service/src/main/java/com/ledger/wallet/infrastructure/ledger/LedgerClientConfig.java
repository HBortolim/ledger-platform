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

    /**
     * Built from the auto-configured {@link RestClient.Builder} bean rather than the
     * {@code RestClient.builder()} static factory: only the bean carries Spring's
     * observation instrumentation, which is what injects the W3C {@code traceparent}
     * header on the outbound call. Building from the static factory produces a client
     * that works perfectly but emits no client span and propagates no trace context --
     * splitting what NFR-OBS-5 requires to be one end-to-end trace into two.
     */
    @Bean
    public RestClient ledgerRestClient(RestClient.Builder builder, LedgerServiceProperties props) {
        return builder
                .baseUrl(props.url())
                .requestFactory(jdkRequestFactory(CONNECT_TIMEOUT, READ_TIMEOUT))
                .build();
    }

    /**
     * Package-visible so tests can build a client against a WireMock server with short
     * timeouts, without duplicating the request-factory wiring. Tests do not need -- and
     * should not depend on -- observation instrumentation, so the static factory is correct here.
     */
    static RestClient buildRestClient(String baseUrl, Duration connectTimeout, Duration readTimeout) {
        return RestClient.builder()
                .baseUrl(baseUrl)
                .requestFactory(jdkRequestFactory(connectTimeout, readTimeout))
                .build();
    }

    private static JdkClientHttpRequestFactory jdkRequestFactory(Duration connectTimeout, Duration readTimeout) {
        HttpClient httpClient = HttpClient.newBuilder()
                .connectTimeout(connectTimeout)
                // The Ledger Service (Go/Gin's net/http) speaks plain HTTP/1.1 only.
                // Without pinning this, the JDK client's HTTP/2-upgrade attempt against
                // an HTTP/1.1-only server intermittently resets the connection.
                .version(HttpClient.Version.HTTP_1_1)
                .build();
        JdkClientHttpRequestFactory requestFactory = new JdkClientHttpRequestFactory(httpClient);
        requestFactory.setReadTimeout(readTimeout);
        return requestFactory;
    }
}
