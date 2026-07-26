package com.ledger.wallet.application.idempotency;

import java.nio.ByteBuffer;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.util.UUID;

/**
 * Deterministic {@code transactionId} derivation (ADR-0006). Same {@code (userId, key)}
 * always yields the same UUID, which is what makes the §9.2 crash-recovery path possible.
 */
public final class IdempotencyKeys {

    // Fixed namespace for UUIDv5 derivation. Never change this value once transactions
    // exist in production — doing so would break re-derivation of past transactionIds.
    public static final UUID NAMESPACE = UUID.fromString("331b3e69-c5f0-425b-b7c0-0d5ef39ae323");

    private IdempotencyKeys() {}

    public static UUID transactionId(UUID userId, String idempotencyKey) {
        return uuidV5(NAMESPACE, userId + ":" + idempotencyKey);
    }

    public static UUID randomTransactionId() {
        return UUID.randomUUID();
    }

    /** RFC 4122 UUIDv5 (SHA-1, name-based). */
    static UUID uuidV5(UUID namespace, String name) {
        try {
            MessageDigest sha1 = MessageDigest.getInstance("SHA-1");
            ByteBuffer namespaceBytes = ByteBuffer.allocate(16);
            namespaceBytes.putLong(namespace.getMostSignificantBits());
            namespaceBytes.putLong(namespace.getLeastSignificantBits());
            sha1.update(namespaceBytes.array());

            byte[] hash = sha1.digest(name.getBytes(StandardCharsets.UTF_8));

            hash[6] &= 0x0f;
            hash[6] |= 0x50; // version 5
            hash[8] &= 0x3f;
            hash[8] |= (byte) 0x80; // RFC 4122 variant

            ByteBuffer buffer = ByteBuffer.wrap(hash, 0, 16);
            return new UUID(buffer.getLong(), buffer.getLong());
        } catch (NoSuchAlgorithmException e) {
            throw new IllegalStateException("SHA-1 unavailable", e);
        }
    }
}
