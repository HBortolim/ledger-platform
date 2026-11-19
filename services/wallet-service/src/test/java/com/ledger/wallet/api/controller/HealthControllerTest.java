package com.ledger.wallet.api.controller;

import org.junit.jupiter.api.Test;
import org.springframework.http.HttpStatus;

import javax.sql.DataSource;
import java.sql.Connection;
import java.sql.SQLException;
import java.time.Duration;

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

class HealthControllerTest {

    @Test
    void liveIsAlwaysUp() {
        var controller = new HealthController(mock(DataSource.class));

        assertThat(controller.live().getStatusCode()).isEqualTo(HttpStatus.OK);
    }

    @Test
    void readyIsUpWhenTheDatabaseAnswers() throws SQLException {
        var response = new HealthController(dataSourceWithConnection(true)).ready();

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(response.getBody()).containsEntry("status", "UP");
    }

    @Test
    void readyIsDownWhenTheConnectionIsNotValid() throws SQLException {
        var response = new HealthController(dataSourceWithConnection(false)).ready();

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.SERVICE_UNAVAILABLE);
        assertThat(response.getBody()).containsEntry("status", "DOWN");
    }

    @Test
    void readyIsDownWhenTheDatabaseRefusesConnections() throws SQLException {
        DataSource dataSource = mock(DataSource.class);
        when(dataSource.getConnection()).thenThrow(new SQLException("connection refused"));

        var response = new HealthController(dataSource).ready();

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.SERVICE_UNAVAILABLE);
    }

    @Test
    void readyGivesUpInsteadOfWaitingForASaturatedPool() throws SQLException {
        DataSource dataSource = mock(DataSource.class);
        when(dataSource.getConnection()).thenAnswer(invocation -> {
            Thread.sleep(10_000);
            return null;
        });

        long start = System.nanoTime();
        var response = new HealthController(dataSource).ready();
        Duration elapsed = Duration.ofNanos(System.nanoTime() - start);

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.SERVICE_UNAVAILABLE);
        assertThat(elapsed).isLessThan(Duration.ofSeconds(5));
    }

    private static DataSource dataSourceWithConnection(boolean valid) throws SQLException {
        DataSource dataSource = mock(DataSource.class);
        Connection connection = mock(Connection.class);
        when(dataSource.getConnection()).thenReturn(connection);
        when(connection.isValid(2)).thenReturn(valid);
        return dataSource;
    }
}
