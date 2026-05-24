package com.ledger.wallet.api.controller;

import com.ledger.wallet.api.dto.CreateTransferRequest;
import com.ledger.wallet.api.dto.TransferResponse;
import jakarta.validation.Valid;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.*;

@RestController
@RequestMapping("/transfers")
public class TransferController {

    @PostMapping
    public ResponseEntity<TransferResponse> createTransfer(
            @RequestHeader(value = "Idempotency-Key", required = false) String idempotencyKey,
            @Valid @RequestBody CreateTransferRequest request
    ) {
        // TODO: implement via use case
        throw new UnsupportedOperationException("not yet implemented");
    }
}
