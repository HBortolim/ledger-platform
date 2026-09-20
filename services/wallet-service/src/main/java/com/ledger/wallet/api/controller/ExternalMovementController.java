package com.ledger.wallet.api.controller;

import com.ledger.wallet.api.dto.CreateDepositRequest;
import com.ledger.wallet.api.dto.CreateWithdrawalRequest;
import com.ledger.wallet.application.usecase.ExternalMovementUseCase;
import com.ledger.wallet.application.usecase.TransferOutcome;
import com.ledger.wallet.infrastructure.security.AuthenticatedUser;
import jakarta.validation.Valid;
import org.springframework.http.MediaType;
import org.springframework.http.ResponseEntity;
import org.springframework.security.core.annotation.AuthenticationPrincipal;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestHeader;
import org.springframework.web.bind.annotation.RestController;

/** FR-6/FR-7: admin-only DEPOSIT and WITHDRAWAL, enforced by SecurityConfig (ROLE_ADMIN). */
@RestController
public class ExternalMovementController {

    private final ExternalMovementUseCase externalMovementUseCase;

    public ExternalMovementController(ExternalMovementUseCase externalMovementUseCase) {
        this.externalMovementUseCase = externalMovementUseCase;
    }

    @PostMapping("/deposits")
    public ResponseEntity<String> createDeposit(
            @RequestHeader(value = "Idempotency-Key", required = false) String idempotencyKey,
            @Valid @RequestBody CreateDepositRequest request,
            @AuthenticationPrincipal AuthenticatedUser principal
    ) {
        TransferOutcome outcome = externalMovementUseCase.deposit(request, idempotencyKey, principal);
        return ResponseEntity.status(outcome.httpStatus())
                .contentType(MediaType.APPLICATION_JSON)
                .body(outcome.body());
    }

    @PostMapping("/withdrawals")
    public ResponseEntity<String> createWithdrawal(
            @RequestHeader(value = "Idempotency-Key", required = false) String idempotencyKey,
            @Valid @RequestBody CreateWithdrawalRequest request,
            @AuthenticationPrincipal AuthenticatedUser principal
    ) {
        TransferOutcome outcome = externalMovementUseCase.withdraw(request, idempotencyKey, principal);
        return ResponseEntity.status(outcome.httpStatus())
                .contentType(MediaType.APPLICATION_JSON)
                .body(outcome.body());
    }
}
