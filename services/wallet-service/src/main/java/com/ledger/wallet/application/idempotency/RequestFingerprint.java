package com.ledger.wallet.application.idempotency;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.ledger.wallet.api.dto.CreateTransferRequest;

import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.util.LinkedHashMap;
import java.util.Map;

/**
 * SHA-256 fingerprint over a canonical re-serialization of the bound request (stable field
 * order, {@link java.math.BigDecimal#toPlainString()} for the amount) so that whitespace or
 * key-order differences in the incoming JSON never defeat AC-5.12's replay check.
 *
 * <p>{@code description} is deliberately excluded: it doesn't affect the ledger posting (only
 * the wallets and amount do), so varying it alone must not trip AC-5.13's
 * IDEMPOTENCY_KEY_MISMATCH on an otherwise-identical retry.
 */
public final class RequestFingerprint {

    private static final ObjectMapper MAPPER = new ObjectMapper();

    private RequestFingerprint() {}

    public static String of(CreateTransferRequest request) {
        Map<String, Object> canonical = new LinkedHashMap<>();
        canonical.put("sourceWalletId", request.sourceWalletId().toString());
        canonical.put("destinationWalletId", request.destinationWalletId().toString());
        canonical.put("amount", request.amount().toPlainString());

        try {
            String json = MAPPER.writeValueAsString(canonical);
            return sha256Hex(json);
        } catch (Exception e) {
            throw new IllegalStateException("Failed to compute request fingerprint", e);
        }
    }

    private static String sha256Hex(String input) {
        try {
            MessageDigest digest = MessageDigest.getInstance("SHA-256");
            byte[] hash = digest.digest(input.getBytes(StandardCharsets.UTF_8));
            StringBuilder hex = new StringBuilder(hash.length * 2);
            for (byte b : hash) {
                hex.append(String.format("%02x", b));
            }
            return hex.toString();
        } catch (NoSuchAlgorithmException e) {
            throw new IllegalStateException("SHA-256 unavailable", e);
        }
    }
}
