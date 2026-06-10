package com.ledger.wallet.api.dto;

import java.time.Instant;
import java.util.UUID;

public record GetWalletResponse(UUID walletId,
                                String currency,
                                String status,
                                Instant createdAt) {
}
