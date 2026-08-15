package com.ledger.wallet.application.usecase;

import com.ledger.wallet.api.dto.BalanceResponse;
import com.ledger.wallet.domain.exception.WalletAccessDeniedException;
import com.ledger.wallet.domain.model.Balance;
import com.ledger.wallet.domain.model.Wallet;
import com.ledger.wallet.infrastructure.security.AuthenticatedUser;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.stereotype.Service;
import org.springframework.transaction.annotation.Transactional;

import java.math.BigDecimal;
import java.math.RoundingMode;
import java.time.Duration;
import java.time.Instant;
import java.util.UUID;

@Service
public class GetBalanceUseCase {

    private static final Logger logger = LoggerFactory.getLogger(GetBalanceUseCase.class);
    private static final Duration STALE_THRESHOLD = Duration.ofSeconds(5);

    private final WalletRepository walletRepository;
    private final BalanceRepository balanceRepository;

    public GetBalanceUseCase(WalletRepository walletRepository, BalanceRepository balanceRepository) {
        this.walletRepository = walletRepository;
        this.balanceRepository = balanceRepository;
    }

    @Transactional(readOnly = true)
    public BalanceResponse execute(UUID walletId, AuthenticatedUser principal) {
        Wallet wallet = walletRepository.getById(walletId, principal.userId())
                .orElseThrow(WalletAccessDeniedException::new);

        Balance row = balanceRepository.getByWalletId(walletId);

        BigDecimal balance = row.balance() != null ? row.balance() : BigDecimal.ZERO.setScale(2, RoundingMode.UNNECESSARY);
        Instant asOf = row.lastAppliedAt() != null ? row.lastAppliedAt() : wallet.createdAt();

        boolean stale = row.latestEntryAt() != null
                && row.latestEntryAt().isAfter(asOf)
                && Duration.between(row.latestEntryAt(), row.dbNow()).compareTo(STALE_THRESHOLD) > 0;

        return new BalanceResponse(walletId, balance, wallet.currency(), asOf, stale);
    }
}
