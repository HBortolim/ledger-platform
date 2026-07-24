package com.ledger.wallet.infrastructure.ledger;

import com.ledger.wallet.infrastructure.ledger.dto.LedgerErrorResponse;
import com.ledger.wallet.infrastructure.ledger.dto.LedgerPosting;
import com.ledger.wallet.infrastructure.ledger.dto.PostPostingEntryRequest;
import com.ledger.wallet.infrastructure.ledger.dto.PostPostingRequest;
import org.springframework.stereotype.Component;
import org.springframework.web.client.ResourceAccessException;
import org.springframework.web.client.RestClient;

import java.math.BigDecimal;
import java.math.RoundingMode;
import java.util.List;
import java.util.Optional;
import java.util.UUID;

/**
 * Every Ledger outcome is either a {@link PostPostingResult}
 * variant or a thrown {@link LedgerUnavailableException} — never a raw HTTP
 * exception leaking upward. No retries here: retrying is the caller's
 * contract via idempotency keys (SPEC.md §13 principle 6).
 */
@Component
public class LedgerClient {

    private final RestClient restClient;

    public LedgerClient(RestClient ledgerRestClient) {
        this.restClient = ledgerRestClient;
    }

    public PostPostingResult postPosting(UUID transactionId, String type, String description, List<LedgerEntryInstruction> entries) {
        PostPostingRequest body = new PostPostingRequest(
                transactionId,
                type,
                description,
                entries.stream()
                        .map(e -> new PostPostingEntryRequest(e.accountId(), e.entryType().name(), formatAmount(e.amount())))
                        .toList()
        );

        try {
            return restClient.post()
                    .uri("/ledger/postings")
                    .body(body)
                    .exchange((request, response) -> {
                        int status = response.getStatusCode().value();
                        if (status == 201) {
                            LedgerPosting posting = response.bodyTo(LedgerPosting.class);
                            return new PostPostingResult.Posted(posting.postedAt(), posting.entries());
                        }
                        if (status == 409) {
                            return new PostPostingResult.AlreadyPosted(response.bodyTo(LedgerPosting.class));
                        }
                        if (status == 422) {
                            LedgerErrorResponse error = response.bodyTo(LedgerErrorResponse.class);
                            return new PostPostingResult.Rejected(error.code(), error.message());
                        }
                        throw new LedgerUnavailableException("ledger service responded with unexpected status " + status);
                    });
        } catch (ResourceAccessException ex) {
            throw new LedgerUnavailableException("ledger service unreachable", ex);
        }
    }

    public Optional<LedgerPosting> getTransaction(UUID transactionId) {
        try {
            return restClient.get()
                    .uri("/admin/ledger/transactions/{id}", transactionId)
                    .exchange((request, response) -> {
                        int status = response.getStatusCode().value();
                        if (status == 200) {
                            return Optional.of(response.bodyTo(LedgerPosting.class));
                        }
                        if (status == 404) {
                            return Optional.<LedgerPosting>empty();
                        }
                        throw new LedgerUnavailableException("ledger service responded with unexpected status " + status);
                    });
        } catch (ResourceAccessException ex) {
            throw new LedgerUnavailableException("ledger service unreachable", ex);
        }
    }

    private static String formatAmount(BigDecimal amount) {
        return amount.setScale(2, RoundingMode.UNNECESSARY).toPlainString();
    }
}
