package com.ledger.wallet.infrastructure.ledger;

import org.springframework.boot.context.properties.ConfigurationProperties;

import java.util.UUID;

@ConfigurationProperties(prefix = "ledger")
public record LedgerServiceProperties(Service service, UUID systemAccountId) {

    public record Service(String url) {}

    public String url() {
        return service.url();
    }
}
