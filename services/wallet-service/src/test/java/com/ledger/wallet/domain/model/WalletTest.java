package com.ledger.wallet.domain.model;

import com.ledger.wallet.domain.exception.WalletConflictException;
import com.ledger.wallet.domain.exception.code.DomainErrorCode;
import org.junit.jupiter.api.Test;

import java.time.Instant;
import java.util.List;
import java.util.UUID;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertSame;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertInstanceOf;
import static org.junit.jupiter.api.Assertions.assertNotEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

class WalletTest {

    @Test
    void create_withBRL_returnsActiveWallet() {
        UUID ownerId = UUID.randomUUID();
        Result<Wallet, List<DomainError>> result = Wallet.create(ownerId, "BRL");

        assertInstanceOf(Result.Success.class, result);
        Wallet wallet = ((Result.Success<Wallet, List<DomainError>>) result).value();
        assertNotNull(wallet.id());
        assertEquals(ownerId, wallet.ownerId());
        assertEquals("BRL", wallet.currency());
        assertEquals(WalletStatus.ACTIVE, wallet.status());
        assertNotNull(wallet.createdAt());
        assertNotNull(wallet.updatedAt());
    }

    @Test
    void create_withUnsupportedCurrency_returnsFailure() {
        Result<Wallet, List<DomainError>> result = Wallet.create(UUID.randomUUID(), "USD");

        assertInstanceOf(Result.Failure.class, result);
    }

    @Test
    void create_withUnsupportedCurrency_hasCorrectErrorCode() {
        Result<Wallet, List<DomainError>> result = Wallet.create(UUID.randomUUID(), "USD");

        List<DomainError> errors = ((Result.Failure<Wallet, List<DomainError>>) result).error();
        assertEquals(1, errors.size());
        assertEquals(DomainErrorCode.UNSUPPORTED_CURRENCY, errors.get(0).code());
    }

    @Test
    void isActive_returnsTrueForActiveWallet() {
        Wallet wallet = ((Result.Success<Wallet, List<DomainError>>) Wallet.create(UUID.randomUUID(), "BRL")).value();
        assertTrue(wallet.isActive());
    }

    @Test
    void isClosed_returnsFalseForActiveWallet() {
        Wallet wallet = ((Result.Success<Wallet, List<DomainError>>) Wallet.create(UUID.randomUUID(), "BRL")).value();
        assertFalse(wallet.isClosed());
    }

    @Test
    void create_generatesUniqueIdsForEachWallet() {
        UUID ownerId = UUID.randomUUID();
        Wallet first = ((Result.Success<Wallet, List<DomainError>>) Wallet.create(ownerId, "BRL")).value();
        Wallet second = ((Result.Success<Wallet, List<DomainError>>) Wallet.create(ownerId, "BRL")).value();

        assertNotEquals(first.id(), second.id());
    }

    // ---- Wallet.apply

    private static final Instant CREATED_AT = Instant.parse("2026-01-01T00:00:00Z");
    private static final Instant UPDATED_AT = Instant.parse("2026-02-02T00:00:00Z");

    private static Wallet walletWith(WalletStatus status) {
        return new Wallet(UUID.randomUUID(), UUID.randomUUID(), "BRL", status, CREATED_AT, UPDATED_AT);
    }

    @Test
    void apply_freezeOnActive_returnsFrozen() {
        Wallet wallet = walletWith(WalletStatus.ACTIVE);

        Wallet result = wallet.apply(AuditAction.FREEZE);

        assertEquals(WalletStatus.FROZEN, result.status());
    }

    @Test
    void apply_freezeOnFrozen_isNoOpReturningSameInstance() {
        Wallet wallet = walletWith(WalletStatus.FROZEN);

        Wallet result = wallet.apply(AuditAction.FREEZE);

        assertSame(wallet, result);
        assertEquals(WalletStatus.FROZEN, result.status());
    }

    @Test
    void apply_freezeOnClosed_throwsWalletClosed() {
        Wallet wallet = walletWith(WalletStatus.CLOSED);

        WalletConflictException ex = assertThrows(WalletConflictException.class,
                () -> wallet.apply(AuditAction.FREEZE));

        assertEquals(DomainErrorCode.WALLET_CLOSED, ex.getCode());
    }

    @Test
    void apply_unfreezeOnFrozen_returnsActive() {
        Wallet wallet = walletWith(WalletStatus.FROZEN);

        Wallet result = wallet.apply(AuditAction.UNFREEZE);

        assertEquals(WalletStatus.ACTIVE, result.status());
    }

    @Test
    void apply_unfreezeOnActive_isNoOpReturningSameInstance() {
        Wallet wallet = walletWith(WalletStatus.ACTIVE);

        assertSame(wallet, wallet.apply(AuditAction.UNFREEZE));
    }

    @Test
    void apply_unfreezeOnClosed_throwsWalletClosed() {
        Wallet wallet = walletWith(WalletStatus.CLOSED);

        WalletConflictException ex = assertThrows(WalletConflictException.class,
                () -> wallet.apply(AuditAction.UNFREEZE));

        assertEquals(DomainErrorCode.WALLET_CLOSED, ex.getCode());
    }

    @Test
    void apply_closeOnActive_returnsClosed() {
        Wallet wallet = walletWith(WalletStatus.ACTIVE);

        assertEquals(WalletStatus.CLOSED, wallet.apply(AuditAction.CLOSE).status());
    }

    @Test
    void apply_closeOnFrozen_returnsClosed() {
        Wallet wallet = walletWith(WalletStatus.FROZEN);

        assertEquals(WalletStatus.CLOSED, wallet.apply(AuditAction.CLOSE).status());
    }

    @Test
    void apply_closeOnClosed_isNoOpReturningSameInstance() {
        Wallet wallet = walletWith(WalletStatus.CLOSED);

        assertSame(wallet, wallet.apply(AuditAction.CLOSE));
    }

    @Test
    void apply_nonLifecycleAction_throwsIllegalArgument() {
        Wallet wallet = walletWith(WalletStatus.ACTIVE);

        assertThrows(IllegalArgumentException.class, () -> wallet.apply(AuditAction.PROJECTION_REBUILD));
    }

    @Test
    void apply_preservesIdentityAndTimestamps() {
        Wallet wallet = walletWith(WalletStatus.ACTIVE);

        Wallet result = wallet.apply(AuditAction.FREEZE);

        assertEquals(wallet.id(), result.id());
        assertEquals(wallet.ownerId(), result.ownerId());
        assertEquals(wallet.currency(), result.currency());
        assertEquals(CREATED_AT, result.createdAt());
        assertEquals(UPDATED_AT, result.updatedAt());
    }
}
