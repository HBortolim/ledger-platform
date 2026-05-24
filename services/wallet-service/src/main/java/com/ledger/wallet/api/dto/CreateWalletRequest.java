package com.ledger.wallet.api.dto;

import jakarta.validation.constraints.NotBlank;
import jakarta.validation.constraints.NotNull;
import java.util.UUID;

public record CreateWalletRequest(
        @NotNull UUID ownerId,
        @NotBlank String currency
) {}
