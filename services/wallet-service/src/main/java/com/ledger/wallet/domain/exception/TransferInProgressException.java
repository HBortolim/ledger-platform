package com.ledger.wallet.domain.exception;

/**
 * AC-5.16: a duplicate request waited out the in-flight window and the original still
 * hasn't completed (and the §9.2 ledger recovery check found nothing posted either).
 */
public class TransferInProgressException extends RuntimeException {}
