package com.ledger.wallet.infrastructure.security;

import io.jsonwebtoken.Claims;
import io.jsonwebtoken.JwtException;
import io.jsonwebtoken.JwtParser;
import io.jsonwebtoken.Jwts;
import org.springframework.stereotype.Component;

import java.security.interfaces.RSAPublicKey;
import java.util.UUID;

@Component
public class JwtTokenParser {

    private final JwtParser parser;

    public JwtTokenParser(RSAPublicKey jwtPublicKey) {
        this.parser = Jwts.parser().verifyWith(jwtPublicKey).build();
    }

    public AuthenticatedUser parse(String token) {
        Claims claims = parser.parseSignedClaims(token).getPayload();

        String sub = claims.getSubject();
        if (sub == null) {
            throw new JwtException("Missing sub claim");
        }
        UUID userId;
        try {
            userId = UUID.fromString(sub);
        } catch (IllegalArgumentException e) {
            throw new JwtException("sub claim is not a valid UUID: " + sub, e);
        }

        return new AuthenticatedUser(userId, claims.get("role", String.class));
    }
}
