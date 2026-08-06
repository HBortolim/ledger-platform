package com.ledger.wallet.api.dto;

import java.util.List;

public record ValidationErrorResponse(List<ErrorResponse> errors) {}
