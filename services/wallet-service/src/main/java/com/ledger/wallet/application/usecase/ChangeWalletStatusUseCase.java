package com.ledger.wallet.application.usecase;

import com.ledger.wallet.api.dto.GetWalletResponse;
import com.ledger.wallet.domain.exception.WalletAccessDeniedException;
import com.ledger.wallet.domain.exception.WalletConflictException;
import com.ledger.wallet.domain.exception.code.DomainErrorCode;
import com.ledger.wallet.domain.model.AuditAction;
import com.ledger.wallet.domain.model.Wallet;
import com.ledger.wallet.domain.model.WalletStatus;
import com.ledger.wallet.infrastructure.security.AuthenticatedUser;
import org.springframework.stereotype.Service;
import org.springframework.transaction.annotation.Transactional;

import java.util.UUID;

@Service
public class ChangeWalletStatusUseCase {

    private final WalletRepository walletRepository;
    private final BalanceRepository balanceRepository;
    private final WalletAuditRepository auditRepository;

    public ChangeWalletStatusUseCase(WalletRepository walletRepository, BalanceRepository balanceRepository,  WalletAuditRepository auditRepository) {
        this.walletRepository = walletRepository;
        this.balanceRepository = balanceRepository;
        this.auditRepository = auditRepository;
    }

    @Transactional
    public GetWalletResponse freeze(UUID walletId, AuthenticatedUser principal) {
        return transition(walletId, principal, AuditAction.FREEZE);
    }

    @Transactional
    public GetWalletResponse unfreeze(UUID walletId, AuthenticatedUser principal) {
        return transition(walletId, principal, AuditAction.UNFREEZE);
    }

    @Transactional
    public GetWalletResponse close(UUID walletId, AuthenticatedUser principal) {
        return transition(walletId, principal, AuditAction.CLOSE);
    }

    private GetWalletResponse transition(UUID walletId, AuthenticatedUser principal, AuditAction action) {
        Wallet wallet = walletRepository.getById(walletId, principal.userId())
                .orElseThrow(WalletAccessDeniedException::new);

        if (action == AuditAction.CLOSE && wallet.status() != WalletStatus.CLOSED
                && balanceRepository.getLedgerBalance(walletId).signum() != 0) {
            throw new WalletConflictException(DomainErrorCode.NONZERO_BALANCE,
                    "wallet balance must be zero to close");
        }

        Wallet updated = wallet.apply(action);
        if (updated.status() == wallet.status()) {
            return response(wallet);
        }

        walletRepository.updateStatus(walletId, updated.status());
        auditRepository.recordStatusChange(walletId, principal.userId(), action,
                wallet.status(), updated.status());
        return response(updated);
    }

    private GetWalletResponse response(Wallet wallet) {
        return new GetWalletResponse(wallet.id(),
                wallet.currency(),
                wallet.status().name(),
                wallet.createdAt());
    }

}
