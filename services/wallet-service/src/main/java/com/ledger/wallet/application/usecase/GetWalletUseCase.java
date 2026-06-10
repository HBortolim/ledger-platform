package com.ledger.wallet.application.usecase;

import com.ledger.wallet.api.dto.GetWalletResponse;
import com.ledger.wallet.infrastructure.security.AuthenticatedUser;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.stereotype.Service;
import org.springframework.transaction.annotation.Transactional;

import java.util.Optional;
import java.util.UUID;

@Service
public class GetWalletUseCase {

    private static final Logger logger = LoggerFactory.getLogger(GetWalletUseCase.class);

    private final WalletRepository repository;

    public GetWalletUseCase(WalletRepository walletRepository) {
        this.repository = walletRepository;
    }

    @Transactional(readOnly = true)
    public Optional<GetWalletResponse> execute(UUID walletId, AuthenticatedUser principal) {

        final var walletOpt = repository.getById(walletId, principal.userId());
        if (walletOpt.isEmpty()) return Optional.empty();

        final var wallet = walletOpt.get();
        return Optional.of(
                new GetWalletResponse(wallet.id(),
                        wallet.currency(),
                        wallet.status().name(),
                        wallet.createdAt())
        );

    }

}
