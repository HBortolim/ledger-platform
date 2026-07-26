package com.ledger.wallet.application.idempotency;

import org.junit.jupiter.api.Test;

import java.util.UUID;

import static org.assertj.core.api.Assertions.assertThat;

class IdempotencyKeysTest {

    // RFC 4122 UUIDv5, verified against Python's uuid.uuid5(uuid.NAMESPACE_DNS, name).
    private static final UUID NAMESPACE_DNS = UUID.fromString("6ba7b810-9dad-11d1-80b4-00c04fd430c8");

    @Test
    void uuidV5_matchesKnownVector_wwwExampleCom() {
        UUID result = IdempotencyKeys.uuidV5(NAMESPACE_DNS, "www.example.com");

        assertThat(result).isEqualTo(UUID.fromString("2ed6657d-e927-568b-95e1-2665a8aea6a2"));
    }

    @Test
    void uuidV5_matchesKnownVector_pythonOrg() {
        UUID result = IdempotencyKeys.uuidV5(NAMESPACE_DNS, "python.org");

        assertThat(result).isEqualTo(UUID.fromString("886313e1-3b8a-5372-9b90-0c9aee199e5d"));
    }

    @Test
    void uuidV5_setsVersionAndVariantBits() {
        UUID result = IdempotencyKeys.uuidV5(NAMESPACE_DNS, "anything");

        assertThat(result.version()).isEqualTo(5);
        assertThat(result.variant()).isEqualTo(2); // RFC 4122
    }

    @Test
    void transactionId_sameUserAndKey_isDeterministic() {
        UUID userId = UUID.randomUUID();

        UUID first = IdempotencyKeys.transactionId(userId, "key-1");
        UUID second = IdempotencyKeys.transactionId(userId, "key-1");

        assertThat(first).isEqualTo(second);
    }

    @Test
    void transactionId_differentKey_isDifferent() {
        UUID userId = UUID.randomUUID();

        UUID first = IdempotencyKeys.transactionId(userId, "key-1");
        UUID second = IdempotencyKeys.transactionId(userId, "key-2");

        assertThat(first).isNotEqualTo(second);
    }

    @Test
    void transactionId_differentUser_sameKey_isDifferent() {
        UUID first = IdempotencyKeys.transactionId(UUID.randomUUID(), "same-key");
        UUID second = IdempotencyKeys.transactionId(UUID.randomUUID(), "same-key");

        assertThat(first).isNotEqualTo(second);
    }

    @Test
    void randomTransactionId_isNotDeterministic() {
        UUID first = IdempotencyKeys.randomTransactionId();
        UUID second = IdempotencyKeys.randomTransactionId();

        assertThat(first).isNotEqualTo(second);
    }
}
