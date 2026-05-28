package com.ledger.wallet.api.controller;

import com.ledger.wallet.api.dto.CreateWalletRequest;
import com.ledger.wallet.api.dto.CreateWalletResponse;
import com.ledger.wallet.application.usecase.CreateWalletUseCase;
import jakarta.validation.Valid;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.*;

import java.util.UUID;

@RestController
@RequestMapping("/wallets")
public class WalletController {

    private CreateWalletUseCase createWalletUseCase;

    public WalletController() {

    }

    @PostMapping
    public ResponseEntity<CreateWalletResponse> createWallet(@Valid @RequestBody CreateWalletRequest request) {
        // TODO: implement via use case
        throw new UnsupportedOperationException("not yet implemented");
    }

    @GetMapping("/{walletId}/balance")
    public ResponseEntity<?> getBalance(@PathVariable UUID walletId) {
        // TODO: implement via use case
        throw new UnsupportedOperationException("not yet implemented");
    }

    @PostMapping("/{walletId}/freeze")
    public ResponseEntity<?> freeze(@PathVariable UUID walletId) {
        throw new UnsupportedOperationException("not yet implemented");
    }

    @PostMapping("/{walletId}/unfreeze")
    public ResponseEntity<?> unfreeze(@PathVariable UUID walletId) {
        throw new UnsupportedOperationException("not yet implemented");
    }

    @PostMapping("/{walletId}/close")
    public ResponseEntity<?> close(@PathVariable UUID walletId) {
        throw new UnsupportedOperationException("not yet implemented");
    }
}
