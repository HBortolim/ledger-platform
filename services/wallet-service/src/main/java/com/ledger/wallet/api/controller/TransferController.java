package com.ledger.wallet.api.controller;

import com.ledger.wallet.api.dto.CreateTransferRequest;
import com.ledger.wallet.application.usecase.CreateTransferUseCase;
import com.ledger.wallet.application.usecase.TransferOutcome;
import com.ledger.wallet.infrastructure.security.AuthenticatedUser;
import jakarta.validation.Valid;
import org.springframework.http.MediaType;
import org.springframework.http.ResponseEntity;
import org.springframework.security.core.annotation.AuthenticationPrincipal;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestHeader;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

@RestController
@RequestMapping("/transfers")
public class TransferController {

    private final CreateTransferUseCase createTransferUseCase;

    public TransferController(CreateTransferUseCase createTransferUseCase) {
        this.createTransferUseCase = createTransferUseCase;
    }

    @PostMapping
    public ResponseEntity<String> createTransfer(
            @RequestHeader(value = "Idempotency-Key", required = false) String idempotencyKey,
            @Valid @RequestBody CreateTransferRequest request,
            @AuthenticationPrincipal AuthenticatedUser principal
    ) {
        TransferOutcome outcome = createTransferUseCase.execute(request, idempotencyKey, principal);
        return ResponseEntity.status(outcome.httpStatus())
                .contentType(MediaType.APPLICATION_JSON)
                .body(outcome.body());
    }
}
