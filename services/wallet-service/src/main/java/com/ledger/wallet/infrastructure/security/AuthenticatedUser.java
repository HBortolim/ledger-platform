package com.ledger.wallet.infrastructure.security;

import org.springframework.security.core.GrantedAuthority;
import org.springframework.security.core.authority.SimpleGrantedAuthority;

import java.util.Collection;
import java.util.List;
import java.util.UUID;

public record AuthenticatedUser(UUID userId, String role) {

    public Collection<GrantedAuthority> authorities() {
        return role != null
                ? List.of(new SimpleGrantedAuthority("ROLE_" + role.toUpperCase()))
                : List.of();
    }
}
