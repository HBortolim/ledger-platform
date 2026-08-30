package com.ledger.wallet.infrastructure.config;

import io.micrometer.observation.ObservationRegistry;
import org.springframework.boot.micrometer.observation.autoconfigure.ObservationRegistryCustomizer;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.http.server.observation.ServerRequestObservationContext;

@Configuration
public class ObservabilityConfig {

    /**
     * Keeps the Jaeger trace list to traces worth looking at, so the end-to-end transfer trace
     * NFR-OBS-5 is about isn't buried. Mirrors the Go services, which scope their tracing
     * middleware to business routes only (see ledger-service's internal/handler/routes.go).
     *
     * <p>Two sources are dropped. Docker's 10s healthcheck on {@code /health/live} and
     * Prometheus's 15s scrape of {@code /actuator/prometheus} are filtered by path. Spring
     * Security's per-request observations are dropped by name: they fire on those same
     * infrastructure requests, and once their parent HTTP observation is gone they surface as
     * standalone root traces, so filtering by path alone leaves the flood intact.
     *
     * <p>{@code wallet_requests_total} and {@code wallet_request_duration_seconds} are
     * unaffected: MetricsFilter writes straight to the MeterRegistry and never goes through
     * the ObservationRegistry.
     */
    @Bean
    ObservationRegistryCustomizer<ObservationRegistry> untracedInfrastructureEndpoints() {
        return registry -> registry.observationConfig()
                .observationPredicate((name, context) -> {
                    if (name.startsWith("spring.security")) {
                        return false;
                    }
                    if (context instanceof ServerRequestObservationContext http) {
                        String path = http.getCarrier().getRequestURI();
                        return !(path.startsWith("/health") || path.startsWith("/actuator"));
                    }
                    return true;
                });
    }
}
