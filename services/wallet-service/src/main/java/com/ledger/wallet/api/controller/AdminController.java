package com.ledger.wallet.api.controller;

import com.ledger.wallet.application.usecase.RebuildProjectionUseCase;
import com.ledger.wallet.infrastructure.projection.dto.RebuildResult;
import com.ledger.wallet.infrastructure.security.AuthenticatedUser;
import org.springframework.http.ResponseEntity;
import org.springframework.security.core.annotation.AuthenticationPrincipal;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

import java.util.UUID;

@RestController
@RequestMapping("/admin")
public class AdminController {

    private final RebuildProjectionUseCase rebuildProjectionUseCase;

    public AdminController(RebuildProjectionUseCase rebuildProjectionUseCase) {
        this.rebuildProjectionUseCase = rebuildProjectionUseCase;
    }

    @PostMapping("/projections/{walletId}/rebuild")
    public ResponseEntity<RebuildResult> rebuild(@PathVariable UUID walletId,
                                                 @AuthenticationPrincipal AuthenticatedUser principal) {
        var response =  rebuildProjectionUseCase.execute(walletId, principal);
        return response.map(rebuildResult -> ResponseEntity.ok().body(rebuildResult))
                .orElseGet(() -> ResponseEntity.notFound().build());
    }
}

