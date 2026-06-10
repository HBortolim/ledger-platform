package com.ledger.wallet.api.controller;

import com.ledger.wallet.api.dto.CreateWalletRequest;
import com.ledger.wallet.api.dto.CreateWalletResponse;
import com.ledger.wallet.api.dto.GetWalletResponse;
import com.ledger.wallet.application.usecase.CreateWalletUseCase;
import com.ledger.wallet.application.usecase.GetWalletUseCase;
import com.ledger.wallet.infrastructure.security.AuthenticatedUser;
import jakarta.validation.Valid;
import org.springframework.http.HttpStatus;
import org.springframework.http.ResponseEntity;
import org.springframework.security.core.annotation.AuthenticationPrincipal;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

import java.util.UUID;

@RestController
@RequestMapping("/wallets")
public class WalletController {

    private final CreateWalletUseCase createWalletUseCase;
    private final GetWalletUseCase getWalletUseCase;

    public WalletController(CreateWalletUseCase createWalletUseCase, GetWalletUseCase getWalletUseCase) {
        this.createWalletUseCase = createWalletUseCase;
        this.getWalletUseCase = getWalletUseCase;
    }

    @PostMapping
    public ResponseEntity<CreateWalletResponse> createWallet(
            @Valid @RequestBody CreateWalletRequest request,
            @AuthenticationPrincipal AuthenticatedUser principal) {
        CreateWalletResponse response = createWalletUseCase.execute(request, principal);
        return ResponseEntity.status(201)
                .header("Location", "/wallets/" + response.walletId())
                .body(response);
    }

    @GetMapping("/{id}")
    public ResponseEntity<GetWalletResponse> getWallet(@PathVariable UUID id, @AuthenticationPrincipal AuthenticatedUser principal) {
        final var response = getWalletUseCase.execute(id, principal);
        return response.map(getWalletResponse ->
                ResponseEntity.status(HttpStatus.OK).body(getWalletResponse))
                .orElseGet(() -> ResponseEntity.notFound().build());
    }

    @GetMapping("/{walletId}/balance")
    public ResponseEntity<?> getBalance(@PathVariable UUID walletId, @AuthenticationPrincipal AuthenticatedUser principal) {
        // TODO: implement via use case
        throw new UnsupportedOperationException("not yet implemented");
    }

    @PostMapping("/{walletId}/freeze")
    public ResponseEntity<?> freeze(@PathVariable UUID walletId, @AuthenticationPrincipal AuthenticatedUser principal) {
        throw new UnsupportedOperationException("not yet implemented");
    }

    @PostMapping("/{walletId}/unfreeze")
    public ResponseEntity<?> unfreeze(@PathVariable UUID walletId, @AuthenticationPrincipal AuthenticatedUser principal) {
        throw new UnsupportedOperationException("not yet implemented");
    }

    @PostMapping("/{walletId}/close")
    public ResponseEntity<?> close(@PathVariable UUID walletId, @AuthenticationPrincipal AuthenticatedUser principal) {
        throw new UnsupportedOperationException("not yet implemented");
    }
}
