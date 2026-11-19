package com.ledger.wallet.api.controller;

import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

import javax.sql.DataSource;
import java.sql.Connection;
import java.util.Map;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.TimeUnit;

@RestController
@RequestMapping("/health")
public class HealthController {

    private static final int PROBE_TIMEOUT_SECONDS = 2;

    private final DataSource dataSource;

    public HealthController(DataSource dataSource) {
        this.dataSource = dataSource;
    }

    @GetMapping("/live")
    public ResponseEntity<Map<String, String>> live() {
        return ResponseEntity.ok(Map.of("status", "UP"));
    }

    @GetMapping("/ready")
    public ResponseEntity<Map<String, String>> ready() {
        if (databaseAnswers()) {
            return ResponseEntity.ok(Map.of("status", "UP"));
        }
        return ResponseEntity.status(503).body(Map.of("status", "DOWN"));
    }

    // Bounded as a whole: getConnection() alone can wait out Hikari's 30 s timeout on a saturated pool,
    // which would hang the probe exactly when the service is under the most load.
    private boolean databaseAnswers() {
        try {
            return CompletableFuture.supplyAsync(this::ping).get(PROBE_TIMEOUT_SECONDS, TimeUnit.SECONDS);
        } catch (Exception e) {
            if (e instanceof InterruptedException) {
                Thread.currentThread().interrupt();
            }
            return false;
        }
    }

    private boolean ping() {
        try (Connection connection = dataSource.getConnection()) {
            return connection.isValid(PROBE_TIMEOUT_SECONDS);
        } catch (Exception e) {
            return false;
        }
    }
}
