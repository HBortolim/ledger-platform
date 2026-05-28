package com.ledger.wallet.support;

import org.springframework.boot.test.context.TestConfiguration;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Primary;

import java.security.interfaces.RSAPublicKey;

@TestConfiguration
public class TestSecurityConfig {

    @Bean
    @Primary
    public RSAPublicKey jwtPublicKey() {
        return JwtTestHelper.publicKey();
    }
}
