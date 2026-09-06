package com.ledger.wallet.application.usecase;

import com.ledger.wallet.domain.model.AuditAction;
import com.ledger.wallet.domain.model.WalletStatus;

import java.util.UUID;

public interface WalletAuditRepository {

    void recordStatusChange(UUID walletId, UUID actorId, AuditAction action, WalletStatus beforeState, WalletStatus afterState);
    void recordAction(UUID walletId, UUID actorId, AuditAction action);

}
