package com.ledger.wallet.application.usecase;

import com.ledger.wallet.domain.model.AuditAction;
import com.ledger.wallet.infrastructure.projection.ProjectionClient;
import com.ledger.wallet.infrastructure.projection.dto.RebuildResult;
import com.ledger.wallet.infrastructure.security.AuthenticatedUser;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.stereotype.Service;
import org.springframework.transaction.annotation.Transactional;

import java.util.Optional;
import java.util.UUID;

@Service
public class RebuildProjectionUseCase {

    private static final Logger logger = LoggerFactory.getLogger(RebuildProjectionUseCase.class);

    private final ProjectionClient projectionClient;
    private final WalletRepository walletRepository;
    private final WalletAuditRepository walletAuditRepository;

    public RebuildProjectionUseCase(ProjectionClient projectionClient,
                                    WalletRepository walletRepository,
                                    WalletAuditRepository walletAuditRepository) {
        this.projectionClient = projectionClient;
        this.walletRepository = walletRepository;
        this.walletAuditRepository = walletAuditRepository;
    }

    @Transactional
    public Optional<RebuildResult> execute(UUID walletId, AuthenticatedUser principal) {

        var wallet = walletRepository.getById(walletId);
        if (wallet.isEmpty()) {
            return Optional.empty();
        }

        RebuildResult response = projectionClient.rebuild(walletId);

        walletAuditRepository.recordAction(walletId, principal.userId(), AuditAction.PROJECTION_REBUILD);

        logger.warn("Projection rebuilt for wallet {} by {}", walletId, principal.userId());

        return Optional.of(response);
    }
}
