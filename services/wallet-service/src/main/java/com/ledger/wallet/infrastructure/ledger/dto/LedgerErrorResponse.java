package com.ledger.wallet.infrastructure.ledger.dto;

/** The {@code {code, message}} error body the Ledger Service returns for 422/503/404 (SPEC.md §7.1). */
public record LedgerErrorResponse(String code, String message) {}
