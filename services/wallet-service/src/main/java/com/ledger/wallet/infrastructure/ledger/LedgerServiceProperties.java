package com.ledger.wallet.infrastructure.ledger;

import org.springframework.boot.context.properties.ConfigurationProperties;

@ConfigurationProperties(prefix = "ledger.service")
public record LedgerServiceProperties(String url) {}
