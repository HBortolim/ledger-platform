package com.ledger.wallet.domain.model;

import java.math.BigDecimal;
import java.time.Instant;

public record Balance(BigDecimal balance, Instant lastAppliedAt, Instant latestEntryAt, Instant dbNow) {
}
