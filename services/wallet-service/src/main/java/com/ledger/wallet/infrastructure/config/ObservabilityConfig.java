package com.ledger.wallet.infrastructure.config;

import io.micrometer.observation.ObservationRegistry;
import org.springframework.boot.micrometer.observation.autoconfigure.ObservationRegistryCustomizer;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.http.server.observation.ServerRequestObservationContext;

@Configuration
public class ObservabilityConfig {

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
