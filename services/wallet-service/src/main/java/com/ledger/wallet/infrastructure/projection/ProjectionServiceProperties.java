package com.ledger.wallet.infrastructure.projection;

import org.springframework.boot.context.properties.ConfigurationProperties;

@ConfigurationProperties(prefix = "projection.service")
public record ProjectionServiceProperties(String url) {
}
