package com.ledger.wallet.infrastructure.persistence;

import com.ledger.wallet.application.usecase.WalletAuditRepository;
import com.ledger.wallet.application.usecase.WalletRepository;
import com.ledger.wallet.domain.model.AuditAction;
import com.ledger.wallet.domain.model.Wallet;
import com.ledger.wallet.domain.model.WalletStatus;
import com.ledger.wallet.support.BaseIntegrationTest;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.EnumSource;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.jdbc.core.namedparam.MapSqlParameterSource;
import org.springframework.jdbc.core.namedparam.NamedParameterJdbcTemplate;
import org.springframework.transaction.PlatformTransactionManager;
import org.springframework.transaction.annotation.Propagation;
import org.springframework.transaction.annotation.Transactional;
import org.springframework.transaction.support.TransactionTemplate;

import java.time.Instant;
import java.util.Map;
import java.util.UUID;

import static org.assertj.core.api.Assertions.assertThat;

class WalletAuditJdbcRepositoryIT extends BaseIntegrationTest {

    @Autowired
    private WalletAuditRepository repository;

    @Autowired
    private NamedParameterJdbcTemplate jdbcTemplate;

    @Autowired
    private WalletRepository walletRepository;

    @Autowired
    private PlatformTransactionManager transactionManager;

    private Map<String, Object> lastRowFor(UUID walletId) {
        return jdbcTemplate.queryForMap("""
                SELECT id,
                       action,
                       actor_id,
                       before_state->>'status' AS before_status,
                       after_state->>'status'  AS after_status,
                       before_state IS NULL    AS before_is_null,
                       after_state IS NULL     AS after_is_null,
                       at
                  FROM wallet_db.wallet_audit_log
                 WHERE wallet_id = :walletId
                 ORDER BY id DESC
                 LIMIT 1
                """, new MapSqlParameterSource().addValue("walletId", walletId));
    }

    private Integer countFor(UUID walletId) {
        return jdbcTemplate.queryForObject("""
                SELECT COUNT(*)
                  FROM wallet_db.wallet_audit_log
                 WHERE wallet_id = :walletId
                """, new MapSqlParameterSource().addValue("walletId", walletId), Integer.class);
    }

    @Test
    void recordStatusChange_FreezeState_SerializeAllFieldsCorrectly() {
        UUID walletId = UUID.randomUUID();
        UUID actorId = UUID.randomUUID();
        AuditAction action = AuditAction.FREEZE;
        WalletStatus beforeState = WalletStatus.ACTIVE;
        WalletStatus afterState = WalletStatus.FROZEN;

        repository.recordStatusChange(walletId, actorId, action, beforeState, afterState);

        Map<String, Object> row = lastRowFor(walletId);
        assertThat(row.get("id")).isNotNull();
        assertThat(row)
                .containsEntry("action", action.name())
                .containsEntry("actor_id", actorId)
                .containsEntry("before_status", beforeState.name())
                .containsEntry("after_status", afterState.name());
        assertThat(row.get("at")).isNotNull();
    }

    @Test
    void recordAction_ProjectionRebuildState_SerializeAllFieldsCorrectly() {
        UUID walletId = UUID.randomUUID();
        UUID actorId = UUID.randomUUID();
        AuditAction action = AuditAction.PROJECTION_REBUILD;

        repository.recordAction(walletId, actorId, action);

        Map<String, Object> row = lastRowFor(walletId);
        assertThat(row.get("id")).isNotNull();
        assertThat(row)
                .containsEntry("actor_id", actorId)
                .containsEntry("action", action.name());
        assertThat(row.get("at")).isNotNull();
        assertThat(row.get("before_is_null")).isEqualTo(Boolean.TRUE);
        assertThat(row.get("after_is_null")).isEqualTo(Boolean.TRUE);
    }

    @Test
    void recordStatusChange_MultipleRecords_InsertAll() {
        UUID walletId = UUID.randomUUID();

        // Row 1
        UUID actorId = UUID.randomUUID();
        AuditAction action = AuditAction.FREEZE;
        WalletStatus beforeState = WalletStatus.ACTIVE;
        WalletStatus afterState = WalletStatus.FROZEN;

        // Row 2
        UUID secondActorId = UUID.randomUUID();
        AuditAction secondAction = AuditAction.UNFREEZE;
        WalletStatus secondBeforeState = WalletStatus.FROZEN;
        WalletStatus secondAfterState = WalletStatus.ACTIVE;

        repository.recordStatusChange(walletId, actorId, action, beforeState, afterState);

        Map<String, Object> rowOne = lastRowFor(walletId);

        repository.recordStatusChange(walletId, secondActorId, secondAction, secondBeforeState, secondAfterState);

        Map<String, Object> rowTwo = lastRowFor(walletId);

        assertThat(countFor(walletId)).isEqualTo(2);
        assertThat(rowOne.get("id")).isNotEqualTo(rowTwo.get("id"));
        assertThat(rowOne.get("actor_id")).isNotEqualTo(rowTwo.get("actor_id"));
        assertThat(rowOne.get("before_status")).isNotEqualTo(rowTwo.get("before_status"));
        assertThat(rowOne.get("after_status")).isNotEqualTo(rowTwo.get("after_status"));
    }

    @Test
    void recordStatusChange_ActorDiffersFromOwner_StoresTheActor() {
        UUID ownerId = UUID.randomUUID();
        UUID adminId = UUID.randomUUID();
        Instant now = Instant.now();
        Wallet wallet = new Wallet(UUID.randomUUID(), ownerId, "BRL", WalletStatus.ACTIVE, now, now);
        walletRepository.save(wallet);

        repository.recordStatusChange(wallet.id(), adminId, AuditAction.FREEZE,
                WalletStatus.ACTIVE, WalletStatus.FROZEN);

        Map<String, Object> row = lastRowFor(wallet.id());
        assertThat(row.get("actor_id")).isEqualTo(adminId);
        assertThat(row.get("actor_id")).isNotEqualTo(ownerId);
        assertThat(walletRepository.getById(wallet.id()).orElseThrow().ownerId()).isEqualTo(ownerId);
    }

    @Test
    @Transactional(propagation = Propagation.NOT_SUPPORTED)
    void recordStatusChange_CallerRollsBack_WritesNothing() {
        UUID walletId = UUID.randomUUID();
        UUID actorId = UUID.randomUUID();

        TransactionTemplate template = new TransactionTemplate(transactionManager);
        template.executeWithoutResult(status -> {
            repository.recordStatusChange(walletId, actorId, AuditAction.FREEZE,
                    WalletStatus.ACTIVE, WalletStatus.FROZEN);
            // row visible within transaction
            assertThat(countFor(walletId)).isEqualTo(1);
            status.setRollbackOnly();
        });

        assertThat(countFor(walletId)).isZero();
    }

    @ParameterizedTest
    @EnumSource(AuditAction.class)
    void everyAuditAction_PersistsAsItsEnumName(AuditAction action) {
        UUID walletId = UUID.randomUUID();
        UUID actorId = UUID.randomUUID();

        if (action.isStatusTransition()) {
            WalletStatus before = action.resultingStatus() == WalletStatus.ACTIVE
                    ? WalletStatus.FROZEN
                    : WalletStatus.ACTIVE;
            repository.recordStatusChange(walletId, actorId, action, before, action.resultingStatus());
        } else {
            repository.recordAction(walletId, actorId, action);
        }

        assertThat(lastRowFor(walletId)).containsEntry("action", action.name());
    }
}
