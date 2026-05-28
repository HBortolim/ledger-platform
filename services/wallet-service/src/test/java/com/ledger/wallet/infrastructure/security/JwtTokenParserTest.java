package com.ledger.wallet.infrastructure.security;

import com.ledger.wallet.support.JwtTestHelper;
import io.jsonwebtoken.JwtException;
import io.jsonwebtoken.Jwts;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.security.KeyPairGenerator;
import java.util.UUID;

import static org.junit.jupiter.api.Assertions.*;

class JwtTokenParserTest {

    private JwtTokenParser parser;

    @BeforeEach
    void setUp() {
        parser = new JwtTokenParser(JwtTestHelper.publicKey());
    }

    @Test
    void parsesUserToken() {
        UUID userId = UUID.randomUUID();
        AuthenticatedUser user = parser.parse(JwtTestHelper.tokenFor(userId));

        assertEquals(userId, user.userId());
        assertNull(user.role());
    }

    @Test
    void parsesAdminToken() {
        UUID userId = UUID.randomUUID();
        AuthenticatedUser user = parser.parse(JwtTestHelper.adminTokenFor(userId));

        assertEquals(userId, user.userId());
        assertEquals("admin", user.role());
    }

    @Test
    void rejectsTokenSignedWithDifferentKey() throws Exception {
        var otherKey = KeyPairGenerator.getInstance("RSA").generateKeyPair().getPrivate();
        String token = Jwts.builder()
                .subject(UUID.randomUUID().toString())
                .signWith(otherKey, Jwts.SIG.RS256)
                .compact();

        assertThrows(JwtException.class, () -> parser.parse(token));
    }

    @Test
    void rejectsTamperedToken() {
        String token = JwtTestHelper.tokenFor(UUID.randomUUID());
        String tampered = token.substring(0, token.length() - 4) + "XXXX";

        assertThrows(JwtException.class, () -> parser.parse(tampered));
    }
}
