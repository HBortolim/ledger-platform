package com.ledger.wallet.application.idempotency;

import org.springframework.boot.context.properties.ConfigurationProperties;

@ConfigurationProperties(prefix = "ledger.idempotency")
public record IdempotencyProperties(long ttlHours, long pendingSweepSeconds) {}
