package com.ledger.wallet.api.dto;

import java.time.Instant;
import java.util.UUID;

public record TransferResponse(
        UUID transferId,
        String status,
        Instant postedAt
) {}
