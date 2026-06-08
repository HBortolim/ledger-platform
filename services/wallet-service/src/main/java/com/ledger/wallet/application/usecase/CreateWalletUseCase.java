package com.ledger.wallet.application.usecase;

import com.ledger.wallet.api.dto.CreateWalletRequest;
import com.ledger.wallet.api.dto.CreateWalletResponse;
import com.ledger.wallet.domain.exception.DomainValidationException;
import com.ledger.wallet.domain.model.Wallet;
import com.ledger.wallet.infrastructure.security.AuthenticatedUser;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.security.access.AccessDeniedException;
import org.springframework.stereotype.Service;
import org.springframework.transaction.annotation.Transactional;

@Service
public class CreateWalletUseCase {

    private static final Logger LOGGER = LoggerFactory.getLogger(CreateWalletUseCase.class);

    private final WalletRepository repository;

    public CreateWalletUseCase(WalletRepository repository) {
        this.repository = repository;
    }

    @Transactional
    public CreateWalletResponse execute(CreateWalletRequest request, AuthenticatedUser principal) {
        LOGGER.info("Create Wallet Request for user: {}", principal.userId());

        if (!request.ownerId().equals(principal.userId())) {
            throw new AccessDeniedException("Cannot create wallet for another user");
        }

        Wallet wallet = Wallet.create(request.ownerId(), request.currency())
                .orElseThrow(DomainValidationException::new);

        repository.save(wallet);

        LOGGER.info("Wallet created: {} for user: {}", wallet.id(), principal.userId());

        return new CreateWalletResponse(
                wallet.id(),
                wallet.ownerId(),
                wallet.currency(),
                wallet.status().name(),
                wallet.createdAt()
        );
    }
}
