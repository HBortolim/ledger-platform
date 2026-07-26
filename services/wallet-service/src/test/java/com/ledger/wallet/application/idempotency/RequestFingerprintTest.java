package com.ledger.wallet.application.idempotency;

import com.ledger.wallet.api.dto.CreateTransferRequest;
import org.junit.jupiter.api.Test;

import java.math.BigDecimal;
import java.util.UUID;

import static org.assertj.core.api.Assertions.assertThat;

class RequestFingerprintTest {

    @Test
    void sameLogicalRequest_producesSameFingerprint() {
        UUID source = UUID.randomUUID();
        UUID destination = UUID.randomUUID();

        CreateTransferRequest a = new CreateTransferRequest(source, destination, new BigDecimal("100.00"), "rent");
        CreateTransferRequest b = new CreateTransferRequest(source, destination, new BigDecimal("100.00"), "rent");

        assertThat(RequestFingerprint.of(a)).isEqualTo(RequestFingerprint.of(b));
    }

    @Test
    void fingerprint_isSha256Hex() {
        CreateTransferRequest request = new CreateTransferRequest(
                UUID.randomUUID(), UUID.randomUUID(), new BigDecimal("1.00"), null);

        String fingerprint = RequestFingerprint.of(request);

        assertThat(fingerprint).hasSize(64).matches("[0-9a-f]{64}");
    }

    @Test
    void differentAmount_producesDifferentFingerprint() {
        UUID source = UUID.randomUUID();
        UUID destination = UUID.randomUUID();

        CreateTransferRequest a = new CreateTransferRequest(source, destination, new BigDecimal("100.00"), null);
        CreateTransferRequest b = new CreateTransferRequest(source, destination, new BigDecimal("999.00"), null);

        assertThat(RequestFingerprint.of(a)).isNotEqualTo(RequestFingerprint.of(b));
    }

    @Test
    void differentWallets_producesDifferentFingerprint() {
        CreateTransferRequest a = new CreateTransferRequest(
                UUID.randomUUID(), UUID.randomUUID(), new BigDecimal("100.00"), null);
        CreateTransferRequest b = new CreateTransferRequest(
                UUID.randomUUID(), UUID.randomUUID(), new BigDecimal("100.00"), null);

        assertThat(RequestFingerprint.of(a)).isNotEqualTo(RequestFingerprint.of(b));
    }

    @Test
    void amountScale_100_and_100_00_produceDifferentFingerprints() {
        // BigDecimal.toPlainString() distinguishes "100" from "100.00" -- deliberately
        // stricter than numeric equality, since the two didn't arrive as the same JSON bytes.
        UUID source = UUID.randomUUID();
        UUID destination = UUID.randomUUID();

        CreateTransferRequest a = new CreateTransferRequest(source, destination, new BigDecimal("100"), null);
        CreateTransferRequest b = new CreateTransferRequest(source, destination, new BigDecimal("100.00"), null);

        assertThat(RequestFingerprint.of(a)).isNotEqualTo(RequestFingerprint.of(b));
    }

    @Test
    void nullDescription_doesNotThrow() {
        CreateTransferRequest request = new CreateTransferRequest(
                UUID.randomUUID(), UUID.randomUUID(), new BigDecimal("5.00"), null);

        assertThat(RequestFingerprint.of(request)).isNotBlank();
    }

    @Test
    void differentDescription_sameWalletsAndAmount_producesSameFingerprint() {
        // description doesn't affect the ledger posting (source/dest/amount do) -- varying it
        // alone must not trip AC-5.13's IDEMPOTENCY_KEY_MISMATCH on an otherwise-identical retry.
        UUID source = UUID.randomUUID();
        UUID destination = UUID.randomUUID();

        CreateTransferRequest a = new CreateTransferRequest(source, destination, new BigDecimal("100.00"), "rent");
        CreateTransferRequest b = new CreateTransferRequest(source, destination, new BigDecimal("100.00"), "rent (corrected memo)");

        assertThat(RequestFingerprint.of(a)).isEqualTo(RequestFingerprint.of(b));
    }
}
