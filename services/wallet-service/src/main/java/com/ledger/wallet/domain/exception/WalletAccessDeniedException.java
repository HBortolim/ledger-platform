package com.ledger.wallet.domain.exception;

/**
 * Source wallet missing or not owned by the JWT subject (AC-5.11). Deliberately carries no
 * detail — the response must be identical whether the wallet doesn't exist or just isn't
 * the caller's, so there's no existence leak.
 */
public class WalletAccessDeniedException extends RuntimeException {}
