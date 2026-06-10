package com.ledger.wallet.api.dto;

import jakarta.validation.constraints.NotBlank;

public record CreateWalletRequest(
        @NotBlank String currency
) {}
