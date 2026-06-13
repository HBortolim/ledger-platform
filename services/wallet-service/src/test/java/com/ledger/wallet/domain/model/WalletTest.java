package com.ledger.wallet.domain.model;

import com.ledger.wallet.domain.exception.code.DomainErrorCode;
import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.UUID;

import static org.junit.jupiter.api.Assertions.*;

class WalletTest {

    @Test
    void create_withBRL_returnsActiveWallet() {
        UUID ownerId = UUID.randomUUID();
        Result<Wallet, List<DomainError>> result = Wallet.create(ownerId, "BRL");

        assertTrue(result instanceof Result.Success<?, ?>);
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

        assertTrue(result instanceof Result.Failure<?, ?>);
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
}
