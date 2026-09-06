package com.ledger.wallet.domain.model;

public enum AuditAction {
    FREEZE(WalletStatus.FROZEN),
    UNFREEZE(WalletStatus.ACTIVE),
    CLOSE(WalletStatus.CLOSED),

    /** Task 05: an admin rebuilt this wallet's projection. Not a status change. */
    PROJECTION_REBUILD(null);

    private final WalletStatus resultingStatus;

    AuditAction(WalletStatus resultingStatus) {
        this.resultingStatus = resultingStatus;
    }

    public boolean isStatusTransition() {
        return resultingStatus != null;
    }

    /** The status a wallet must be in after this action. Null for non-transitions. */
    public WalletStatus resultingStatus() {
        return resultingStatus;
    }
}
