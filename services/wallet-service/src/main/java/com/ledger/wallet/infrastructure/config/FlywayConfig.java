package com.ledger.wallet.infrastructure.config;

import javax.sql.DataSource;

import org.flywaydb.core.Flyway;
import org.springframework.beans.factory.InitializingBean;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;

@Configuration
public class FlywayConfig {

    @Bean
    public InitializingBean flywayMigration(
            DataSource dataSource,
            @Value("${spring.flyway.locations}") String locations,
            @Value("${spring.flyway.schemas}") String schemas,
            @Value("${spring.flyway.baseline-on-migrate}") boolean baselineOnMigrate) {
        return () -> Flyway.configure()
                .dataSource(dataSource)
                .locations(locations)
                .schemas(schemas)
                .baselineOnMigrate(baselineOnMigrate)
                .load()
                .migrate();
    }
}
