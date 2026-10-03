package com.ledger.wallet.infrastructure.projection;

import com.ledger.wallet.infrastructure.projection.dto.RebuildResult;
import org.springframework.http.MediaType;
import org.springframework.stereotype.Component;
import org.springframework.web.client.ResourceAccessException;
import org.springframework.web.client.RestClient;

import java.util.UUID;

@Component
public class ProjectionClient {

    private final RestClient restClient;

    public ProjectionClient(RestClient projectionRestClient) {
        this.restClient = projectionRestClient;
    }

    public RebuildResult rebuild(UUID walletId) {

        try {
            return restClient.post()
                    // Path matches the Projection Service's own route exactly (Task 04) —
                    // §9.7's URL is literally the one an operator types; only host and auth differ.
                    .uri("/admin/projections/{walletId}/rebuild", walletId)
                    .exchange((request, response) -> {
                        int status = response.getStatusCode().value();
                        if (status == 200) {
                            return response.bodyTo(RebuildResult.class);
                        }
                        // 400 here means the Wallet Service sent something the Projection Service
                        // rejected -- a contract mismatch, not a client error we caused on purpose
                        // (the walletId is already a validated UUID by the time we get here).
                        // Every other status is the same "can't trust this response" outcome.
                        throw new ProjectionUnavailableException("Error rebuilding wallet");
                    });
        }
        catch (ResourceAccessException e) {
            throw new ProjectionUnavailableException("Error rebuilding wallet", e);
        }
    }
}
