package com.ledger.wallet.infrastructure.projection;

import com.github.tomakehurst.wiremock.WireMockServer;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.web.client.RestClient;

import java.time.Duration;
import java.util.UUID;

import static com.github.tomakehurst.wiremock.client.WireMock.*;
import static org.junit.jupiter.api.Assertions.*;

class ProjectionClientTest {

    private WireMockServer wireMock;
    private ProjectionClient client;

    @BeforeEach
    void setUp() {
        wireMock = new WireMockServer(0);
        wireMock.start();
        RestClient restClient = ProjectionClientConfig.buildRestClient(
                wireMock.baseUrl(), Duration.ofSeconds(2), Duration.ofSeconds(5));
        client = new ProjectionClient(restClient);
    }

    @AfterEach
    void tearDown() {
        wireMock.stop();
    }

    @Test
    void rebuild_returns200_mapsToRebuildResult() {
        UUID walletId = UUID.randomUUID();
        // Path parity with the Projection Service's own route (Task 04, §9.7): this is the
        // assertion that catches a client hitting the wrong URL and still "succeeding" because
        // WireMock happened to be stubbed loosely.
        wireMock.stubFor(post(urlEqualTo("/admin/projections/" + walletId + "/rebuild"))
                .willReturn(aResponse().withStatus(200).withHeader("Content-Type", "application/json")
                        .withBody("""
                                {
                                  "walletId": "%s",
                                  "balance": "300.00",
                                  "entriesApplied": 2,
                                  "rebuiltAt": "2026-05-19T14:23:00.123Z"
                                }
                                """.formatted(walletId)))
        );

        var result = client.rebuild(walletId);

        assertEquals(walletId, result.walletId());
        assertEquals("300.00", result.balance().toPlainString());
        assertEquals(2, result.entriesApplied());
        assertNotNull(result.rebuiltAt());
    }

    @Test
    void rebuild_returns400_throwsProjectionUnavailable() {
        UUID walletId = UUID.randomUUID();
        wireMock.stubFor(post(urlEqualTo("/admin/projections/" + walletId + "/rebuild"))
                .willReturn(aResponse().withStatus(400)));

        assertThrows(ProjectionUnavailableException.class, () -> client.rebuild(walletId));
    }

    @Test
    void rebuild_returns500_throwsProjectionUnavailable() {
        UUID walletId = UUID.randomUUID();
        wireMock.stubFor(post(urlEqualTo("/admin/projections/" + walletId + "/rebuild"))
                .willReturn(aResponse().withStatus(500)));

        assertThrows(ProjectionUnavailableException.class, () -> client.rebuild(walletId));
    }

    // A connection failure (the Projection Service container is down) must degrade the same
    // way an upstream error does -- never propagate a raw RestClient/network exception.
    @Test
    void rebuild_connectionFailure_throwsProjectionUnavailable() {
        wireMock.stop(); // nothing listening on this port now

        assertThrows(ProjectionUnavailableException.class, () -> client.rebuild(UUID.randomUUID()));
    }

    // NFR-AVAIL-4 in spirit: the cause is preserved for logs, never swallowed.
    @Test
    void rebuild_connectionFailure_preservesCause() {
        wireMock.stop();

        ProjectionUnavailableException ex = assertThrows(ProjectionUnavailableException.class,
                () -> client.rebuild(UUID.randomUUID()));

        assertNotNull(ex.getCause());
    }
}
